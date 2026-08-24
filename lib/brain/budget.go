package brain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"yufeng/lib/kernel"
)

// consumeBudget 按持久预算账户扣一次预算。超限 resource_exhausted。
func consumeBudget(ctx context.Context, pool *pgxpool.Pool, budgetID, subject, azp string, maxCalls int64) (int64, error) {
	if maxCalls <= 0 {
		return 0, nil
	}
	tag, err := pool.Exec(ctx, `INSERT INTO capability_budget(jti, budget_id, subject, azp, max_calls, calls_used, expires_at)
		VALUES($1,$1,$2,$3,$4,1,now()+interval '24 hours')
		ON CONFLICT (budget_id) DO UPDATE SET calls_used = capability_budget.calls_used + 1
		WHERE capability_budget.revoked = false AND capability_budget.calls_used < capability_budget.max_calls`,
		budgetID, subject, azp, maxCalls)
	if err != nil {
		return 0, err
	}
	if tag.RowsAffected() != 1 {
		return 0, connect.NewError(connect.CodeResourceExhausted, errors.New("capability token budget exhausted or revoked"))
	}
	var used, max int64
	if err := pool.QueryRow(ctx, `SELECT calls_used, max_calls FROM capability_budget WHERE budget_id=$1`, budgetID).Scan(&used, &max); err != nil {
		return 0, err
	}
	remain := max - used
	if remain < 0 {
		remain = 0
	}
	return remain, nil
}

func seedCapabilityBudget(ctx context.Context, db dbTX, budgetID, subject, azp string, maxCalls int64, expires time.Time) error {
	if strings.TrimSpace(budgetID) == "" || maxCalls <= 0 {
		return nil
	}
	_, err := db.Exec(ctx, `INSERT INTO capability_budget(jti, budget_id, subject, azp, max_calls, calls_used, expires_at)
		VALUES($1,$1,$2,$3,$4,0,$5) ON CONFLICT (budget_id) DO NOTHING`, budgetID, subject, azp, maxCalls, expires)
	return err
}

func registerCapabilityToken(ctx context.Context, db dbTX, jti, budgetID, leaseID string, leaseEpoch int64, expires time.Time) error {
	if strings.TrimSpace(jti) == "" || strings.TrimSpace(budgetID) == "" {
		return errors.New("capability token identity is required")
	}
	_, err := db.Exec(ctx, `INSERT INTO capability_token_instances(jti, budget_id, lease_id, lease_epoch, expires_at)
		VALUES($1,$2,$3,$4,$5)`, jti, budgetID, leaseID, leaseEpoch, expires)
	return err
}

func abortIdempotency(ctx context.Context, pool *pgxpool.Pool, scope, key string) error {
	if key == "" {
		return nil
	}
	_, err := pool.Exec(ctx, `DELETE FROM idempotency_keys WHERE scope=$1 AND idem_key=$2 AND status_code='pending'`, scope, key)
	return err
}

const idempotencyKeyMaxLen = 128

func requestDigest(tool, args, idem string) string {
	sum := sha256.Sum256([]byte(tool + "\n" + args + "\n" + idem))
	return hex.EncodeToString(sum[:])
}

func loadIdempotency(ctx context.Context, pool *pgxpool.Pool, scope, key, digest string) (hit bool, status, body string, err error) {
	hit, status, body, _, err = loadIdempotencyRow(ctx, pool, scope, key, digest)
	return hit, status, body, err
}

func loadIdempotencyRow(ctx context.Context, pool *pgxpool.Pool, scope, key, digest string) (hit bool, status, body string, created time.Time, err error) {
	if key == "" {
		return false, "", "", time.Time{}, nil
	}
	var storedDigest, storedStatus, storedBody string
	err = pool.QueryRow(ctx, `SELECT request_digest, status_code, response_json::text, created_at FROM idempotency_keys WHERE scope=$1 AND idem_key=$2`, scope, key).
		Scan(&storedDigest, &storedStatus, &storedBody, &created)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, "", "", time.Time{}, nil
	}
	if err != nil {
		return false, "", "", time.Time{}, err
	}
	if storedDigest != digest {
		return false, "", "", time.Time{}, connect.NewError(connect.CodeFailedPrecondition, errors.New("idempotency key reused with different request"))
	}
	return true, storedStatus, storedBody, created, nil
}

