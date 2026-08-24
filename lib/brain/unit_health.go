package brain

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// MarkStaleUnitsDegraded 把连续三个心跳周期未上报的执行单元标为降级。
func MarkStaleUnitsDegraded(ctx context.Context, pool *pgxpool.Pool, heartbeat time.Duration) error {
	if heartbeat <= 0 {
		heartbeat = heartbeatIntervalSeconds * time.Second
	}
	_, err := pool.Exec(ctx, `UPDATE units SET health='degraded',updated_at=now()
		WHERE last_heartbeat_at IS NOT NULL AND last_heartbeat_at < $1 AND health<>'degraded'`, time.Now().Add(-3*heartbeat))
	return err
}

// StartUnitHealthMonitor 周期检查单元失联；下一次合法心跳会重新计算健康状态。
func StartUnitHealthMonitor(ctx context.Context, pool *pgxpool.Pool, heartbeat time.Duration) {
	if heartbeat <= 0 {
		heartbeat = heartbeatIntervalSeconds * time.Second
	}
	go func() {
		ticker := time.NewTicker(heartbeat)
		defer ticker.Stop()
		_ = MarkStaleUnitsDegraded(ctx, pool, heartbeat)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = MarkStaleUnitsDegraded(ctx, pool, heartbeat)
			}
		}
	}()
}
