package brain

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"yufeng/lib/kernel"
)

var (
	eventPartitionName = regexp.MustCompile(`^events_\d{8}$`)
	auditPartitionName = regexp.MustCompile(`^audit_entries_\d{6}$`)
)

// LedgerMaintenanceResult 汇总一次事件与审计账本保留任务的删除结果。
type LedgerMaintenanceResult struct {
	Events          int64
	AuditEntries    int64
	AuditPartitions int64
}

// StartLedgerMaintenance 启动事件日分区、审计月分区和冻结保留任务。
//
// 审计删除必须同时具备类型化签名器和外部检查点文件；缺少任一条件时只维护分区与事件，不删除审计正文。
func StartLedgerMaintenance(ctx context.Context, pool *pgxpool.Pool, checkpointPath string, signer kernel.Signer) {
	go func() {
		run := func() {
			if _, err := MaintainLedgerData(ctx, pool, time.Now(), checkpointPath, signer); err != nil {
				log.Printf("账本分区与保留任务失败: %v", err)
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

// MaintainLedgerData 创建即将使用的分区并执行事件三十天、审计一百八十天的保留语义。
func MaintainLedgerData(ctx context.Context, pool *pgxpool.Pool, now time.Time, checkpointPath string, signer kernel.Signer) (LedgerMaintenanceResult, error) {
	var result LedgerMaintenanceResult
	if pool == nil {
		return result, errors.New("governance pool is required")
	}
	day := now.UTC().Truncate(24 * time.Hour)
	for offset := -1; offset <= 2; offset++ {
		if err := ensureLedgerPartition(ctx, pool, "events", "events_default", "occurred_at", day.AddDate(0, 0, offset), false); err != nil {
			return result, err
		}
	}
	month := time.Date(now.UTC().Year(), now.UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
	for offset := -1; offset <= 1; offset++ {
		if err := ensureLedgerPartition(ctx, pool, "audit_entries", "audit_entries_default", "occurred_at", month.AddDate(0, offset, 0), true); err != nil {
			return result, err
		}
	}
	if err := withTx(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('ledger-retention'), 35)`); err != nil {
			return err
		}
		cutoff := now.UTC().AddDate(0, 0, -30)
		removed, err := dropEventPartitions(ctx, tx, cutoff)
		if err != nil {
			return err
		}
		result.Events += removed
		tag, err := tx.Exec(ctx, `DELETE FROM events WHERE occurred_at<$1`, cutoff)
		if err != nil {
			return err
		}
		result.Events += tag.RowsAffected()
		_, err = tx.Exec(ctx, `DELETE FROM event_receipts r WHERE occurred_at<$1
			AND NOT EXISTS(SELECT 1 FROM check_tickets t WHERE t.event_id=r.event_id)
			AND NOT EXISTS(SELECT 1 FROM model_inferences m WHERE m.event_id=r.event_id)`, cutoff)
		return err
	}); err != nil {
		return result, err
	}
	if signer == nil || strings.TrimSpace(checkpointPath) == "" {
		return result, nil
	}
	auditEntries, auditPartitions, err := dropAuditPartitions(ctx, pool, now.UTC().AddDate(0, 0, -180), checkpointPath, signer)
	if err != nil {
		return result, err
	}
	result.AuditEntries = auditEntries
	result.AuditPartitions = auditPartitions
	return result, nil
}

func ensureLedgerPartition(ctx context.Context, pool *pgxpool.Pool, parent, defaultTable, column string, start time.Time, monthly bool) error {
	format := "20060102"
	end := start.AddDate(0, 0, 1)
	allowed := eventPartitionName
	if monthly {
		format = "200601"
		end = start.AddDate(0, 1, 0)
		allowed = auditPartitionName
	}
	name := parent + "_" + start.UTC().Format(format)
	if !allowed.MatchString(name) {
		return errors.New("invalid ledger partition name")
	}
	return withTx(ctx, pool, func(tx pgx.Tx) error {
		var attached bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_inherits i
			JOIN pg_class c ON c.oid=i.inhrelid JOIN pg_namespace n ON n.oid=c.relnamespace
			WHERE n.nspname=current_schema() AND c.relname=$1)`, name).Scan(&attached); err != nil {
			return err
		}
		if attached {
			return nil
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (LIKE %s INCLUDING ALL)`, name, parent)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(`LOCK TABLE %s IN ACCESS EXCLUSIVE MODE`, defaultTable)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(`INSERT INTO %s SELECT * FROM %s WHERE %s>=$1 AND %s<$2 ON CONFLICT DO NOTHING`,
			name, defaultTable, column, column), start.UTC(), end.UTC()); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(`DELETE FROM %s WHERE %s>=$1 AND %s<$2`, defaultTable, column, column), start.UTC(), end.UTC()); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, fmt.Sprintf(`ALTER TABLE %s ATTACH PARTITION %s FOR VALUES FROM ('%s') TO ('%s')`,
			parent, name, start.UTC().Format(time.RFC3339), end.UTC().Format(time.RFC3339)))
		return err
	})
}

func dropEventPartitions(ctx context.Context, tx pgx.Tx, cutoff time.Time) (int64, error) {
	names, err := listPartitions(ctx, tx, "events", eventPartitionName)
	if err != nil {
		return 0, err
	}
	var removed int64
	cutoffDay := cutoff.UTC().Truncate(24 * time.Hour)
	for _, name := range names {
		start, err := time.Parse("20060102", strings.TrimPrefix(name, "events_"))
		if err != nil || start.AddDate(0, 0, 1).After(cutoffDay) {
			continue
		}
		var count int64
		if err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM %s`, name)).Scan(&count); err != nil {
			return removed, err
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(`DROP TABLE %s`, name)); err != nil {
			return removed, err
		}
		removed += count
	}
	return removed, nil
}

func dropAuditPartitions(ctx context.Context, pool *pgxpool.Pool, cutoff time.Time, checkpointPath string, signer kernel.Signer) (int64, int64, error) {
	rows, err := pool.Query(ctx, `SELECT c.relname FROM pg_inherits i JOIN pg_class p ON p.oid=i.inhparent
		JOIN pg_class c ON c.oid=i.inhrelid JOIN pg_namespace n ON n.oid=c.relnamespace
		WHERE n.nspname=current_schema() AND p.relname='audit_entries' ORDER BY c.relname`)
	if err != nil {
		return 0, 0, err
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return 0, 0, err
		}
		if auditPartitionName.MatchString(name) {
			names = append(names, name)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, 0, err
	}
	rows.Close()
	var removed, partitions int64
	cutoffMonth := time.Date(cutoff.UTC().Year(), cutoff.UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
	for _, name := range names {
		start, err := time.Parse("200601", strings.TrimPrefix(name, "audit_entries_"))
		if err != nil || start.AddDate(0, 1, 0).After(cutoffMonth) {
			continue
		}
		count, err := checkpointAndDropAuditPartition(ctx, pool, name, checkpointPath, signer)
		if err != nil {
			return removed, partitions, err
		}
		removed += count
		partitions++
	}
	return removed, partitions, nil
}

func checkpointAndDropAuditPartition(ctx context.Context, pool *pgxpool.Pool, name, checkpointPath string, signer kernel.Signer) (int64, error) {
	if !auditPartitionName.MatchString(name) {
		return 0, errors.New("invalid audit partition name")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('audit-partition-retention'), 35)`); err != nil {
		return 0, err
	}
	var count, firstSequence, lastSequence int64
	var previousHash, lastHash string
	err = tx.QueryRow(ctx, fmt.Sprintf(`SELECT count(*),COALESCE(min(sequence),0),COALESCE(max(sequence),0),
		COALESCE((array_agg(previous_hash ORDER BY sequence))[1],''),COALESCE((array_agg(entry_hash ORDER BY sequence DESC))[1],'') FROM %s`, name)).
		Scan(&count, &firstSequence, &lastSequence, &previousHash, &lastHash)
	if err != nil {
		return 0, err
	}
	if count == 0 {
		if _, err := tx.Exec(ctx, fmt.Sprintf(`DROP TABLE %s`, name)); err != nil {
			return 0, err
		}
		return 0, tx.Commit(ctx)
	}
	checkpoint := &kernel.AuditCheckpoint{Sequence: lastSequence, Head: lastHash, CreatedAt: time.Now().UTC()}
	if err := kernel.SignAuditCheckpointWithSigner(checkpoint, signer); err != nil {
		return 0, err
	}
	if err := appendSignedCheckpoint(checkpointPath, checkpoint); err != nil {
		return 0, err
	}
	checkpointRef := fmt.Sprintf("%s#%d", checkpointPath, lastSequence)
	if _, err := tx.Exec(ctx, `INSERT INTO audit_partition_anchors(
		partition_name,first_sequence,last_sequence,previous_hash,last_hash,checkpoint_ref,checkpoint_signature)
		VALUES($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT(partition_name) DO UPDATE SET first_sequence=EXCLUDED.first_sequence,last_sequence=EXCLUDED.last_sequence,
		previous_hash=EXCLUDED.previous_hash,last_hash=EXCLUDED.last_hash,checkpoint_ref=EXCLUDED.checkpoint_ref,
		checkpoint_signature=EXCLUDED.checkpoint_signature`, name, firstSequence, lastSequence, previousHash, lastHash,
		checkpointRef, base64.StdEncoding.EncodeToString(checkpoint.Signature)); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf(`DROP TABLE %s`, name)); err != nil {
		return 0, err
	}
	return count, tx.Commit(ctx)
}

func appendSignedCheckpoint(path string, checkpoint *kernel.AuditCheckpoint) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	writeErr := kernel.WriteSignedAuditCheckpoint(f, checkpoint)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	return errors.Join(writeErr, f.Close())
}

func listPartitions(ctx context.Context, tx pgx.Tx, parent string, allowed *regexp.Regexp) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT c.relname FROM pg_inherits i JOIN pg_class p ON p.oid=i.inhparent
		JOIN pg_class c ON c.oid=i.inhrelid JOIN pg_namespace n ON n.oid=c.relnamespace
		WHERE n.nspname=current_schema() AND p.relname=$1`, parent)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		if allowed.MatchString(name) {
			names = append(names, name)
		}
	}
	return names, rows.Err()
}