// reserveIdempotency 在副作用前占住幂等键。同键同摘要已完成则返回首次结果；
// 同键不同摘要冲突；并发同键未完成则 aborted，避免双写。
func reserveIdempotency(ctx context.Context, pool *pgxpool.Pool, scope, key, digest string) (hit bool, status, body string, err error) {
	if key == "" {
		return false, "", "", nil
	}
	if len(key) > idempotencyKeyMaxLen {
		return false, "", "", connect.NewError(connect.CodeInvalidArgument, errors.New("idempotency-key exceeds 128 characters"))
	}
	tag, err := pool.Exec(ctx, `INSERT INTO idempotency_keys(scope, idem_key, request_digest, status_code, response_json, created_at)
		VALUES($1,$2,$3,'pending','{}',now()) ON CONFLICT DO NOTHING`, scope, key, digest)
	if err != nil {
		return false, "", "", err
	}
	if tag.RowsAffected() == 1 {
		return false, "", "", nil
	}
	hit, status, body, created, err := loadIdempotencyRow(ctx, pool, scope, key, digest)
	if err != nil {
		return false, "", "", err
	}
	if !hit {
		return false, "", "", connect.NewError(connect.CodeAborted, errors.New("idempotency key in flight"))
	}
	if status == "pending" || status == "" {
		if !created.IsZero() && time.Since(created) > kernel.IdempotencyPendingTTL {
			taken, takeErr := takeoverExpiredPending(ctx, pool, scope, key, digest, created)
			if takeErr != nil {
				return false, "", "", takeErr
			}
			if taken {
				return false, "", "", nil
			}
		}
		return false, "", "", connect.NewError(connect.CodeAborted, errors.New("idempotency key in flight"))
	}
	return true, status, body, nil
}

func takeoverExpiredPending(ctx context.Context, pool *pgxpool.Pool, scope, key, digest string, created time.Time) (bool, error) {
	tag, err := pool.Exec(ctx, `DELETE FROM idempotency_keys
		WHERE scope=$1 AND idem_key=$2 AND status_code='pending' AND created_at=$3`, scope, key, created)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() != 1 {
		return false, nil
	}
	tag, err = pool.Exec(ctx, `INSERT INTO idempotency_keys(scope, idem_key, request_digest, status_code, response_json, created_at)
		VALUES($1,$2,$3,'pending','{}',now()) ON CONFLICT DO NOTHING`, scope, key, digest)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func requireIdempotencyKey(header interface{ Get(string) string }) (string, error) {
	if header == nil {
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("idempotency-key is required"))
	}
	key := strings.TrimSpace(header.Get("Idempotency-Key"))
	if key == "" {
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("idempotency-key is required"))
	}
	if len(key) > idempotencyKeyMaxLen {
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("idempotency-key exceeds 128 characters"))
	}
	return key, nil
}

func storeIdempotency(ctx context.Context, pool *pgxpool.Pool, scope, key, digest, status, body string) error {
	return storeIdempotencyDB(ctx, pool, scope, key, digest, status, body)
}

func storeIdempotencyDB(ctx context.Context, db dbTX, scope, key, digest, status, body string) error {
	if key == "" {
		return nil
	}
	raw, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		return err
	}
	tag, err := db.Exec(ctx, `UPDATE idempotency_keys SET request_digest=$3, status_code=$4, response_json=$5::jsonb
		WHERE scope=$1 AND idem_key=$2`, scope, key, digest, status, raw)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	_, err = db.Exec(ctx, `INSERT INTO idempotency_keys(scope, idem_key, request_digest, status_code, response_json, created_at)
		VALUES($1,$2,$3,$4,$5::jsonb,now()) ON CONFLICT DO NOTHING`, scope, key, digest, status, raw)
	return err
}

func unwrapIdemBody(stored string) string {
	var wrap struct {
		Body string `json:"body"`
	}
	if json.Unmarshal([]byte(stored), &wrap) == nil && wrap.Body != "" {
		return wrap.Body
	}
	return stored
}
