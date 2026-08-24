package brain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	caseAgentAssignmentActivity = "agent-profile-assigned"
	caseAgentRequiredActivity   = "agent-profile-required"
)

type frozenAgentBinding struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// frozenAgentProfile 是案件创建时固定的受管 Agent 能力快照。
//
// 档案后续更新或删除不得改变已经分派案件的授权依据。
type frozenAgentProfile struct {
	AgentID      string               `json:"agent_id"`
	DisplayName  string               `json:"display_name"`
	Kind         string               `json:"kind"`
	Tools        []string             `json:"tools"`
	Bindings     []frozenAgentBinding `json:"bindings"`
	ConfigDigest string               `json:"config_digest"`
}

type profileSnapshotContent struct {
	AgentID     string               `json:"agent_id"`
	DisplayName string               `json:"display_name"`
	Kind        string               `json:"kind"`
	Tools       []string             `json:"tools"`
	Bindings    []frozenAgentBinding `json:"bindings"`
}

// assignCaseAgentProfile 给尚未编排的案件选择资产匹配的启用档案并创建待投递记录。
func assignCaseAgentProfile(ctx context.Context, tx pgx.Tx, caseID string) (bool, error) {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('managed-agent-case-assignment'), 34)`); err != nil {
		return false, err
	}
	var assetID, assignedAgentID, state string
	if err := tx.QueryRow(ctx, `SELECT asset_id, assigned_agent_id, state FROM investigation_cases
		WHERE case_id=$1 FOR UPDATE`, caseID).Scan(&assetID, &assignedAgentID, &state); err != nil {
		return false, err
	}
	if assignedAgentID != "" || state != "open" {
		return assignedAgentID != "", nil
	}

	var profile frozenAgentProfile
	var toolsRaw, bindingsRaw []byte
	var storedConfigDigest string
	err := tx.QueryRow(ctx, `SELECT p.agent_id, p.display_name, p.kind, p.tools, p.bindings,p.config_digest
		FROM managed_agent_profiles p
		WHERE p.agent_id=(
			SELECT candidate.agent_id
			FROM managed_agent_profiles candidate
			WHERE candidate.kind='traffic_review' AND candidate.state='enabled'
			  AND EXISTS (
				SELECT 1 FROM jsonb_array_elements(candidate.bindings) binding
				WHERE binding->>'kind'='asset' AND binding->>'id'=$1
			  )
			ORDER BY (
				SELECT count(*) FROM investigation_cases active_case
				WHERE active_case.assigned_agent_id=candidate.agent_id
				  AND active_case.state NOT IN ('resolved','failed','evidence_expired')
			), candidate.updated_at, candidate.agent_id
			LIMIT 1
		) FOR SHARE`, assetID).Scan(&profile.AgentID, &profile.DisplayName, &profile.Kind, &toolsRaw, &bindingsRaw, &storedConfigDigest)
	if errors.Is(err, pgx.ErrNoRows) {
		_, insertErr := tx.Exec(ctx, `INSERT INTO case_activities(case_id, kind, ref_id, summary)
			SELECT $1,'recommendation',$2,'没有启用且覆盖该资产的受管 Agent，案件保持待编排'
			WHERE NOT EXISTS (SELECT 1 FROM case_activities WHERE case_id=$1 AND ref_id=$2)`,
			caseID, caseAgentRequiredActivity)
		return false, insertErr
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(toolsRaw, &profile.Tools); err != nil {
		return false, fmt.Errorf("decode managed agent tools: %w", err)
	}
	if err := json.Unmarshal(bindingsRaw, &profile.Bindings); err != nil {
		return false, fmt.Errorf("decode managed agent bindings: %w", err)
	}
	if !frozenProfileCoversAsset(profile, assetID) {
		return false, errors.New("selected managed agent does not cover case asset")
	}
	profile.Tools = normalizedFrozenProfileTools(profile.Tools)
	if len(profile.Tools) == 0 {
		return false, errors.New("selected managed agent has no usable tools")
	}
	profile.ConfigDigest, err = digestFrozenAgentProfile(profile)
	if err != nil {
		return false, err
	}
	if storedConfigDigest != "" && storedConfigDigest != profile.ConfigDigest {
		return false, errors.New("managed agent configuration digest mismatch")
	}
	snapshot, err := json.Marshal(profile)
	if err != nil {
		return false, err
	}
	tag, err := tx.Exec(ctx, `UPDATE investigation_cases
		SET assigned_agent_id=$2, assigned_agent_display_name=$3, agent_profile_snapshot=$4::jsonb, updated_at=now()
		WHERE case_id=$1 AND assigned_agent_id='' AND state='open'`, caseID, profile.AgentID, profile.DisplayName, snapshot)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() != 1 {
		return false, nil
	}
	if _, err := tx.Exec(ctx, `INSERT INTO case_delegation_outbox(case_id) VALUES($1)
		ON CONFLICT(case_id) DO NOTHING`, caseID); err != nil {
		return false, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO case_activities(case_id, kind, ref_id, summary)
		VALUES($1,'state_changed',$2,$3)`, caseID, caseAgentAssignmentActivity,
		"案件已分派给受管 Agent "+profile.DisplayName+"，等待 Jarvis 领取")
	return err == nil, err
}

