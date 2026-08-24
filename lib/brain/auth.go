package brain

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	authv1 "yufeng/proto/gen/authv1"
	"yufeng/proto/gen/authv1/authv1connect"
	commonv1 "yufeng/proto/gen/commonv1"

	"yufeng/lib/kernel"
)

// AuthServer 是操作域认证服务。
type AuthServer struct {
	pool                  *pgxpool.Pool
	sessionTTL            time.Duration
	allowSelfRegistration bool
	passwordMinLength     int32
	loginMu               sync.Mutex
	loginFailures         map[string]loginAttempt
	loginRate             *windowLimiter
	trustedProxies        []netip.Prefix
}

// SetTrustedProxies 配置只有直接对端命中时才可信的转发代理网段。
func (a *AuthServer) SetTrustedProxies(prefixes []netip.Prefix) {
	a.trustedProxies = append([]netip.Prefix(nil), prefixes...)
}

// 登录限流窗口：窗口内失败次数达到 maxLoginFailures 即拒绝该用户名。
const (
	loginFailureWindow = time.Minute
	maxLoginFailures   = 5
)

type loginAttempt struct {
	count       int
	windowStart time.Time
}

// NewAuthServer 构造认证服务。
func NewAuthServer(pool *pgxpool.Pool, sessionTTL time.Duration, allowSelfRegistration bool, passwordMinLength int32) *AuthServer {
	if sessionTTL <= 0 {
		sessionTTL = 12 * time.Hour
	}
	if passwordMinLength < MinPasswordLength {
		passwordMinLength = MinPasswordLength
	}
	return &AuthServer{
		pool:                  pool,
		sessionTTL:            sessionTTL,
		allowSelfRegistration: allowSelfRegistration,
		passwordMinLength:     passwordMinLength,
		loginFailures:         map[string]loginAttempt{},
		loginRate:             newWindowLimiter(kernel.LoginRatePerMinute, time.Minute),
	}
}

// Handler 返回 Connect 服务端处理器。
func (a *AuthServer) Handler() (string, http.Handler) {
	return authv1connect.NewAuthServiceHandler(a, handlerOptions()...)
}

// dummyPasswordHash 是用户不存在时也要执行的占位比较目标：bcrypt 耗时
// 60ms+，只对存在的用户执行会让响应时差构成用户枚举侧信道。
var dummyPasswordHash, _ = bcrypt.GenerateFromPassword([]byte("yufeng-dummy-password"), bcrypt.DefaultCost)

