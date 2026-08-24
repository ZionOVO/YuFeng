package brain

import (
	"context"
	"crypto/ed25519"
	"errors"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/encoding/protojson"

	artifactv1 "yufeng/proto/gen/artifactv1"
	commonv1 "yufeng/proto/gen/commonv1"

	"yufeng/lib/kernel"
	"yufeng/lib/observability"
)

// ProductionScheduler 返回架构预算冻结的生产默认门槛。
func ProductionScheduler(interval time.Duration) SchedulerConfig {
	if interval <= 0 {
		interval = kernel.GuardWindow
	}
	return SchedulerConfig{
		Interval:          interval,
		DenyThreshold:     kernel.DenyFeedbackBlockThreshold,
		GuardBadWindows:   kernel.GuardBadWindows,
		ShadowMinDuration: kernel.ShadowMinDuration,
		ShadowMinRequests: kernel.ShadowMinRequests,
		CanaryMinDuration: kernel.CanaryMinDuration,
		CanaryMinRequests: kernel.CanaryMinRequests,
		CanaryPercent:     kernel.CanaryPercentDefault,
	}
}

// SchedulerConfig 是自动退休、自动晋升与守护窗口配置。
// 演示环境把时长与请求门槛显式设为 0；生产使用架构预算的默认值。
type SchedulerConfig struct {
	Interval          time.Duration
	DenyThreshold     uint64
	GuardBadWindows   int
	ShadowMinDuration time.Duration
	ShadowMinRequests uint64
	CanaryMinDuration time.Duration
	CanaryMinRequests uint64
	CanaryPercent     int32
	DemoTriage        bool
	SigningKey        ed25519.PrivateKey
	ArtifactSigner    kernel.Signer
}

// badWindowBlockRateJump 是坏窗口判定的拦截率跳变阈值：一个窗口内增量
// 拦截率超过一半，通常意味着规则误杀大面积发生，需要人工介入或自动回滚。
const badWindowBlockRateJump = 0.5

// StartScheduler 启动中台后台调度器：存活时限到期自动退休，守护窗口越界自动回滚。
func StartScheduler(ctx context.Context, pool *pgxpool.Pool, cfg SchedulerConfig) {
	if cfg.Interval <= 0 {
		cfg.Interval = 30 * time.Second
	}
	if cfg.DenyThreshold == 0 {
		cfg.DenyThreshold = 1
	}
	if cfg.GuardBadWindows == 0 {
		cfg.GuardBadWindows = 2
	}
	if cfg.CanaryPercent == 0 {
		cfg.CanaryPercent = 5
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(cfg.Interval):
			}
			if n, err := autoPromoteReleases(ctx, pool, cfg); err != nil {
				log.Printf("自动晋升失败: %v", err)
			} else if n > 0 {
				log.Printf("自动晋升 %d 条发布", n)
			}
			if n, err := guardReleases(ctx, pool, cfg); err != nil {
				log.Printf("守护窗口检查失败: %v", err)
			} else if n > 0 {
				log.Printf("守护窗口自动回滚 %d 条发布", n)
			}
			if n, err := expireReleases(ctx, pool, cfg); err != nil {
				log.Printf("硬过期退休失败: %v", err)
			} else if n > 0 {
				log.Printf("硬过期退休 %d 条发布", n)
			}
			if n, err := enqueueDueReviews(ctx, pool); err != nil {
				log.Printf("复核入队失败: %v", err)
			} else if n > 0 {
				log.Printf("复核入队 %d 条", n)
			}
			if n, err := pauseExpiredRunBudgetLeases(ctx, pool, time.Now()); err != nil {
				log.Printf("run 失租预算暂停失败: %v", err)
			} else if n > 0 {
				log.Printf("run 失租预算暂停 %d 条", n)
			}
			if n, err := expireDueRuns(ctx, pool, time.Now()); err != nil {
				log.Printf("run 墙钟到期收口失败: %v", err)
			} else if n > 0 {
				log.Printf("run 墙钟到期收口 %d 条", n)
			}
		}
	}()
}

