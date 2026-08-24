package brain

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"yufeng/lib/kernel"
)

var trafficPartitionName = regexp.MustCompile(`^(traffic_windows|review_candidates)_\d{8}$`)

// TrafficMaintenanceResult 汇总一次幂等流量保留任务实际删除的行数。
type TrafficMaintenanceResult struct {
	TrafficWindows   int64
	ReviewCandidates int64
	Cases            int64
}

// StartTrafficMaintenance 启动独立于治理调度器的流量分区、汇总与保留任务。
func StartTrafficMaintenance(ctx context.Context, pool *pgxpool.Pool, agents *AgentServer, jarvisID string) {
	go func() {
		run := func() {
			if err := processPendingReviewCases(ctx, pool, agents, jarvisID); err != nil {
				log.Printf("流量案件 outbox 任务失败: %v", err)
			}
			if err := releaseSuppressedTrafficCases(ctx, pool); err != nil {
				log.Printf("流量案件自动调查额度恢复失败: %v", err)
			}
			if err := reconcilePendingCaseDelegations(ctx, pool); err != nil {
				log.Printf("流量案件 Agent 匹配任务失败: %v", err)
			}
			if err := processPendingCaseDelegations(ctx, pool, agents, jarvisID); err != nil {
				log.Printf("流量案件 Agent 委派任务失败: %v", err)
			}
		}
		run()
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				run()
			}
		}
	}()
	go func() {
		run := func() {
			if _, err := MaintainTrafficData(ctx, pool, time.Now()); err != nil {
				log.Printf("流量数据保留任务失败: %v", err)
			}
		}
		run()
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				run()
			}
		}
	}()
}

