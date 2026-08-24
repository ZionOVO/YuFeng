package brain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
)

const (
	threadSourceSession = "session"
	threadSourceRun     = "run"
	turnStatePending    = "pending"
	turnStateRunning    = "running"
	itemKindInput       = "input_reference"
)

type turnSeed struct {
	SourceKind    string
	SourceRef     string
	SubjectID     string
	SourceVersion int64
	SourceCursor  any
	InputSnapshot any
	BudgetID      string
	ContentRef    string
}

func ensureAgentTurn(ctx context.Context, db dbTX, seed turnSeed) (threadID, turnID string, err error) {
	cursor, err := json.Marshal(seed.SourceCursor)
	if err != nil {
		return "", "", err
	}
	snapshot, err := json.Marshal(seed.InputSnapshot)
	if err != nil {
		return "", "", err
	}
	threadID, err = newID("thr")
	if err != nil {
		return "", "", err
	}
	if _, err := db.Exec(ctx, `INSERT INTO agent_threads(thread_id, source_kind, source_ref, agent_id)
		VALUES($1,$2,$3,$4) ON CONFLICT (source_kind, source_ref, agent_id) DO NOTHING`,
		threadID, seed.SourceKind, seed.SourceRef, seed.SubjectID); err != nil {
		return "", "", err
	}
	if err := db.QueryRow(ctx, `SELECT thread_id FROM agent_threads
		WHERE source_kind=$1 AND source_ref=$2 AND agent_id=$3`, seed.SourceKind, seed.SourceRef, seed.SubjectID).Scan(&threadID); err != nil {
		return "", "", err
	}
	turnID, err = newID("turn")
	if err != nil {
		return "", "", err
	}
	if _, err := db.Exec(ctx, `INSERT INTO agent_turns(
		turn_id, thread_id, source_version, source_cursor, input_snapshot, budget_id)
		VALUES($1,$2,$3,$4::jsonb,$5::jsonb,$6)
		ON CONFLICT (thread_id, source_version) DO NOTHING`,
		turnID, threadID, seed.SourceVersion, cursor, snapshot, seed.BudgetID); err != nil {
		return "", "", err
	}
	if err := db.QueryRow(ctx, `SELECT turn_id FROM agent_turns WHERE thread_id=$1 AND source_version=$2`,
		threadID, seed.SourceVersion).Scan(&turnID); err != nil {
		return "", "", err
	}
	stepID, err := newID("step")
	if err != nil {
		return "", "", err
	}
	if _, err := db.Exec(ctx, `INSERT INTO agent_steps(step_id, turn_id, step_sequence)
		VALUES($1,$2,1) ON CONFLICT (turn_id, step_sequence) DO NOTHING`, stepID, turnID); err != nil {
		return "", "", err
	}
	if err := db.QueryRow(ctx, `SELECT step_id FROM agent_steps WHERE turn_id=$1 AND step_sequence=1`, turnID).Scan(&stepID); err != nil {
		return "", "", err
	}
	itemID, err := newID("item")
	if err != nil {
		return "", "", err
	}
	if _, err := db.Exec(ctx, `INSERT INTO agent_items(
		item_id, turn_id, step_id, item_sequence, kind, content_ref, content_digest, payload)
		VALUES($1,$2,$3,1,$4,$5,$6,$7::jsonb) ON CONFLICT (turn_id, item_sequence) DO NOTHING`,
		itemID, turnID, stepID, itemKindInput, seed.ContentRef, agentContentDigest(snapshot), snapshot); err != nil {
		return "", "", err
	}
	if _, err := db.Exec(ctx, `UPDATE agent_turns SET next_item_sequence=GREATEST(next_item_sequence,2), updated_at=now()
		WHERE turn_id=$1`, turnID); err != nil {
		return "", "", err
	}
	return threadID, turnID, nil
}

func appendTurnInput(ctx context.Context, tx pgx.Tx, turnID, kind, contentRef string) (int64, error) {
	var sequence int64
	if err := tx.QueryRow(ctx, `SELECT next_input_sequence FROM agent_turns WHERE turn_id=$1 FOR UPDATE`, turnID).Scan(&sequence); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO agent_turn_inputs(turn_id, input_sequence, kind, content_ref)
		VALUES($1,$2,$3,$4)`, turnID, sequence, kind, contentRef); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `UPDATE agent_turns SET next_input_sequence=$2, updated_at=now() WHERE turn_id=$1`,
		turnID, sequence+1); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `UPDATE agent_turns SET state='pending', updated_at=now()
		WHERE turn_id=$1 AND state='waiting_input'`, turnID); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `UPDATE agent_instructions SET status='pending'
		WHERE turn_id=$1 AND status='waiting'`, turnID); err != nil {
		return 0, err
	}
	return sequence, nil
}

func agentContentDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func sourceRefForTurn(ctx context.Context, db dbTX, turnID, sourceKind string) (string, error) {
	var sourceRef string
	err := db.QueryRow(ctx, `SELECT th.source_ref FROM agent_turns t
		JOIN agent_threads th ON th.thread_id=t.thread_id
		WHERE t.turn_id=$1 AND th.source_kind=$2`, turnID, sourceKind).Scan(&sourceRef)
	return sourceRef, err
}

func cognitiveTurnExists(ctx context.Context, db dbTX, turnID string) (bool, error) {
	var exists bool
	err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_turns WHERE turn_id=$1)`, turnID).Scan(&exists)
	return exists, err
}

func runPlanDigest(planRef string) string {
	return agentContentDigest([]byte(fmt.Sprintf("plan_ref:%s", planRef)))
}