func autoPromoteReleases(ctx context.Context, pool *pgxpool.Pool, cfg SchedulerConfig) (int, error) {
	n := 0
	shadows, err := listReleaseIDs(ctx, pool, "shadow")
	if err != nil {
		return 0, err
	}
	for _, id := range shadows {
		if skip, err := skipAutoPromote(ctx, pool, id, cfg.DemoTriage); err != nil || skip {
			continue
		}
		if !cfg.DemoTriage {
			units, err := releaseBoundUnitCount(ctx, pool, id)
			if err != nil || units < kernel.CanaryMinUnits(cfg.CanaryPercent) {
				continue
			}
		}
		ok, err := promotionGatesMet(ctx, pool, id, "shadow", cfg.ShadowMinDuration, cfg.ShadowMinRequests)
		if err != nil {
			log.Printf("自动晋升门槛 %s 失败: %v", id, err)
			continue
		}
		if !ok {
			continue
		}
		rel, err := loadRelease(ctx, pool, id)
		if err != nil {
			continue
		}
		shadow, ok := rel.(*kernel.Shadow)
		if !ok {
			continue
		}
		canary, err := shadow.PromoteCanary(cfg.CanaryPercent)
		if err != nil {
			log.Printf("自动 canary %s 失败: %v", id, err)
			continue
		}
		if err := commitReleaseChange(ctx, pool, releaseWrite{rel: canary, feed: true, key: cfg.SigningKey, signer: cfg.ArtifactSigner}); err != nil {
			log.Printf("自动 canary 落盘 %s 失败: %v", id, err)
			continue
		}
		n++
	}
	canaries, err := listReleaseIDs(ctx, pool, "canary")
	if err != nil {
		return n, err
	}
	for _, id := range canaries {
		if skip, err := skipAutoPromote(ctx, pool, id, cfg.DemoTriage); err != nil || skip {
			continue
		}
		ok, err := promotionGatesMet(ctx, pool, id, "canary", cfg.CanaryMinDuration, cfg.CanaryMinRequests)
		if err != nil {
			log.Printf("自动晋升门槛 %s 失败: %v", id, err)
			continue
		}
		if !ok {
			continue
		}
		rel, err := loadRelease(ctx, pool, id)
		if err != nil {
			continue
		}
		canary, ok := rel.(*kernel.Canary)
		if !ok {
			continue
		}
		enforce := canary.PromoteEnforce()
		if err := commitReleaseChange(ctx, pool, releaseWrite{rel: enforce, feed: true, key: cfg.SigningKey, signer: cfg.ArtifactSigner}); err != nil {
			log.Printf("自动 enforce 落盘 %s 失败: %v", id, err)
			continue
		}
		n++
	}
	return n, nil
}

func skipAutoPromote(ctx context.Context, pool *pgxpool.Pool, releaseID string, demo bool) (bool, error) {
	var scopeRisk, evidence, kind string
	var raw []byte
	err := pool.QueryRow(ctx, `SELECT COALESCE(scope_risk,''), COALESCE(evidence_class,''), COALESCE(artifact->>'kind',''), artifact
		FROM releases WHERE release_id=$1`, releaseID).Scan(&scopeRisk, &evidence, &kind, &raw)
	if err != nil {
		return true, err
	}
	if kind == "KIND_SHAPE" {
		return true, nil
	}
	if kind == "KIND_RULE" {
		return !demo, nil
	}
	if demo && scopeRisk == "" && evidence == "" {
		return false, nil
	}
	if autoPromoteBlocked(scopeRisk, evidence) {
		return true, nil
	}
	if !demo && !replayCoverageOK(raw) {
		return true, nil
	}
	return false, nil
}

func replayCoverageOK(raw []byte) bool {
	var a artifactv1.Artifact
	if err := protojson.Unmarshal(raw, &a); err != nil {
		return false
	}
	return replayReportPassed(&a)
}

func autoPromoteBlocked(scopeRisk, evidence string) bool {
	if (scopeRisk == "exact" || scopeRisk == "route") && (evidence == "crs_mapped" || evidence == "human" || evidence == "replay") {
		return false
	}
	return true
}