func releaseSuppressedTrafficCases(ctx context.Context, pool *pgxpool.Pool) error {
	return withTx(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('traffic-case-daily-quota'), 34)`); err != nil {
			return err
		}
		var globalToday int
		if err := tx.QueryRow(ctx, `SELECT count(DISTINCT case_id) FROM (
			SELECT case_id FROM investigation_cases
			WHERE module_id='traffic-interception' AND automation_suppressed_reason=''
			AND created_at>=date_trunc('day',now())
			UNION ALL
			SELECT case_id FROM case_activities WHERE ref_id LIKE 'automation-resumed:%'
			AND occurred_at>=date_trunc('day',now())
		) resumed`).Scan(&globalToday); err != nil {
			return err
		}
		remaining := kernel.TrafficReviewDailyCases - globalToday
		if remaining <= 0 {
			return nil
		}
		rows, err := tx.Query(ctx, `SELECT c.case_id,c.asset_id FROM investigation_cases c
			WHERE c.module_id='traffic-interception' AND c.state='open' AND c.assigned_agent_id=''
			AND c.automation_suppressed_reason='daily_investigation_quota'
			AND EXISTS (SELECT 1 FROM jsonb_array_elements(c.representatives) r
				WHERE COALESCE(r->>'evidence_handle','')=''
				OR (r->>'evidence_expires_at' IS NOT NULL AND (r->>'evidence_expires_at')::timestamptz>now()))
			ORDER BY c.priority DESC,c.created_at LIMIT $1 FOR UPDATE SKIP LOCKED`, remaining)
		if err != nil {
			return err
		}
		type suppressedCase struct{ caseID, assetID string }
		var cases []suppressedCase
		for rows.Next() {
			var item suppressedCase
			if err := rows.Scan(&item.caseID, &item.assetID); err != nil {
				rows.Close()
				return err
			}
			cases = append(cases, item)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		assetCounts := make(map[string]int)
		for _, item := range cases {
			count, ok := assetCounts[item.assetID]
			if !ok {
				if err := tx.QueryRow(ctx, `SELECT count(DISTINCT case_id) FROM (
					SELECT case_id FROM investigation_cases WHERE module_id='traffic-interception'
					AND asset_id=$1 AND automation_suppressed_reason='' AND created_at>=date_trunc('day',now())
					UNION ALL
					SELECT a.case_id FROM case_activities a JOIN investigation_cases c USING(case_id)
					WHERE c.asset_id=$1 AND a.ref_id LIKE 'automation-resumed:%'
					AND a.occurred_at>=date_trunc('day',now())
				) resumed`, item.assetID).Scan(&count); err != nil {
					return err
				}
			}
			if count >= kernel.TrafficReviewDailyCasesPerAsset {
				assetCounts[item.assetID] = count
				continue
			}
			tag, err := tx.Exec(ctx, `UPDATE investigation_cases SET automation_suppressed_reason='',updated_at=now()
				WHERE case_id=$1 AND automation_suppressed_reason='daily_investigation_quota'`, item.caseID)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 1 {
				if _, err := tx.Exec(ctx, `INSERT INTO case_activities(case_id,kind,ref_id,summary)
					VALUES($1,'state_changed',$2,'每日自动调查额度恢复，案件重新进入 Agent 分派队列')`,
					item.caseID, "automation-resumed:"+time.Now().UTC().Format("2006-01-02")); err != nil {
					return err
				}
				assetCounts[item.assetID] = count + 1
			}
		}
		return nil
	})
}

// MaintainTrafficData 提前创建日分区，重算有界汇总并执行冻结保留期。
func MaintainTrafficData(ctx context.Context, pool *pgxpool.Pool, now time.Time) (TrafficMaintenanceResult, error) {
	var result TrafficMaintenanceResult
	if pool == nil {
		return result, fmt.Errorf("traffic pool is required")
	}
	day := now.UTC().Truncate(24 * time.Hour)
	for offset := -1; offset <= 2; offset++ {
		partitionDay := day.AddDate(0, 0, offset)
		if err := ensureTrafficPartition(ctx, pool, "traffic_windows", "traffic_windows_default", "window_start", partitionDay); err != nil {
			return result, err
		}
		if err := ensureTrafficPartition(ctx, pool, "review_candidates", "review_candidates_default", "occurred_at", partitionDay); err != nil {
			return result, err
		}
	}
	err := withTx(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('traffic-maintenance'), 34)`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO traffic.traffic_hourly_metrics(asset_id, bucket_start, metrics)
			SELECT asset_id, date_trunc('hour', window_start), jsonb_build_object(
				'requests',sum(request_count),'critical',sum(critical_count),'blocked',sum(blocked_count),
				'observed',sum(observed_count),'incomplete',sum(incomplete_count),'evidence_dropped',sum(evidence_dropped_count))
			FROM traffic.traffic_windows WHERE window_start >= $1
			GROUP BY asset_id, date_trunc('hour', window_start)
			ON CONFLICT(asset_id,bucket_start) DO UPDATE SET metrics=EXCLUDED.metrics`, now.AddDate(0, 0, -8)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO traffic.traffic_daily_metrics(asset_id, bucket_start, metrics)
			SELECT asset_id, window_start::date, jsonb_build_object(
				'requests',sum(request_count),'critical',sum(critical_count),'blocked',sum(blocked_count),
				'observed',sum(observed_count),'incomplete',sum(incomplete_count),'evidence_dropped',sum(evidence_dropped_count))
			FROM traffic.traffic_windows WHERE window_start >= $1
			GROUP BY asset_id, window_start::date
			ON CONFLICT(asset_id,bucket_start) DO UPDATE SET metrics=EXCLUDED.metrics`, now.AddDate(-1, 0, 0)); err != nil {
			return err
		}
		windowCutoff := now.AddDate(0, 0, -7)
		droppedRows, err := dropExpiredTrafficPartitions(ctx, tx, "traffic_windows", windowCutoff)
		if err != nil {
			return err
		}
		result.TrafficWindows += droppedRows
		tag, err := tx.Exec(ctx, `DELETE FROM traffic.traffic_windows WHERE window_start < $1`, windowCutoff)
		if err != nil {
			return err
		}
		result.TrafficWindows += tag.RowsAffected()
		if _, err := tx.Exec(ctx, `DELETE FROM traffic.traffic_window_receipts WHERE window_start < $1`, windowCutoff); err != nil {
			return err
		}
		candidateCutoff := now.AddDate(0, 0, -30)
		droppedRows, err = dropExpiredTrafficPartitions(ctx, tx, "review_candidates", candidateCutoff)
		if err != nil {
			return err
		}
		result.ReviewCandidates += droppedRows
		tag, err = tx.Exec(ctx, `DELETE FROM traffic.review_candidates WHERE occurred_at < $1`, candidateCutoff)
		if err != nil {
			return err
		}
		result.ReviewCandidates += tag.RowsAffected()
		if _, err := tx.Exec(ctx, `DELETE FROM traffic.traffic_hourly_metrics WHERE bucket_start < $1`, now.AddDate(0, 0, -90)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM traffic.traffic_daily_metrics WHERE bucket_start < $1::date`, now.AddDate(-1, 0, 0)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM traffic.review_case_outbox WHERE processed_at < $1`, now.AddDate(0, 0, -30)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE evidence_approvals SET state='expired'
			WHERE state IN ('pending','approved') AND expires_at <= $1`, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE evidence_requests SET state='expired'
			WHERE state IN ('pending','leased','submitted') AND expires_at <= $1`, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE investigation_cases c SET state='evidence_expired', updated_at=$1
			WHERE c.state='waiting_evidence_approval' AND NOT EXISTS (
				SELECT 1 FROM evidence_approvals a WHERE a.case_id=c.case_id AND a.state IN ('pending','approved') AND a.expires_at>$1)`, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE workers w SET max_concurrency=c.previous_capacity, updated_at=$1
			FROM worker_capacity_changes c WHERE c.worker_id=w.worker_id AND c.state='approved' AND c.expires_at<=$1`, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE worker_capacity_changes SET state='expired'
			WHERE state IN ('pending','approved') AND expires_at<=$1`, now); err != nil {
			return err
		}
		tag, err = tx.Exec(ctx, `DELETE FROM investigation_cases
			WHERE state IN ('resolved','failed','evidence_expired')
			AND COALESCE(resolved_at,updated_at) < $1`, now.AddDate(0, 0, -180))
		if err != nil {
			return err
		}
		result.Cases = tag.RowsAffected()
		return nil
	})
	return result, err
}

func dropExpiredTrafficPartitions(ctx context.Context, tx pgx.Tx, parent string, cutoff time.Time) (int64, error) {
	rows, err := tx.Query(ctx, `SELECT child.relname FROM pg_inherits i
		JOIN pg_class parent ON parent.oid=i.inhparent
		JOIN pg_namespace parent_ns ON parent_ns.oid=parent.relnamespace
		JOIN pg_class child ON child.oid=i.inhrelid
		JOIN pg_namespace child_ns ON child_ns.oid=child.relnamespace
		WHERE parent_ns.nspname='traffic' AND child_ns.nspname='traffic' AND parent.relname=$1`, parent)
	if err != nil {
		return 0, err
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return 0, err
		}
		if trafficPartitionName.MatchString(name) {
			names = append(names, name)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	var deleted int64
	cutoffDay := cutoff.UTC().Truncate(24 * time.Hour)
	for _, name := range names {
		partitionDay, err := time.Parse("20060102", name[len(name)-8:])
		if err != nil || !partitionDay.Before(cutoffDay) {
			continue
		}
		var count int64
		if err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM traffic.%s`, name)).Scan(&count); err != nil {
			return deleted, err
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(`DROP TABLE traffic.%s`, name)); err != nil {
			return deleted, err
		}
		deleted += count
	}
	return deleted, nil
}