func normalizedFrozenProfileTools(raw []string) []string {
	set := make(map[string]struct{}, len(raw))
	for _, tool := range raw {
		if _, ok := trafficReviewProfileTools[tool]; ok {
			set[tool] = struct{}{}
		}
	}
	tools := make([]string, 0, len(set))
	for tool := range set {
		tools = append(tools, tool)
	}
	sort.Strings(tools)
	return tools
}

func frozenProfileCoversAsset(profile frozenAgentProfile, assetID string) bool {
	for _, binding := range profile.Bindings {
		if binding.Kind == "asset" && binding.ID == assetID {
			return true
		}
	}
	return false
}

func digestFrozenAgentProfile(profile frozenAgentProfile) (string, error) {
	content := profileSnapshotContent{
		AgentID: profile.AgentID, DisplayName: profile.DisplayName, Kind: profile.Kind,
		Tools: profile.Tools, Bindings: profile.Bindings,
	}
	raw, err := json.Marshal(content)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func caseInstructionTools(profile frozenAgentProfile) []string {
	return normalizedFrozenProfileTools(profile.Tools)
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func frozenCaseInstructionScope(ctx context.Context, db dbTX, caseID string) (frozenAgentProfile, string, error) {
	var profile frozenAgentProfile
	var assetID, assignedAgentID string
	var snapshotRaw []byte
	err := db.QueryRow(ctx, `SELECT asset_id, assigned_agent_id, agent_profile_snapshot
		FROM investigation_cases WHERE case_id=$1`, caseID).Scan(&assetID, &assignedAgentID, &snapshotRaw)
	if err != nil {
		return profile, "", err
	}
	if err := json.Unmarshal(snapshotRaw, &profile); err != nil {
		return profile, "", fmt.Errorf("decode frozen agent profile: %w", err)
	}
	digest, err := digestFrozenAgentProfile(profile)
	if err != nil {
		return profile, "", err
	}
	if profile.AgentID == "" || profile.AgentID != assignedAgentID || profile.ConfigDigest == "" ||
		profile.ConfigDigest != digest || !frozenProfileCoversAsset(profile, assetID) {
		return profile, "", errors.New("frozen agent profile is incomplete")
	}
	for _, tool := range requiredTrafficReviewProfileTools {
		if !containsString(profile.Tools, tool) {
			return profile, "", errors.New("frozen agent profile does not contain the traffic review toolset")
		}
	}
	return profile, assetID, nil
}

func enqueueCaseReviewInstruction(ctx context.Context, db dbTX, agents *AgentServer, jarvisID, caseID, dedupeKey string) (frozenAgentProfile, error) {
	profile, assetID, err := frozenCaseInstructionScope(ctx, db, caseID)
	if err != nil {
		return profile, err
	}
	err = agents.enqueueInstructionDeduped(ctx, db, jarvisID, instructionCaseReview, caseID,
		caseInstructionTools(profile), []string{assetBinding(assetID), "case:" + caseID}, dedupeKey)
	return profile, err
}

// reconcilePendingCaseDelegations 为历史上因缺少合格档案而待编排的案件重新匹配。
func reconcilePendingCaseDelegations(ctx context.Context, pool *pgxpool.Pool) error {
	rows, err := pool.Query(ctx, `SELECT case_id FROM investigation_cases
		WHERE module_id='traffic-interception' AND state='open' AND assigned_agent_id=''
		AND automation_suppressed_reason=''
		ORDER BY priority DESC, created_at LIMIT 100`)
	if err != nil {
		return err
	}
	var caseIDs []string
	for rows.Next() {
		var caseID string
		if err := rows.Scan(&caseID); err != nil {
			rows.Close()
			return err
		}
		caseIDs = append(caseIDs, caseID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, caseID := range caseIDs {
		if err := withTx(ctx, pool, func(tx pgx.Tx) error {
			_, assignErr := assignCaseAgentProfile(ctx, tx, caseID)
			return assignErr
		}); err != nil {
			return err
		}
	}
	return nil
}

// processPendingCaseDelegations 把冻结后的案件能力安全投递给单逻辑 Jarvis。
func processPendingCaseDelegations(ctx context.Context, pool *pgxpool.Pool, agents *AgentServer, jarvisID string) error {
	if agents == nil {
		return errors.New("agent control service is required for case delegation")
	}
	if strings.TrimSpace(jarvisID) == "" {
		return errors.New("jarvis agent id is required for case delegation")
	}
	rows, err := pool.Query(ctx, `SELECT case_id FROM case_delegation_outbox
		WHERE state='pending' AND next_attempt_at<=now() ORDER BY created_at LIMIT 100`)
	if err != nil {
		return err
	}
	var caseIDs []string
	for rows.Next() {
		var caseID string
		if err := rows.Scan(&caseID); err != nil {
			rows.Close()
			return err
		}
		caseIDs = append(caseIDs, caseID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, caseID := range caseIDs {
		if err := dispatchCaseDelegation(ctx, pool, agents, jarvisID, caseID); err != nil {
			if _, recordErr := pool.Exec(ctx, `UPDATE case_delegation_outbox
				SET attempts=attempts+1,
					next_attempt_at=now()+LEAST(interval '15 minutes', interval '5 seconds' * power(2, LEAST(attempts,8))),
					last_error=$2
				WHERE case_id=$1 AND state='pending'`, caseID, truncateTrafficError(err.Error())); recordErr != nil {
				return errors.Join(err, recordErr)
			}
		}
	}
	return nil
}

func dispatchCaseDelegation(ctx context.Context, pool *pgxpool.Pool, agents *AgentServer, jarvisID, caseID string) error {
	return withTx(ctx, pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT outbox.case_id
			FROM case_delegation_outbox outbox JOIN investigation_cases c USING(case_id)
			WHERE outbox.case_id=$1 AND outbox.state='pending' AND outbox.next_attempt_at<=now()
			FOR UPDATE OF outbox`, caseID).Scan(&caseID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		profile, err := enqueueCaseReviewInstruction(ctx, tx, agents, jarvisID, caseID, "case-assigned:"+caseID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE case_delegation_outbox
			SET state='dispatched', attempts=attempts+1, dispatched_at=now(), last_error=''
			WHERE case_id=$1`, caseID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO case_activities(case_id, kind, ref_id, summary)
			VALUES($1,'run_progress',$2,'Jarvis 已收到受管 Agent 调查委派')`, caseID, profile.AgentID)
		return err
	})
}
