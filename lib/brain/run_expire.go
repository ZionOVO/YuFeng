package brain

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const runDeadlineError = "execution deadline exceeded"

func expireRunDeadline(ctx context.Context, pool *pgxpool.Pool, runID string, now time.Time) (bool, error) {
	expired := false
	err := withTx(ctx, pool, func(tx pgx.Tx) error {
		var state, budgetID, turnID, workID string
		var deadline *time.Time
		err := tx.QueryRow(ctx, `SELECT work_id, budget_id, turn_id FROM work_items
			WHERE run_id=$1 ORDER BY work_id LIMIT 1 FOR UPDATE`, runID).Scan(&workID, &budgetID, &turnID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		err = tx.QueryRow(ctx, `SELECT state, deadline FROM runs WHERE run_id=$1 FOR UPDATE`, runID).
			Scan(&state, &deadline)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if isRunTerminal(state) || deadline == nil || now.Before(*deadline) {
			return nil
		}
		if err := closeRunBudget(ctx, tx, budgetID, "expired", false, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE capability_budget SET revoked=true WHERE budget_id=$1`, budgetID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE capability_token_instances SET revoked=true WHERE budget_id=$1`, budgetID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE work_items SET status='failed', updated_at=now()
			WHERE run_id=$1 AND status IN ('pending','leased')`, runID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE runs SET state='failed', error=$2, updated_at=now() WHERE run_id=$1`, runID, runDeadlineError); err != nil {
			return err
		}
		if workID != "" {
			if err := recordInvestigationTerminal(ctx, tx, workID, "timeout", "deadline_exceeded", runDeadlineError); err != nil {
				return err
			}
		}
		if turnID != "" {
			if _, err := tx.Exec(ctx, `UPDATE agent_turns SET state='failed', updated_at=now()
				WHERE turn_id=$1 AND state NOT IN ('completed','failed','cancelled','outcome_unknown')`, turnID); err != nil {
				return err
			}
		}
		if err := appendRunEvent(ctx, tx, runID, "deadline", runDeadlineError); err != nil {
			return err
		}
		expired = true
		return nil
	})
	return expired, err
}

func expireDueRuns(ctx context.Context, pool *pgxpool.Pool, now time.Time) (int, error) {
	rows, err := pool.Query(ctx, `SELECT run_id FROM runs
		WHERE state NOT IN ('succeeded','failed','cancelled') AND deadline IS NOT NULL AND deadline <= $1
		ORDER BY deadline LIMIT 100`, now)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	count := 0
	for _, id := range ids {
		expired, err := expireRunDeadline(ctx, pool, id, now)
		if err != nil {
			return count, err
		}
		if expired {
			count++
		}
	}
	return count, nil
}

func pauseExpiredRunBudgetLeases(ctx context.Context, pool *pgxpool.Pool, now time.Time) (int, error) {
	rows, err := pool.Query(ctx, `SELECT w.work_id FROM work_items w
		JOIN run_budget_accounts b ON b.budget_id=w.budget_id
		WHERE w.status='leased' AND w.lease_deadline IS NOT NULL AND w.lease_deadline <= $1
		  AND b.active_started_at IS NOT NULL
		ORDER BY w.lease_deadline LIMIT 100`, now)
	if err != nil {
		return 0, err
	}
	var workIDs []string
	for rows.Next() {
		var workID string
		if err := rows.Scan(&workID); err != nil {
			rows.Close()
			return 0, err
		}
		workIDs = append(workIDs, workID)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	paused := 0
	for _, workID := range workIDs {
		err := withTx(ctx, pool, func(tx pgx.Tx) error {
			var budgetID string
			var deadline time.Time
			err := tx.QueryRow(ctx, `SELECT w.budget_id, w.lease_deadline FROM work_items w
				JOIN run_budget_accounts b ON b.budget_id=w.budget_id
				WHERE w.work_id=$1 AND w.status='leased' AND w.lease_deadline <= $2
				  AND b.active_started_at IS NOT NULL FOR UPDATE OF w`, workID, now).
				Scan(&budgetID, &deadline)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			if err := stopRunBudgetActive(ctx, tx, budgetID, deadline); err != nil {
				return err
			}
			paused++
			return nil
		})
		if err != nil {
			return paused, err
		}
	}
	return paused, nil
}
