package brain

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	authv1 "yufeng/proto/gen/authv1"
	commonv1 "yufeng/proto/gen/commonv1"
	userv1 "yufeng/proto/gen/userv1"
	"yufeng/proto/gen/userv1/userv1connect"
)

// UserServer 是平台用户管理服务，写路径要求 user.admin。
type UserServer struct {
	pool              *pgxpool.Pool
	passwordMinLength int32
}

// NewUserServer 构造用户管理服务。
func NewUserServer(pool *pgxpool.Pool, passwordMinLength int32) *UserServer {
	if passwordMinLength < MinPasswordLength {
		passwordMinLength = MinPasswordLength
	}
	return &UserServer{pool: pool, passwordMinLength: passwordMinLength}
}

// Handler 返回 Connect 服务端处理器。
func (u *UserServer) Handler() (string, http.Handler) {
	return userv1connect.NewUserServiceHandler(u, handlerOptions()...)
}

func (u *UserServer) requireAdmin(ctx context.Context, req interface{ Header() http.Header }) error {
	user, err := requireUser(ctx, u.pool, req)
	if err != nil {
		return err
	}
	return authorizeWrite(ctx, u.pool, user, "user.admin", "", "", false)
}

// CreateUser 由管理员创建本地账户并写入初始角色与密码摘要。
func (u *UserServer) CreateUser(ctx context.Context, req *connect.Request[userv1.CreateUserRequest]) (*connect.Response[userv1.CreateUserResponse], error) {
	if err := u.requireAdmin(ctx, req); err != nil {
		return nil, err
	}
	if err := validatePassword(req.Msg.Password, u.passwordMinLength); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	role, err := roleString(req.Msg.Role)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Msg.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	resp := &userv1.CreateUserResponse{}
	err = idempotentProto(ctx, u.pool, "user.create", idempotencyKey(req.Header()), req.Msg, resp, func(tx pgx.Tx) error {
		userID, err := newID("usr")
		if err != nil {
			return err
		}
		row := tx.QueryRow(ctx, `INSERT INTO users(user_id, username, display_name, role, state, password_hash)
	VALUES($1,$2,$3,$4,'active',$5)
	RETURNING user_id, username, display_name, role, state, created_at, updated_at, NULL::timestamptz AS last_login_at`,
			userID, req.Msg.Username, req.Msg.DisplayName, role, string(hash))
		created, err := scanUser(row)
		if err != nil {
			if isUniqueViolation(err) {
				return connect.NewError(connect.CodeAlreadyExists, errors.New("username already exists"))
			}
			return err
		}
		resp.User = created
		return nil
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

// ListUsers 按查询条件分页列出本地账户的非敏感信息。
func (u *UserServer) ListUsers(ctx context.Context, req *connect.Request[userv1.ListUsersRequest]) (*connect.Response[userv1.ListUsersResponse], error) {
	if err := u.requireAdmin(ctx, req); err != nil {
		return nil, err
	}
	limit := ClampPageSize(req.Msg.GetPageSize())
	offset, err := decodePageOffset(req.Msg.GetPageToken())
	if err != nil {
		return nil, err
	}
	rows, err := u.pool.Query(ctx, `SELECT user_id, username, display_name, role, state,
	       created_at, updated_at, last_login_at
	FROM users WHERE ($1='' OR username ILIKE '%'||$1||'%' OR display_name ILIKE '%'||$1||'%')
	  AND ($2='' OR role=$2) AND ($3='' OR state=$3)
	ORDER BY created_at DESC LIMIT $4 OFFSET $5`, req.Msg.Query, roleFilter(req.Msg.Role), stateFilter(req.Msg.State), limit+1, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []*authv1.User
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	if rows.Err() != nil {
		return nil, rows.Err()
	}
	resp := &userv1.ListUsersResponse{Users: users}
	if len(users) > limit {
		resp.Users = users[:limit]
		resp.NextPageToken = encodePageOffset(offset + limit)
	}
	return connect.NewResponse(resp), nil
}

// GetUser 返回指定本地账户的非敏感信息。
func (u *UserServer) GetUser(ctx context.Context, req *connect.Request[userv1.GetUserRequest]) (*connect.Response[userv1.GetUserResponse], error) {
	if err := u.requireAdmin(ctx, req); err != nil {
		return nil, err
	}
	row := u.pool.QueryRow(ctx, `SELECT user_id, username, display_name, role, state,
	       created_at, updated_at, last_login_at FROM users WHERE user_id=$1`, req.Msg.UserId)
	user, err := scanUser(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("user not found"))
		}
		return nil, err
	}
	return connect.NewResponse(&userv1.GetUserResponse{User: user}), nil
}

// UpdateUser 修改指定账户的显示名、角色或启用状态，并保护最后一个管理账户。
func (u *UserServer) UpdateUser(ctx context.Context, req *connect.Request[userv1.UpdateUserRequest]) (*connect.Response[userv1.UpdateUserResponse], error) {
	if err := u.requireAdmin(ctx, req); err != nil {
		return nil, err
	}
	if req.Msg.GetUser() == nil || req.Msg.GetUpdateMask() == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("user and update_mask are required"))
	}
	sets := []string{"updated_at=now()"}
	args := []any{req.Msg.UserId}
	var requestedRole, requestedState string
	arg := func(v any) string { args = append(args, v); return "$" + strconv.Itoa(len(args)) }
	// FieldMask 由 proto 标准库表达为路径列表；v1 只允许改三个字段。
	for _, path := range req.Msg.UpdateMask.Paths {
		switch path {
		case "display_name":
			sets = append(sets, "display_name="+arg(req.Msg.User.DisplayName))
		case "role":
			role, err := roleString(req.Msg.User.Role)
			if err != nil {
				return nil, connect.NewError(connect.CodeInvalidArgument, err)
			}
			sets = append(sets, "role="+arg(role))
			requestedRole = role
		case "state":
			state, err := stateString(req.Msg.User.State)
			if err != nil {
				return nil, connect.NewError(connect.CodeInvalidArgument, err)
			}
			sets = append(sets, "state="+arg(state))
			requestedState = state
		default:
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("unsupported update path "+path))
		}
	}
	if len(sets) == 1 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("update mask is empty"))
	}
	resp := &userv1.UpdateUserResponse{}
	err := idempotentProto(ctx, u.pool, "user.update", idempotencyKey(req.Header()), req.Msg, resp, func(tx pgx.Tx) error {
		if err := lockUserAdministration(ctx, tx); err != nil {
			return err
		}
		if err := protectEffectiveAdministratorMutation(ctx, tx, req.Msg.GetUserId(), requestedRole, requestedState); err != nil {
			return persistRPCError(err)
		}
		row := tx.QueryRow(ctx, `UPDATE users SET `+strings.Join(sets, ",")+` WHERE user_id=$1
			RETURNING user_id, username, display_name, role, state, created_at, updated_at, last_login_at`, args...)
		user, err := scanUser(row)
		if errors.Is(err, pgx.ErrNoRows) {
			return persistRPCError(connect.NewError(connect.CodeNotFound, errors.New("user not found")))
		}
		if err != nil {
			return err
		}
		resp.User = user
		if requestedState != "" && requestedState != "active" {
			_, err = tx.Exec(ctx, `DELETE FROM user_sessions WHERE user_id=$1`, req.Msg.GetUserId())
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

// DeleteUser 删除指定账户并保护当前账户与最后一个管理账户。
func (u *UserServer) DeleteUser(ctx context.Context, req *connect.Request[userv1.DeleteUserRequest]) (*connect.Response[userv1.DeleteUserResponse], error) {
	actor, err := requireUser(ctx, u.pool, req)
	if err != nil {
		return nil, err
	}
	if err := authorizeWrite(ctx, u.pool, actor, "user.admin", "", "", false); err != nil {
		return nil, err
	}
	if actor.GetUserId() == req.Msg.GetUserId() {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("cannot delete the current user"))
	}
	resp := &userv1.DeleteUserResponse{}
	err = idempotentProto(ctx, u.pool, "user.delete", idempotencyKey(req.Header()), req.Msg, resp, func(tx pgx.Tx) error {
		if err := lockUserAdministration(ctx, tx); err != nil {
			return err
		}
		if err := protectEffectiveAdministratorMutation(ctx, tx, req.Msg.GetUserId(), "", "deleted"); err != nil {
			return persistRPCError(err)
		}
		row := tx.QueryRow(ctx, `UPDATE users SET state='deleted', updated_at=now() WHERE user_id=$1
			RETURNING user_id, username, display_name, role, state, created_at, updated_at, last_login_at`, req.Msg.UserId)
		user, err := scanUser(row)
		if errors.Is(err, pgx.ErrNoRows) {
			return persistRPCError(connect.NewError(connect.CodeNotFound, errors.New("user not found")))
		}
		if err != nil {
			return err
		}
		resp.User = user
		_, err = tx.Exec(ctx, `DELETE FROM user_sessions WHERE user_id=$1`, req.Msg.UserId)
		return err
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

// AdminResetPassword 由管理员替换指定账户密码并撤销其既有会话。
func (u *UserServer) AdminResetPassword(ctx context.Context, req *connect.Request[userv1.AdminResetPasswordRequest]) (*connect.Response[userv1.AdminResetPasswordResponse], error) {
	if err := u.requireAdmin(ctx, req); err != nil {
		return nil, err
	}
	if err := validatePassword(req.Msg.NewPassword, u.passwordMinLength); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Msg.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	resp := &userv1.AdminResetPasswordResponse{}
	err = idempotentProto(ctx, u.pool, "user.reset_password", idempotencyKey(req.Header()), req.Msg, resp, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `UPDATE users SET password_hash=$1, updated_at=now() WHERE user_id=$2
			RETURNING user_id, username, display_name, role, state, created_at, updated_at, last_login_at`,
			string(hash), req.Msg.UserId)
		user, err := scanUser(row)
		if errors.Is(err, pgx.ErrNoRows) {
			return persistRPCError(connect.NewError(connect.CodeNotFound, errors.New("user not found")))
		}
		if err != nil {
			return err
		}
		resp.User = user
		if req.Msg.RevokeSessions {
			_, err = tx.Exec(ctx, `DELETE FROM user_sessions WHERE user_id=$1`, req.Msg.UserId)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func lockUserAdministration(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('yufeng:user-administration'))`)
	return err
}

func protectEffectiveAdministratorMutation(ctx context.Context, tx pgx.Tx, userID, nextRole, nextState string) error {
	var currentRole, currentState string
	if err := tx.QueryRow(ctx, `SELECT role,state FROM users WHERE user_id=$1 FOR UPDATE`, userID).Scan(&currentRole, &currentState); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return connect.NewError(connect.CodeNotFound, errors.New("user not found"))
		}
		return err
	}
	currentAdmin, err := effectiveAdministrator(ctx, tx, userID, currentRole, currentState)
	if err != nil || !currentAdmin {
		return err
	}
	if nextRole == "" {
		nextRole = currentRole
	}
	if nextState == "" {
		nextState = currentState
	}
	nextAdmin, err := effectiveAdministrator(ctx, tx, userID, nextRole, nextState)
	if err != nil || nextAdmin {
		return err
	}
	var activeAdmins int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM users u
		WHERE u.state='active' AND (
			(EXISTS (SELECT 1 FROM deployment_onboarding WHERE id=1 AND state<>$1) AND u.role='admin') OR
			EXISTS (SELECT 1 FROM grants g WHERE g.subject_kind='user' AND g.subject_id=u.user_id
				AND (g.expires_at IS NULL OR g.expires_at>now()) AND g.tools ? 'user.admin')
		)`, OnboardingStateCompleted).Scan(&activeAdmins); err != nil {
		return err
	}
	if activeAdmins <= 1 {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("cannot remove the last effective administrator"))
	}
	return nil
}

func effectiveAdministrator(ctx context.Context, tx pgx.Tx, userID, role, state string) (bool, error) {
	if state != "active" {
		return false, nil
	}
	var completed, granted bool
	if err := tx.QueryRow(ctx, `SELECT state=$1 FROM deployment_onboarding WHERE id=1`, OnboardingStateCompleted).Scan(&completed); err != nil {
		return false, err
	}
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM grants WHERE subject_kind='user' AND subject_id=$1
		AND (expires_at IS NULL OR expires_at>now()) AND tools ? 'user.admin')`, userID).Scan(&granted); err != nil {
		return false, err
	}
	return granted || (!completed && role == "admin"), nil
}

func roleFilter(r commonv1.UserRole) string {
	s, _ := roleString(r)
	return s
}

func stateFilter(s commonv1.UserState) string {
	v, _ := stateString(s)
	return v
}

func stateString(s commonv1.UserState) (string, error) {
	switch s {
	case commonv1.UserState_USER_STATE_ACTIVE:
		return "active", nil
	case commonv1.UserState_USER_STATE_DISABLED:
		return "disabled", nil
	case commonv1.UserState_USER_STATE_DELETED:
		return "deleted", nil
	default:
		return "", errors.New("unknown user state")
	}
}