// Login 校验本地账户凭据并建立可撤销的浏览器会话。
func (a *AuthServer) Login(ctx context.Context, req *connect.Request[authv1.LoginRequest]) (*connect.Response[authv1.LoginResponse], error) {
	username := req.Msg.Username
	password := req.Msg.Password
	if username == "" || password == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("username and password are required"))
	}
	src := requestSource(req.Peer().Addr, req.Header(), a.trustedProxies)
	if a.loginRate != nil && !a.loginRate.Allow(username+"|"+src, time.Now()) {
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("login rate exceeded"))
	}
	if !a.allowLogin(username + "|" + src) {
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("too many login attempts"))
	}
	row := a.pool.QueryRow(ctx, `SELECT user_id, username, display_name, role, state,
	       created_at, updated_at, last_login_at, password_hash
	FROM users WHERE username=$1`, username)
	var u authv1.User
	var role, state string
	var createdAt, updatedAt time.Time
	var lastLogin *time.Time
	var passwordHash string
	if err := row.Scan(&u.UserId, &u.Username, &u.DisplayName, &role, &state,
		&createdAt, &updatedAt, &lastLogin, &passwordHash); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// 用户不存在也走一次同代价比较，抹平时差。
			_ = bcrypt.CompareHashAndPassword(dummyPasswordHash, []byte(password))
			a.recordLoginFailure(username + "|" + src)
			return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid username or password"))
		}
		return nil, err
	}
	if state != "active" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("user is disabled"))
	}
	if bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(password)) != nil {
		a.recordLoginFailure(username + "|" + src)
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid username or password"))
	}
	a.clearLoginFailures(username + "|" + src)
	var err error
	if u.Role, err = roleEnum(role); err != nil {
		return nil, err
	}
	if u.State, err = stateEnum(state); err != nil {
		return nil, err
	}
	u.CreatedAt = timestamppb.New(createdAt)
	u.UpdatedAt = timestamppb.New(updatedAt)
	now := time.Now()
	if lastLogin != nil {
		u.LastLoginAt = timestamppb.New(*lastLogin)
	}
	raw, hash, err := newSessionToken()
	if err != nil {
		return nil, err
	}
	expires := now.Add(a.sessionTTL)
	if _, err := a.pool.Exec(ctx, `INSERT INTO user_sessions(token_hash, user_id, expires_at) VALUES($1,$2,$3)`,
		hash, u.UserId, expires); err != nil {
		return nil, err
	}
	if _, err := a.pool.Exec(ctx, `UPDATE users SET last_login_at=$1, updated_at=$1 WHERE user_id=$2`, now, u.UserId); err != nil {
		return nil, err
	}
	u.LastLoginAt = timestamppb.New(now)
	access, err := loadEffectiveAccess(ctx, a.pool, &u)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&authv1.LoginResponse{
		Token:     raw,
		ExpiresAt: timestamppb.New(expires),
		User:      &u,
		Access:    access,
	}), nil
}

// Logout 撤销当前浏览器会话并清除会话凭据。
func (a *AuthServer) Logout(ctx context.Context, req *connect.Request[authv1.LogoutRequest]) (*connect.Response[authv1.LogoutResponse], error) {
	raw := bearerToken(req.Header().Get("Authorization"))
	if raw != "" {
		// 删除失败时令牌仍有效至自然过期，登出语义降级但不报错——
		// 客户端已按登出处理，重试没有意义。
		_, _ = a.pool.Exec(ctx, `DELETE FROM user_sessions WHERE token_hash=$1`, hashToken(raw))
	}
	return connect.NewResponse(&authv1.LogoutResponse{}), nil
}

// GetMe 返回当前账户身份、角色与经授予裁剪后的访问范围。
func (a *AuthServer) GetMe(ctx context.Context, req *connect.Request[authv1.GetMeRequest]) (*connect.Response[authv1.GetMeResponse], error) {
	u, err := requireUser(ctx, a.pool, req)
	if err != nil {
		return nil, err
	}
	access, err := loadEffectiveAccess(ctx, a.pool, u)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&authv1.GetMeResponse{User: u, Access: access}), nil
}

// ChangePassword 校验原密码后更新当前账户密码并撤销既有会话。
func (a *AuthServer) ChangePassword(ctx context.Context, req *connect.Request[authv1.ChangePasswordRequest]) (*connect.Response[authv1.ChangePasswordResponse], error) {
	u, err := requireUser(ctx, a.pool, req)
	if err != nil {
		return nil, err
	}
	if err := validatePassword(req.Msg.NewPassword, a.passwordMinLength); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	var currentHash string
	if err := a.pool.QueryRow(ctx, `SELECT password_hash FROM users WHERE user_id=$1`, u.UserId).Scan(&currentHash); err != nil {
		return nil, err
	}
	if bcrypt.CompareHashAndPassword([]byte(currentHash), []byte(req.Msg.OldPassword)) != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("old password is invalid"))
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(req.Msg.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	raw := bearerToken(req.Header().Get("Authorization"))
	if _, err := a.pool.Exec(ctx, `UPDATE users SET password_hash=$1, updated_at=now() WHERE user_id=$2`, string(newHash), u.UserId); err != nil {
		return nil, err
	}
	if raw != "" {
		if _, err := a.pool.Exec(ctx, `DELETE FROM user_sessions WHERE user_id=$1 AND token_hash<>$2`, u.UserId, hashToken(raw)); err != nil {
			return nil, err
		}
	}
	return connect.NewResponse(&authv1.ChangePasswordResponse{}), nil
}

