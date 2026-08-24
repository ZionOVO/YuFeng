package brain

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/types/known/timestamppb"

	authv1 "yufeng/proto/gen/authv1"
	commonv1 "yufeng/proto/gen/commonv1"
)

// MinPasswordLength 是全平台密码最小长度（各服务与引导逻辑共用的唯一定义）。
const MinPasswordLength = 8

// isUniqueViolation 判定 PostgreSQL 唯一约束冲突（SQLSTATE 23505）。
// 不比对错误文案：文案随版本与 locale 变化，SQLSTATE 才是稳定契约。
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// newSessionToken 生成 256 位随机会话令牌。
func newSessionToken() (string, string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", "", fmt.Errorf("generate session token: %w", err)
	}
	raw := hex.EncodeToString(b[:])
	return raw, hashToken(raw), nil
}

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func bearerToken(header string) string {
	const prefix = "Bearer "
	if len(header) >= len(prefix) && strings.EqualFold(header[:len(prefix)], prefix) {
		return strings.TrimSpace(header[len(prefix):])
	}
	return ""
}

// requireUser 从 Authorization 头解析用户会话并校验有效期。
func requireUser(ctx context.Context, pool *pgxpool.Pool, req interface {
	Header() http.Header
}) (*authv1.User, error) {
	raw := bearerToken(req.Header().Get("Authorization"))
	if raw == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing bearer token"))
	}
	return userBySession(ctx, pool, hashToken(raw))
}

func userBySession(ctx context.Context, pool *pgxpool.Pool, tokenHash string) (*authv1.User, error) {
	row := pool.QueryRow(ctx, `SELECT u.user_id, u.username, u.display_name, u.role, u.state,
	       u.created_at, u.updated_at, u.last_login_at
	FROM user_sessions s JOIN users u ON u.user_id = s.user_id
	WHERE s.token_hash=$1 AND s.expires_at > now() AND u.state='active'`, tokenHash)
	u, err := scanUser(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid or expired session"))
	}
	return u, err
}

func scanUser(row pgx.Row) (*authv1.User, error) {
	u := &authv1.User{}
	var role, state string
	var createdAt, updatedAt time.Time
	var lastLogin *time.Time
	if err := row.Scan(&u.UserId, &u.Username, &u.DisplayName, &role, &state,
		&createdAt, &updatedAt, &lastLogin); err != nil {
		return nil, err
	}
	var err error
	u.Role, err = roleEnum(role)
	if err != nil {
		return nil, err
	}
	u.State, err = stateEnum(state)
	if err != nil {
		return nil, err
	}
	u.CreatedAt = timestamppb.New(createdAt)
	u.UpdatedAt = timestamppb.New(updatedAt)
	if lastLogin != nil {
		u.LastLoginAt = timestamppb.New(*lastLogin)
	}
	return u, nil
}

func roleEnum(s string) (commonv1.UserRole, error) {
	switch s {
	case "admin":
		return commonv1.UserRole_USER_ROLE_ADMIN, nil
	case "operator":
		return commonv1.UserRole_USER_ROLE_OPERATOR, nil
	case "viewer":
		return commonv1.UserRole_USER_ROLE_VIEWER, nil
	default:
		return commonv1.UserRole_USER_ROLE_UNSPECIFIED, fmt.Errorf("unknown role %q", s)
	}
}

func roleString(r commonv1.UserRole) (string, error) {
	switch r {
	case commonv1.UserRole_USER_ROLE_ADMIN:
		return "admin", nil
	case commonv1.UserRole_USER_ROLE_OPERATOR:
		return "operator", nil
	case commonv1.UserRole_USER_ROLE_VIEWER:
		return "viewer", nil
	default:
		return "", fmt.Errorf("unknown role %s", r)
	}
}
