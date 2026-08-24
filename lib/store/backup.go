package store

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"yufeng/lib/kernel"
)

// LedgerSnapshot 是三本账可移植快照，供全新库恢复演练。
type LedgerSnapshot struct {
	Events   []map[string]any `json:"events"`
	Releases []map[string]any `json:"releases"`
}

// DumpLedger 导出事件账与发布账的可恢复副本。
func DumpLedger(ctx context.Context, pool *pgxpool.Pool) (*LedgerSnapshot, error) {
	if pool == nil {
		return nil, fmt.Errorf("store: pool is nil")
	}
	snap := &LedgerSnapshot{}
	erows, err := pool.Query(ctx, `SELECT event_id, asset_id, kind, verdict, payload,occurred_at,payload_digest FROM events`)
	if err != nil {
		return nil, err
	}
	defer erows.Close()
	for erows.Next() {
		var id, asset, kind, verdict, payloadDigest string
		var payload []byte
		var occurredAt any
		if err := erows.Scan(&id, &asset, &kind, &verdict, &payload, &occurredAt, &payloadDigest); err != nil {
			return nil, err
		}
		snap.Events = append(snap.Events, map[string]any{
			"event_id": id, "asset_id": asset, "kind": kind, "verdict": verdict, "payload": string(payload),
			"occurred_at": occurredAt, "payload_digest": payloadDigest,
		})
	}
	if err := erows.Err(); err != nil {
		return nil, err
	}
	rrows, err := pool.Query(ctx, `SELECT release_id, state, artifact, ttl_seconds FROM releases`)
	if err != nil {
		return nil, err
	}
	defer rrows.Close()
	for rrows.Next() {
		var id, state string
		var artifact []byte
		var ttl int32
		if err := rrows.Scan(&id, &state, &artifact, &ttl); err != nil {
			return nil, err
		}
		snap.Releases = append(snap.Releases, map[string]any{
			"release_id": id, "state": state, "artifact": string(artifact), "ttl_seconds": ttl,
		})
	}
	return snap, rrows.Err()
}

// RestoreLedger 把快照写入目标库（调用方须已完成迁移）。
func RestoreLedger(ctx context.Context, pool *pgxpool.Pool, snap *LedgerSnapshot) (err error) {
	if pool == nil || snap == nil {
		return fmt.Errorf("store: restore requires pool and snapshot")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if rollbackErr := tx.Rollback(ctx); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			err = errors.Join(err, rollbackErr)
		}
	}()
	for _, e := range snap.Events {
		if _, err := tx.Exec(ctx, `INSERT INTO event_receipts(event_id,payload_digest,occurred_at)
			VALUES($1,$2,$3) ON CONFLICT(event_id) DO UPDATE SET
				payload_digest=EXCLUDED.payload_digest,occurred_at=EXCLUDED.occurred_at`,
			e["event_id"], e["payload_digest"], e["occurred_at"]); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO events(event_id, occurred_at, asset_id, kind, verdict, payload)
			VALUES($1,$2,$3,$4,$5,$6::jsonb) ON CONFLICT (event_id,occurred_at) DO NOTHING`,
			e["event_id"], e["occurred_at"], e["asset_id"], e["kind"], e["verdict"], e["payload"]); err != nil {
			return err
		}
	}
	for _, r := range snap.Releases {
		ttlN := snapshotTTL(r["ttl_seconds"])
		if _, err := tx.Exec(ctx, `INSERT INTO releases(release_id, state, artifact, ttl_seconds)
			VALUES($1,$2,$3::jsonb,$4) ON CONFLICT (release_id) DO NOTHING`,
			r["release_id"], r["state"], r["artifact"], ttlN); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// snapshotTTL 把快照中的生存时长转换成平台硬过期范围内的正整数，并对无效值应用默认值。
func snapshotTTL(v any) int32 {
	defaultTTL := int32(kernel.TTLDefault.Seconds())
	maxTTL := int64(kernel.TTLMax.Seconds())
	var seconds int64
	switch n := v.(type) {
	case int32:
		seconds = int64(n)
	case int:
		seconds = int64(n)
	case int64:
		seconds = n
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) || math.Trunc(n) != n || n < 1 || n > float64(maxTTL) {
			return defaultTTL
		}
		seconds = int64(n)
	default:
		return defaultTTL
	}
	if seconds < 1 || seconds > maxTTL {
		return defaultTTL
	}
	return int32(seconds)
}