// Register 仅在允许初始注册时创建首个管理账户。
func (a *AuthServer) Register(ctx context.Context, req *connect.Request[authv1.RegisterRequest]) (*connect.Response[authv1.RegisterResponse], error) {
	if !a.allowSelfRegistration {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("self registration is disabled"))
	}
	if err := validatePassword(req.Msg.Password, a.passwordMinLength); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if req.Msg.Username == "" || len(req.Msg.Username) > 64 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("username must be 1-64 characters"))
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Msg.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	userID, err := newID("usr")
	if err != nil {
		return nil, err
	}
	row := a.pool.QueryRow(ctx, `INSERT INTO users(user_id, username, display_name, role, state, password_hash)
	VALUES($1,$2,$3,'viewer','active',$4)
	RETURNING user_id, username, display_name, role, state, created_at, updated_at, NULL::timestamptz AS last_login_at`,
		userID, req.Msg.Username, req.Msg.DisplayName, string(hash))
	u, err := scanUser(row)
	if err != nil {
		// 只有唯一约束冲突映射为"已存在"；连接错误等原样上抛，避免误报。
		if isUniqueViolation(err) {
			return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("username already exists"))
		}
		return nil, err
	}
	return connect.NewResponse(&authv1.RegisterResponse{User: u}), nil
}

// GetLoginConfig 返回登录页所需的公开注册状态，不泄露账户信息。
func (a *AuthServer) GetLoginConfig(ctx context.Context, _ *connect.Request[authv1.GetLoginConfigRequest]) (*connect.Response[authv1.GetLoginConfigResponse], error) {
	return connect.NewResponse(&authv1.GetLoginConfigResponse{
		AllowSelfRegistration: a.allowSelfRegistration,
		PasswordMinLength:     a.passwordMinLength,
		SessionTtl:            durationpb.New(a.sessionTTL),
	}), nil
}

func (a *AuthServer) allowLogin(username string) bool {
	a.loginMu.Lock()
	defer a.loginMu.Unlock()
	att, ok := a.loginFailures[username]
	if !ok || time.Since(att.windowStart) > loginFailureWindow {
		return true
	}
	return att.count < maxLoginFailures
}

func (a *AuthServer) recordLoginFailure(username string) {
	a.loginMu.Lock()
	defer a.loginMu.Unlock()
	now := time.Now()
	// 顺手淘汰过期条目：攻击者用随机用户名喷洒时 map 只增不减会无界增长。
	for name, att := range a.loginFailures {
		if time.Since(att.windowStart) > loginFailureWindow {
			delete(a.loginFailures, name)
		}
	}
	att := a.loginFailures[username]
	if time.Since(att.windowStart) > loginFailureWindow {
		att = loginAttempt{windowStart: now}
	}
	att.count++
	a.loginFailures[username] = att
}

func (a *AuthServer) clearLoginFailures(username string) {
	a.loginMu.Lock()
	defer a.loginMu.Unlock()
	delete(a.loginFailures, username)
}
func validatePassword(pw string, min int32) error {
	if int32(len(pw)) < min {
		return fmt.Errorf("password must be at least %d characters", min)
	}
	if len(pw) > 128 {
		return errors.New("password must be at most 128 characters")
	}
	return nil
}

func stateEnum(s string) (commonv1.UserState, error) {
	switch s {
	case "active":
		return commonv1.UserState_USER_STATE_ACTIVE, nil
	case "disabled":
		return commonv1.UserState_USER_STATE_DISABLED, nil
	case "deleted":
		return commonv1.UserState_USER_STATE_DELETED, nil
	default:
		return commonv1.UserState_USER_STATE_UNSPECIFIED, fmt.Errorf("unknown user state %q", s)
	}
}
