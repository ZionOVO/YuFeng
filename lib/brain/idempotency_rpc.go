package brain

import (
	"context"
	"encoding/json"
	"errors"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type persistedRPCError struct {
	err error
}

// Error 保留需要与幂等结果一同提交的远程调用错误文本。
func (e *persistedRPCError) Error() string { return e.err.Error() }

// Unwrap 暴露原始远程调用错误，供错误代码与类型判断使用。
func (e *persistedRPCError) Unwrap() error { return e.err }

func persistRPCError(err error) error {
	if err == nil {
		return nil
	}
	return &persistedRPCError{err: err}
}

type idempotentErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func idempotencyKey(h interface{ Get(string) string }) string {
	if h == nil {
		return ""
	}
	return h.Get("Idempotency-Key")
}

// idempotentProto 把业务写与首次响应放进同一数据库事务。
// 需要持久化失败状态的调用可返回 persistRPCError，使状态与错误结果一同提交。
func idempotentProto(ctx context.Context, pool *pgxpool.Pool, scope, key string, req, resp proto.Message, run func(pgx.Tx) error) error {
	key, digest, execute, err := beginIdempotentProto(ctx, pool, scope, key, req, resp)
	if err != nil {
		return err
	}
	if !execute {
		return nil
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		_ = abortIdempotency(ctx, pool, scope, key)
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := run(tx); err != nil {
		var saved *persistedRPCError
		if !errors.As(err, &saved) {
			_ = tx.Rollback(ctx)
			if abortErr := abortIdempotency(ctx, pool, scope, key); abortErr != nil {
				return abortErr
			}
			return err
		}
		body, marshalErr := json.Marshal(idempotentErrorBody{Code: connect.CodeOf(saved.err).String(), Message: saved.err.Error()})
		if marshalErr != nil {
			_ = tx.Rollback(ctx)
			_ = abortIdempotency(ctx, pool, scope, key)
			return marshalErr
		}
		if err := storeIdempotencyDB(ctx, tx, scope, key, digest, "error", string(body)); err != nil {
			_ = tx.Rollback(ctx)
			_ = abortIdempotency(ctx, pool, scope, key)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			_ = abortIdempotency(ctx, pool, scope, key)
			return err
		}
		return saved.err
	}
	out, err := protojson.Marshal(resp)
	if err != nil {
		_ = tx.Rollback(ctx)
		_ = abortIdempotency(ctx, pool, scope, key)
		return err
	}
	if err := storeIdempotencyDB(ctx, tx, scope, key, digest, "ok", string(out)); err != nil {
		_ = tx.Rollback(ctx)
		_ = abortIdempotency(ctx, pool, scope, key)
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		_ = abortIdempotency(ctx, pool, scope, key)
		return err
	}
	return nil
}

func beginIdempotentProto(ctx context.Context, pool *pgxpool.Pool, scope, key string, req, resp proto.Message) (string, string, bool, error) {
	key, err := requireIdempotencyKey(headerValue(key))
	if err != nil {
		return "", "", false, err
	}
	raw, err := protojson.Marshal(req)
	if err != nil {
		return "", "", false, err
	}
	digest := requestDigest(scope, string(raw), key)
	hit, status, body, err := reserveIdempotency(ctx, pool, scope, key, digest)
	if err != nil {
		return "", "", false, err
	}
	if !hit {
		return key, digest, true, nil
	}
	body = unwrapIdemBody(body)
	if status == "error" {
		var saved idempotentErrorBody
		if err := json.Unmarshal([]byte(body), &saved); err != nil {
			return "", "", false, err
		}
		return "", "", false, connect.NewError(connectCode(saved.Code), errors.New(saved.Message))
	}
	if err := protojson.Unmarshal([]byte(body), resp); err != nil {
		return "", "", false, err
	}
	return key, digest, false, nil
}

type headerValue string

// Get 把已提取的幂等键适配为只读请求头接口。
func (h headerValue) Get(string) string { return string(h) }

func connectCode(raw string) connect.Code {
	for code := connect.CodeCanceled; code <= connect.CodeUnauthenticated; code++ {
		if code.String() == raw {
			return code
		}
	}
	return connect.CodeUnknown
}

func storeIdempotentProtoDB(ctx context.Context, db dbTX, scope, key, digest string, msg proto.Message) error {
	raw, err := protojson.Marshal(msg)
	if err != nil {
		return err
	}
	return storeIdempotencyDB(ctx, db, scope, key, digest, "ok", string(raw))
}

func storeIdempotentErrorDB(ctx context.Context, db dbTX, scope, key, digest string, callErr error) error {
	body, err := json.Marshal(idempotentErrorBody{Code: connect.CodeOf(callErr).String(), Message: callErr.Error()})
	if err != nil {
		return err
	}
	return storeIdempotencyDB(ctx, db, scope, key, digest, "error", string(body))
}
