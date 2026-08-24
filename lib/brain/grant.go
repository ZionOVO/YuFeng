package brain

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/types/known/timestamppb"

	authv1 "yufeng/proto/gen/authv1"
	commonv1 "yufeng/proto/gen/commonv1"
	grantv1 "yufeng/proto/gen/grantv1"
	"yufeng/proto/gen/grantv1/grantv1connect"
)

// GrantServer 装配授予表。生产未注册本服务则拒绝启动。
type GrantServer struct {
	pool *pgxpool.Pool
}

// NewGrantServer 构造授予服务。
func NewGrantServer(pool *pgxpool.Pool) *GrantServer { return &GrantServer{pool: pool} }

// Handler 返回 Connect 处理器。
func (s *GrantServer) Handler() (string, http.Handler) {
	return grantv1connect.NewGrantServiceHandler(s, handlerOptions()...)
}

// ListGrants 分页列出管理员可管理的显式权限授予。
func (s *GrantServer) ListGrants(ctx context.Context, req *connect.Request[grantv1.ListGrantsRequest]) (*connect.Response[grantv1.ListGrantsResponse], error) {
	user, err := requireUser(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	subject := strings.TrimSpace(req.Msg.GetSubjectUserId())
	if subject == "" {
		subject = user.UserId
	}
	if subject != user.UserId {
		if err := authorizeWrite(ctx, s.pool, user, "grant.write", "asset", "", false); err != nil {
			return nil, err
		}
	}
	rows, err := s.pool.Query(ctx, `SELECT grant_id, subject_id, tools, bindings, created_by, created_at, expires_at
		FROM grants WHERE subject_kind='user' AND subject_id=$1 ORDER BY created_at`, subject)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	resp := &grantv1.ListGrantsResponse{}
	for rows.Next() {
		g, err := scanGrant(rows)
		if err != nil {
			return nil, err
		}
		resp.Grants = append(resp.Grants, g)
	}
	return connect.NewResponse(resp), rows.Err()
}

// PutGrant 规范化工具与对象绑定后创建或替换一个权限授予。
func (s *GrantServer) PutGrant(ctx context.Context, req *connect.Request[grantv1.PutGrantRequest]) (*connect.Response[grantv1.PutGrantResponse], error) {
	user, err := requireUser(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	if err := authorizeWrite(ctx, s.pool, user, "grant.write", "asset", "", false); err != nil {
		return nil, err
	}
	if user.UserId == req.Msg.GetSubjectUserId() {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("grant_self"))
	}
	for _, t := range req.Msg.GetTools() {
		if !knownUserTool(t) {
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("grant_unknown_tool"))
		}
	}
	if len(req.Msg.GetBindings()) == 0 && !toolsAccountOnly(req.Msg.GetTools()) {
		return nil, grantMissingError()
	}
	for _, b := range req.Msg.GetBindings() {
		if b.GetId() == "*" || strings.TrimSpace(b.GetId()) == "" {
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("grant_wildcard"))
		}
	}
	if err := checkGrantScope(ctx, s.pool, user, req.Msg.GetBindings()); err != nil {
		return nil, err
	}
	id, err := overlayUserGrant(ctx, s.pool, req.Msg.GetSubjectUserId(), req.Msg.GetTools(), req.Msg.GetBindings(), user.UserId)
	if err != nil {
		return nil, err
	}
	g := &grantv1.Grant{
		GrantId: id, SubjectUserId: req.Msg.GetSubjectUserId(), Tools: req.Msg.GetTools(),
		Bindings: req.Msg.GetBindings(), CreatedBy: user.UserId, CreatedAt: timestamppb.Now(),
	}
	return connect.NewResponse(&grantv1.PutGrantResponse{Grant: g}), nil
}

