package brain

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/encoding/protojson"

	"yufeng/lib/kernel"
	commonv1 "yufeng/proto/gen/commonv1"
	eventv1 "yufeng/proto/gen/eventv1"
)

// 调查执行实例只读工具集。不得含任何治理写。
var investigateTools = []string{
	"ticket.get",
	"cluster.get",
}

const (
	investigateCreatedBy = "system:investigate"
	investigationTTL     = time.Minute
)

// EnqueueInvestigation 创建短命调查执行实例：使用工作进程模板、只读工具以及资产与聚类绑定。
// 同一聚类已有未结束调查则复用，不入贾维斯指令队列。
func EnqueueInvestigation(ctx context.Context, pool *pgxpool.Pool, ticket *eventv1.CheckTicket) (runID string, created bool, err error) {
	if ticket == nil || ticket.EventId == "" {
		return "", false, errors.New("investigation ticket is incomplete")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	frozen, ticketDigest, err := loadFrozenInvestigationTicket(ctx, tx, ticket)
	if err != nil {
		return "", false, err
	}
	assetID, clusterID, err := investigationScope(ctx, tx, frozen)
	if err != nil {
		return "", false, err
	}
	planRef := investigationPlanRef(clusterID, frozen.GetEventId())
	bindings := []string{assetBinding(assetID)}
	if clusterID != "" {
		bindings = append(bindings, "cluster:"+clusterID)
	} else {
		bindings = append(bindings, "event:"+frozen.GetEventId())
	}
	toolJSON, err := json.Marshal(investigateTools)
	if err != nil {
		return "", false, err
	}
	bindJSON, err := json.Marshal(bindings)
	if err != nil {
		return "", false, err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1), 8)`, planRef); err != nil {
		return "", false, err
	}
	var existing string
	err = tx.QueryRow(ctx, `SELECT run_id FROM runs WHERE plan_ref=$1 AND state IN ('pending','running') LIMIT 1`, planRef).Scan(&existing)
	if err == nil && existing != "" {
		if err := tx.Commit(ctx); err != nil {
			return "", false, err
		}
		return existing, false, nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", false, err
	}
	runID, err = newID("run")
	if err != nil {
		return "", false, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO runs(run_id, state, role, plan_ref, toolset, budget, ttl, bindings, created_by, deadline)
		VALUES($1,'pending','worker',$2,$3::jsonb,'8',$4,$5::jsonb,$6,now()+make_interval(secs => $7))`,
		runID, planRef, string(toolJSON), investigationTTL.String(), string(bindJSON), investigateCreatedBy, int(investigationTTL.Seconds())); err != nil {
		return "", false, err
	}
	workID, err := newID("work")
	if err != nil {
		return "", false, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO work_items(
		work_id, run_id, investigation_event_id, investigation_ticket_digest, investigation_cluster_id)
		VALUES($1,$2,$3,$4,$5)`, workID, runID, frozen.GetEventId(), ticketDigest, clusterID); err != nil {
		return "", false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", false, err
	}
	return runID, true, nil
}

func loadFrozenInvestigationTicket(ctx context.Context, db dbTX, incoming *eventv1.CheckTicket) (*eventv1.CheckTicket, string, error) {
	incomingDigest, err := kernel.CheckTicketDigest(incoming)
	if err != nil {
		return nil, "", err
	}
	var raw []byte
	var storedDigest, status, forward string
	err = db.QueryRow(ctx, `SELECT ticket, ticket_digest, status, forward_policy FROM check_tickets WHERE event_id=$1`, incoming.GetEventId()).
		Scan(&raw, &storedDigest, &status, &forward)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", errors.New("investigation ticket is not frozen")
	}
	if err != nil {
		return nil, "", err
	}
	if status != checkTicketReady || forward != commonv1.ForwardPolicyKind_FORWARD_POLICY_KIND_AGENT_INVESTIGATE.String() {
		return nil, "", errors.New("investigation ticket is not ready for investigation")
	}
	if storedDigest == "" || incomingDigest != storedDigest {
		return nil, "", errors.New("investigation ticket does not match frozen ticket")
	}
	var frozen eventv1.CheckTicket
	if err := protojson.Unmarshal(raw, &frozen); err != nil {
		return nil, "", err
	}
	frozenDigest, err := kernel.CheckTicketDigest(&frozen)
	if err != nil || frozenDigest != storedDigest {
		return nil, "", errors.New("frozen investigation ticket digest mismatch")
	}
	return &frozen, storedDigest, nil
}

func investigationScope(ctx context.Context, db dbTX, ticket *eventv1.CheckTicket) (assetID, clusterID string, err error) {
	assetID = ticket.AssetId
	err = db.QueryRow(ctx, `SELECT COALESCE(NULLIF(asset_id,''), $2), cluster_id FROM events WHERE event_id=$1`,
		ticket.EventId, ticket.AssetId).Scan(&assetID, &clusterID)
	if errors.Is(err, pgx.ErrNoRows) {
		if assetID == "" {
			return "", "", errors.New("investigation asset is required")
		}
		return assetID, "", nil
	}
	if err != nil {
		return "", "", err
	}
	if assetID == "" {
		assetID = ticket.AssetId
	}
	if assetID == "" {
		return "", "", errors.New("investigation asset is required")
	}
	return assetID, strings.TrimSpace(clusterID), nil
}

func investigationPlanRef(clusterID, eventID string) string {
	if clusterID != "" {
		return "investigate:" + clusterID
	}
	return "investigate:event:" + eventID
}

func hasGovernTool(tools []string) bool {
	for _, t := range tools {
		if strings.HasPrefix(t, "govern.") {
			return true
		}
	}
	return false
}
