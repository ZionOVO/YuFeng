package brain

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"yufeng/lib/eventbus"
	"yufeng/lib/observability"
)

const outboxTick = 200 * time.Millisecond

func writeOutbox(ctx context.Context, db dbTX, topic, dedupe string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, `INSERT INTO outbox(topic, dedupe_key, payload) VALUES($1,$2,$3::jsonb) ON CONFLICT DO NOTHING`, topic, dedupe, raw)
	return err
}

func refreshQueueBacklog(ctx context.Context, pool *pgxpool.Pool) {
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE published_at IS NULL`).Scan(&n); err != nil {
		return
	}
	observability.Default().Set(observability.MetricQueueBacklog, float64(n))
}

// DeliverOutbox 把未发布的发件箱行写入 NATS 持久流，成功后标记 published_at。
func DeliverOutbox(ctx context.Context, pool *pgxpool.Pool, bus *eventbus.Bus) (int, error) {
	refreshQueueBacklog(ctx, pool)
	if bus == nil {
		return 0, nil
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `SELECT outbox_id, topic, dedupe_key, payload FROM outbox
		WHERE published_at IS NULL AND topic LIKE 'yufeng.%' ORDER BY outbox_id LIMIT 100 FOR UPDATE SKIP LOCKED`)
	if err != nil {
		return 0, err
	}
	type item struct {
		id     int64
		topic  string
		dedupe string
		body   []byte
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.topic, &it.dedupe, &it.body); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	n := 0
	var lastPub error
	for _, it := range items {
		if err := bus.PublishDurable(it.topic, it.dedupe, it.body); err != nil {
			lastPub = err
			if _, uerr := tx.Exec(ctx, `UPDATE outbox SET attempts=attempts+1 WHERE outbox_id=$1`, it.id); uerr != nil {
				return n, uerr
			}
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE outbox SET published_at=now() WHERE outbox_id=$1`, it.id); err != nil {
			return n, err
		}
		n++
	}
	if err := tx.Commit(ctx); err != nil {
		return n, err
	}
	refreshQueueBacklog(ctx, pool)
	if n == 0 && lastPub != nil {
		return 0, lastPub
	}
	return n, nil
}

// StartOutboxLoop 周期投递未确认发件箱。
func StartOutboxLoop(ctx context.Context, pool *pgxpool.Pool, bus *eventbus.Bus) {
	if bus == nil {
		return
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(outboxTick):
			}
			if _, err := DeliverOutbox(ctx, pool, bus); err != nil && ctx.Err() == nil {
				log.Printf("outbox deliver: %v", err)
			}
		}
	}()
}