// RevokeGrant 撤销指定主体的显式权限授予。
func (s *GrantServer) RevokeGrant(ctx context.Context, req *connect.Request[grantv1.RevokeGrantRequest]) (*connect.Response[grantv1.RevokeGrantResponse], error) {
	user, err := requireUser(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	if err := authorizeWrite(ctx, s.pool, user, "grant.write", "asset", "", false); err != nil {
		return nil, err
	}
	g, err := loadGrant(ctx, s.pool, req.Msg.GetGrantId())
	if err != nil {
		return nil, err
	}
	if err := checkGrantScope(ctx, s.pool, user, g.GetBindings()); err != nil {
		return nil, err
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM grants WHERE grant_id=$1`, req.Msg.GetGrantId()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&grantv1.RevokeGrantResponse{}), nil
}

func scanGrant(row pgx.Row) (*grantv1.Grant, error) {
	var id, subject, createdBy string
	var toolsRaw, bindsRaw []byte
	var created time.Time
	var expires *time.Time
	if err := row.Scan(&id, &subject, &toolsRaw, &bindsRaw, &createdBy, &created, &expires); err != nil {
		return nil, err
	}
	var tools []string
	_ = json.Unmarshal(toolsRaw, &tools)
	var binds []*grantv1.BindingRef
	_ = json.Unmarshal(bindsRaw, &binds)
	g := &grantv1.Grant{GrantId: id, SubjectUserId: subject, Tools: tools, Bindings: binds, CreatedBy: createdBy, CreatedAt: timestamppb.New(created)}
	if expires != nil {
		g.ExpiresAt = timestamppb.New(*expires)
	}
	return g, nil
}

// grantMissingError 是无授予或空 Bindings 的固定错误。
func grantMissingError() error {
	return connect.NewError(connect.CodePermissionDenied, errors.New("grant_missing"))
}

// requireUserGrant 要求主体对指定工具与对象有授予。资产类工具空 Bindings 拒绝。
func requireUserGrant(ctx context.Context, pool *pgxpool.Pool, userID, tool, bindingKind, bindingID string) error {
	rows, err := pool.Query(ctx, `SELECT tools, bindings FROM grants WHERE subject_kind='user' AND subject_id=$1 AND (expires_at IS NULL OR expires_at > now())`, userID)
	if err != nil {
		return err
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var toolsRaw, bindsRaw []byte
		if err := rows.Scan(&toolsRaw, &bindsRaw); err != nil {
			return err
		}
		var tools []string
		var binds []*grantv1.BindingRef
		if err := json.Unmarshal(toolsRaw, &tools); err != nil {
			return err
		}
		if err := json.Unmarshal(bindsRaw, &binds); err != nil {
			return err
		}
		if !claimsAllows(tools, tool) {
			continue
		}
		if isAccountTool(tool) && len(binds) == 0 {
			found = true
			continue
		}
		if len(binds) == 0 {
			continue
		}
		for _, b := range binds {
			if b == nil {
				continue
			}
			if (bindingID == "" || b.Id == bindingID) && (bindingKind == "" || b.Kind == bindingKind) {
				found = true
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !found {
		return grantMissingError()
	}
	return nil
}

func knownUserTool(name string) bool {
	switch name {
	case "console.read",
		"case.read", "case.manage", "evidence.approve", "worker.enroll", "worker.capacity.approve", "agent.manage",
		"govern.propose", "govern.gate", "govern.start_shadow",
		"govern.promote_canary", "govern.promote_enforce",
		"govern.rollback", "govern.retire", "govern.deny_feedback",
		"asset.create", "asset.update", "asset.delete", "asset.attach", "asset.detach",
		"run.create", "grant.write", "user.admin", "catalog.manage":
		return true
	default:
		return false
	}
}

func toolsAccountOnly(tools []string) bool {
	if len(tools) == 0 {
		return false
	}
	for _, t := range tools {
		if !isAccountTool(t) {
			return false
		}
	}
	return true
}

func overlayUserGrant(ctx context.Context, pool *pgxpool.Pool, subject string, tools []string, binds []*grantv1.BindingRef, createdBy string) (string, error) {
	toolsRaw, err := json.Marshal(tools)
	if err != nil {
		return "", err
	}
	bindsRaw, err := json.Marshal(binds)
	if err != nil {
		return "", err
	}
	var existing string
	err = pool.QueryRow(ctx, `SELECT grant_id FROM grants WHERE subject_kind='user' AND subject_id=$1 ORDER BY created_at LIMIT 1`, subject).Scan(&existing)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if existing == "" {
		id, err := newID("gr")
		if err != nil {
			return "", err
		}
		if _, err := pool.Exec(ctx, `INSERT INTO grants(grant_id, subject_kind, subject_id, tools, bindings, created_by)
			VALUES($1,'user',$2,$3::jsonb,$4::jsonb,$5)`, id, subject, toolsRaw, bindsRaw, createdBy); err != nil {
			return "", err
		}
		return id, nil
	}
	if _, err := pool.Exec(ctx, `DELETE FROM grants WHERE subject_kind='user' AND subject_id=$1 AND grant_id<>$2`, subject, existing); err != nil {
		return "", err
	}
	if _, err := pool.Exec(ctx, `UPDATE grants SET tools=$2::jsonb, bindings=$3::jsonb, created_by=$4 WHERE grant_id=$1`,
		existing, toolsRaw, bindsRaw, createdBy); err != nil {
		return "", err
	}
	return existing, nil
}

func loadGrant(ctx context.Context, pool *pgxpool.Pool, grantID string) (*grantv1.Grant, error) {
	row := pool.QueryRow(ctx, `SELECT grant_id, subject_id, tools, bindings, created_by, created_at, expires_at
		FROM grants WHERE grant_id=$1`, grantID)
	g, err := scanGrant(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("grant_missing"))
	}
	return g, err
}

func checkGrantScope(ctx context.Context, pool *pgxpool.Pool, user *authv1.User, want []*grantv1.BindingRef) error {
	snap, err := loadOnboarding(ctx, pool)
	if err != nil {
		return err
	}
	if !snap.completed() && user.Role == commonv1.UserRole_USER_ROLE_ADMIN {
		allow := map[string]bool{}
		ids, err := listAssetIDs(ctx, pool)
		if err != nil {
			return err
		}
		for _, id := range ids {
			if id != "" && id != "bootstrap" {
				allow["asset/"+id] = true
			}
		}
		if snap.LocalAssetID != "" && snap.LocalAssetID != "bootstrap" {
			allow["asset/"+snap.LocalAssetID] = true
		}
		for _, b := range want {
			if b == nil {
				continue
			}
			if !allow[b.Kind+"/"+b.Id] {
				return connect.NewError(connect.CodePermissionDenied, errors.New("grant_scope"))
			}
		}
		return nil
	}
	access, err := loadEffectiveAccess(ctx, pool, user)
	if err != nil {
		return err
	}
	allow := map[string]bool{}
	for _, b := range access.Bindings {
		if b != nil {
			allow[b.Kind+"/"+b.Id] = true
		}
	}
	for _, b := range want {
		if b == nil {
			continue
		}
		if !allow[b.Kind+"/"+b.Id] {
			return connect.NewError(connect.CodePermissionDenied, errors.New("grant_scope"))
		}
	}
	return nil
}