func processPendingReviewCases(ctx context.Context, pool *pgxpool.Pool, agents *AgentServer, jarvisID string) error {
	rows, err := pool.Query(ctx, `SELECT candidate_id FROM traffic.review_case_outbox
		WHERE state='pending' AND next_attempt_at<=now() ORDER BY created_at LIMIT 100`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	server := &TelemetryServer{pool: pool, trafficPool: pool, agents: agents, jarvisID: jarvisID}
	for _, id := range ids {
		if err := server.processReviewCaseOutbox(ctx, id); err != nil {
			log.Printf("流量案件 outbox %s 重试失败: %v", id, err)
		}
	}
	return nil
}

func ensureTrafficPartition(ctx context.Context, pool *pgxpool.Pool, parent, defaultTable, column string, day time.Time) error {
	name := parent + "_" + day.UTC().Format("20060102")
	if !trafficPartitionName.MatchString(name) {
		return fmt.Errorf("invalid traffic partition name")
	}
	start := day.UTC().Format("2006-01-02")
	end := day.UTC().AddDate(0, 0, 1).Format("2006-01-02")
	return withTx(ctx, pool, func(tx pgx.Tx) error {
		var attached bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM pg_inherits i JOIN pg_class c ON c.oid=i.inhrelid
			JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='traffic' AND c.relname=$1)`, name).Scan(&attached); err != nil {
			return err
		}
		if attached {
			return nil
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS traffic.%s (LIKE traffic.%s INCLUDING ALL)`, name, parent)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(`LOCK TABLE traffic.%s IN ACCESS EXCLUSIVE MODE`, defaultTable)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(`INSERT INTO traffic.%s SELECT * FROM traffic.%s
			WHERE %s >= $1 AND %s < $2 ON CONFLICT DO NOTHING`, name, defaultTable, column, column), day.UTC(), day.UTC().AddDate(0, 0, 1)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(`DELETE FROM traffic.%s WHERE %s >= $1 AND %s < $2`, defaultTable, column, column), day.UTC(), day.UTC().AddDate(0, 0, 1)); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, fmt.Sprintf(`ALTER TABLE traffic.%s ATTACH PARTITION traffic.%s
			FOR VALUES FROM ('%s') TO ('%s')`, parent, name, start, end))
		return err
	})
}
