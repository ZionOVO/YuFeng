package brain

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"yufeng/lib/kernel"
)

// EnsureBootstrapAdmin 按用户名幂等创建初始管理员。
func EnsureBootstrapAdmin(ctx context.Context, pool *pgxpool.Pool, username, password string) error {
	if username == "" {
		return errors.New("bootstrap admin username and password are required")
	}
	if err := kernel.RejectDefaultPassword(password); err != nil {
		return err
	}
	if len(password) < MinPasswordLength {
		return fmt.Errorf("bootstrap admin password must be at least %d characters", MinPasswordLength)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	id, err := newID("usr")
	if err != nil {
		return err
	}
	// ON CONFLICT DO NOTHING 而不是先查后插：多实例并发引导时
	// count-then-insert 会撞唯一约束把第二个实例打挂。
	tag, err := pool.Exec(ctx, `INSERT INTO users(user_id, username, display_name, role, state, password_hash)
	VALUES($1,$2,$2,'admin','active',$3) ON CONFLICT(username) DO NOTHING`, id, username, string(hash))
	if err != nil {
		return fmt.Errorf("create bootstrap admin: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// 已存在：不覆盖既有密码（重启不能重置管理员凭据）。
		return nil
	}
	return nil
}

// bootstrapJarvisPlaceholder 是预置贾维斯行的占位公钥与 refresh 哈希。
// RegisterAgent 只允许把该占位行认领成真实身份，禁止覆盖已登记智能代理。
const bootstrapJarvisPlaceholder = "bootstrap"

// EnsureBootstrapJarvis 为研判入队预置贾维斯行。已有公钥不覆盖，避免劫持已注册智能代理。
func EnsureBootstrapJarvis(ctx context.Context, pool *pgxpool.Pool, agentID string) error {
	if strings.TrimSpace(agentID) == "" {
		return errors.New("jarvis agent id is required")
	}
	_, err := pool.Exec(ctx, `INSERT INTO agents(agent_id, refresh_token_hash, role, public_key)
		VALUES($1,$2,'orchestrator',$2)
		ON CONFLICT(agent_id) DO UPDATE SET public_key = CASE
			WHEN agents.public_key IS NULL OR agents.public_key = '' THEN EXCLUDED.public_key
			ELSE agents.public_key
		END`, agentID, bootstrapJarvisPlaceholder)
	if err != nil {
		return fmt.Errorf("bootstrap jarvis: %w", err)
	}
	return nil
}