func listReleaseIDs(ctx context.Context, pool *pgxpool.Pool, state string) ([]string, error) {
	rows, err := pool.Query(ctx, `SELECT release_id FROM releases WHERE state=$1`, state)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func promotionGatesMet(ctx context.Context, pool *pgxpool.Pool, releaseID, mode string, minAge time.Duration, minRequests uint64) (bool, error) {
	col := "shadow_started_at"
	if mode == "canary" {
		col = "canary_started_at"
	}
	started, err := releaseTime(ctx, pool, releaseID, col)
	if err != nil {
		return false, err
	}
	if !durationThresholdMet(time.Since(started), minAge) {
		return false, nil
	}
	exclude, err := proposerUnitIDs(ctx, pool, releaseID)
	if err != nil {
		return false, err
	}
	requests, err := sumCountersExcept(ctx, pool, releaseID, mode, exclude)
	if err != nil {
		return false, err
	}
	return requests >= minRequests, nil
}

func durationThresholdMet(elapsed, required time.Duration) bool {
	return required <= 0 || elapsed >= required
}

func proposerUnitIDs(ctx context.Context, pool *pgxpool.Pool, releaseID string) ([]string, error) {
	var createdBy string
	if err := pool.QueryRow(ctx, `SELECT created_by FROM releases WHERE release_id=$1`, releaseID).Scan(&createdBy); err != nil {
		return nil, err
	}
	if createdBy == "" {
		return nil, nil
	}
	rows, err := pool.Query(ctx, `SELECT unit_id FROM units WHERE unit_id=$1
		UNION
		SELECT unit_id FROM unit_assets ua JOIN assets a ON a.asset_id=ua.asset_id WHERE a.asset_id=$1`, createdBy)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func sumCountersExcept(ctx context.Context, pool *pgxpool.Pool, releaseID, mode string, exclude []string) (uint64, error) {
	if exclude == nil {
		exclude = []string{}
	}
	var sum int64
	err := pool.QueryRow(ctx, `SELECT COALESCE(SUM(requests_total),0) FROM release_counters
		WHERE release_id=$1 AND mode=$2 AND NOT (unit_id = ANY($3))`, releaseID, mode, exclude).Scan(&sum)
	return uint64(sum), err
}

func enqueueDueReviews(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	rows, err := pool.Query(ctx, `SELECT release_id FROM releases
		WHERE review_at IS NOT NULL AND review_at < now()
		  AND state IN ('shadow','canary','enforce')`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return n, err
		}
		if err := writeOutbox(ctx, pool, "yufeng.review.due", "review:"+id, map[string]string{"release_id": id}); err != nil {
			return n, err
		}
		n++
	}
	return n, rows.Err()
}

func expireReleases(ctx context.Context, pool *pgxpool.Pool, cfg SchedulerConfig) (int, error) {
	rows, err := pool.Query(ctx, `SELECT release_id FROM releases
	WHERE state IN ('shadow','canary','enforce')
	  AND hard_expires_at IS NOT NULL AND hard_expires_at < now()`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return 0, err
		}
		ids = append(ids, id)
	}
	if rows.Err() != nil {
		return 0, rows.Err()
	}
	for _, id := range ids {
		if _, err := retireRelease(ctx, pool, id, commonv1.RetireReason_RETIRE_REASON_TTL, cfg.SigningKey, cfg.ArtifactSigner); err != nil {
			log.Printf("退休发布 %s 失败: %v", id, err)
		}
	}
	return len(ids), nil
}

func guardReleases(ctx context.Context, pool *pgxpool.Pool, cfg SchedulerConfig) (int, error) {
	denyThreshold := cfg.DenyThreshold
	badWindows := cfg.GuardBadWindows
	rows, err := pool.Query(ctx, `SELECT release_id, state FROM releases WHERE state IN ('canary','enforce')`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	type item struct{ id, state string }
	var releases []item
	for rows.Next() {
		var r item
		if err := rows.Scan(&r.id, &r.state); err != nil {
			return 0, err
		}
		releases = append(releases, r)
	}
	if rows.Err() != nil {
		return 0, rows.Err()
	}
	rolled := 0
	for _, r := range releases {
		var requests, blocks int64
		if err := pool.QueryRow(ctx, `SELECT COALESCE(SUM(requests_total),0), COALESCE(SUM(blocks_total),0)
		FROM release_counters WHERE release_id=$1 AND mode=$2`, r.id, r.state).Scan(&requests, &blocks); err != nil {
			// 单条查询失败只跳过该发布，窗口快照留待下个周期——不能让一条坏行停掉全部守护。
			log.Printf("守护窗口读取 %s 计数失败（本周期跳过）: %v", r.id, err)
			continue
		}
		var denies int64
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM deny_feedback WHERE release_id=$1`, r.id).Scan(&denies); err != nil {
			log.Printf("守护窗口读取 %s 举报失败（本周期跳过）: %v", r.id, err)
			continue
		}
		var prevRequests, prevBlocks, prevDenies, prevP99 int64
		var consecutive int
		if err := pool.QueryRow(ctx, `SELECT requests_total, blocks_total, deny_total, consecutive_bad, COALESCE(last_p99_micros,0)
		FROM release_guards WHERE release_id=$1`, r.id).Scan(&prevRequests, &prevBlocks, &prevDenies, &consecutive, &prevP99); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			log.Printf("守护窗口读取 %s 快照失败（按无快照处理）: %v", r.id, err)
		}
		var up5xx, latSum, latN, p99 int64
		_ = pool.QueryRow(ctx, `SELECT COALESCE(SUM(upstream_5xx_total),0), COALESCE(SUM(latency_micros_total),0), COALESCE(SUM(latency_samples),0), COALESCE(MAX(latency_p99_micros),0)
			FROM release_counters WHERE release_id=$1 AND mode=$2`, r.id, r.state).Scan(&up5xx, &latSum, &latN, &p99)
		bad, reasons := GuardWindowBad(
			GuardSnapshot{Requests: prevRequests, Blocks: prevBlocks, Denies: prevDenies, P99Micros: prevP99},
			GuardSnapshot{Requests: requests, Blocks: blocks, Denies: denies, Upstream5xx: up5xx, LatencyMicros: latSum, LatencySamples: latN, P99Micros: p99},
			denyThreshold,
		)
		if bad {
			consecutive++
		} else {
			consecutive = 0
		}
		// 快照丢失会让 consecutive_bad 从零重数，回滚变慢但绝不误触发——保守方向。
		if _, err := pool.Exec(ctx, `INSERT INTO release_guards(release_id, requests_total, blocks_total, deny_total, consecutive_bad, last_bad_at, last_bad_reasons, last_p99_micros, updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,now())
		ON CONFLICT(release_id) DO UPDATE SET requests_total=EXCLUDED.requests_total, blocks_total=EXCLUDED.blocks_total,
		  deny_total=EXCLUDED.deny_total, consecutive_bad=EXCLUDED.consecutive_bad,
		  last_bad_at=EXCLUDED.last_bad_at, last_bad_reasons=EXCLUDED.last_bad_reasons,
		  last_p99_micros=EXCLUDED.last_p99_micros, updated_at=now()`,
			r.id, requests, blocks, denies, consecutive, nullableTime(bad), reasons, p99); err != nil {
			log.Printf("守护窗口写入 %s 快照失败: %v", r.id, err)
		}
		if consecutive >= badWindows {
			if _, err := retireRelease(ctx, pool, r.id, commonv1.RetireReason_RETIRE_REASON_ROLLBACK, cfg.SigningKey, cfg.ArtifactSigner); err == nil {
				rolled++
				observability.Default().Add(observability.MetricAutoRollback, 1)
			} else {
				log.Printf("守护窗口回滚 %s 失败: %v", r.id, err)
			}
		}
	}
	return rolled, nil
}

func nullableTime(bad bool) *time.Time {
	if !bad {
		return nil
	}
	t := time.Now().UTC()
	return &t
}

func retireRelease(ctx context.Context, pool *pgxpool.Pool, releaseID string, reason commonv1.RetireReason, key ed25519.PrivateKey, signer kernel.Signer) (*kernel.Retired, error) {
	return retireReleaseAudit(ctx, pool, releaseID, reason, key, signer, "system", "scheduler", "release.retire", map[string]any{"reason": reason.String()}, nil)
}

func retireReleaseAudit(ctx context.Context, pool *pgxpool.Pool, releaseID string, reason commonv1.RetireReason, key ed25519.PrivateKey, signer kernel.Signer, actorType, actorID, action string, details map[string]any, complete func(pgx.Tx, *kernel.Retired) error) (*kernel.Retired, error) {
	rel, err := loadRelease(ctx, pool, releaseID)
	if err != nil {
		return nil, err
	}
	active, err := kernel.ActiveOf(rel)
	if err != nil {
		return nil, err
	}
	retired, err := kernel.RetireActive(active, reason)
	if err != nil {
		return nil, err
	}
	if err := commitReleaseChange(ctx, pool, releaseWrite{rel: retired, feed: true, retired: true, reason: reason, key: key, signer: signer,
		actorType: actorType, actorID: actorID, action: action, details: details, complete: func(tx pgx.Tx) error {
			if complete == nil {
				return nil
			}
			return complete(tx, retired)
		}}); err != nil {
		return nil, err
	}
	return retired, nil
}
