package brain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/encoding/protojson"

	artifactv1 "yufeng/proto/gen/artifactv1"
	commonv1 "yufeng/proto/gen/commonv1"
	eventv1 "yufeng/proto/gen/eventv1"

	"yufeng/lib/eventbus"
	telemetryv1 "yufeng/proto/gen/telemetryv1"
	"yufeng/proto/gen/telemetryv1/telemetryv1connect"
)

// TelemetryServer 是事件上行服务。
type TelemetryServer struct {
	pool        *pgxpool.Pool
	trafficPool *pgxpool.Pool
	agents      *AgentServer
	jarvisID    string
	demoTriage  bool
}

// NewTelemetryServer 构造遥测服务。
func NewTelemetryServer(pool *pgxpool.Pool, _ *eventbus.Bus, agents *AgentServer, jarvisID string, trafficPools ...*pgxpool.Pool) *TelemetryServer {
	if jarvisID == "" {
		jarvisID = "jarvis-1"
	}
	trafficPool := pool
	if len(trafficPools) > 0 && trafficPools[0] != nil {
		trafficPool = trafficPools[0]
	}
	return &TelemetryServer{pool: pool, trafficPool: trafficPool, agents: agents, jarvisID: jarvisID}
}

// Handler 返回 Connect 服务端处理器。
func (s *TelemetryServer) Handler() (string, http.Handler) {
	return telemetryv1connect.NewTelemetryServiceHandler(s, handlerOptions()...)
}

// UploadEvents 同步写入事件账：按 event_id 幂等，逐条返回拒因。
func (s *TelemetryServer) UploadEvents(ctx context.Context, req *connect.Request[telemetryv1.UploadEventsRequest]) (*connect.Response[telemetryv1.UploadEventsResponse], error) {
	unitID, err := requireUnitRPC(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	if len(req.Msg.Events) > 100 {
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("at most 100 events per batch"))
	}
	resp := &telemetryv1.UploadEventsResponse{}
	// 单元存在性对整批不变，提到循环外只查一次。
	var unitExists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM units WHERE unit_id=$1)`, unitID).Scan(&unitExists); err != nil {
		return nil, err
	}
	if !unitExists {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid unit token"))
	}
	for _, event := range req.Msg.Events {
		if event == nil || strings.TrimSpace(event.Id) == "" {
			resp.Rejected = append(resp.Rejected, &telemetryv1.RejectedEvent{EventId: eventID(event), Code: "invalid_event", Message: "event id is required"})
			continue
		}
		outcome, reject, err := s.ingestEvent(ctx, unitID, event)
		if err != nil {
			return nil, err
		}
		if reject != nil {
			resp.Rejected = append(resp.Rejected, reject)
			continue
		}
		switch outcome {
		case "deduped":
			resp.Deduped++
		default:
			resp.Accepted++
		}
	}
	return connect.NewResponse(resp), nil
}

func (s *TelemetryServer) ingestEvent(ctx context.Context, unitID string, event *eventv1.Event) (string, *telemetryv1.RejectedEvent, error) {
	var bound bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM unit_assets WHERE unit_id=$1 AND asset_id=$2)`, unitID, event.AssetId).Scan(&bound); err != nil {
		return "", nil, err
	}
	if !bound {
		return "", nil, connect.NewError(connect.CodePermissionDenied, errors.New("asset is not bound to unit"))
	}
	if h := event.GetHttp(); h != nil && h.QueryRedacted != "" {
		h.QueryRedacted = RedactQuery(h.QueryRedacted)
	}
	if !s.demoTriage && event.GetTriageReason() == 0 && len(event.GetDetections()) > 0 {
		event.TriageReason = commonv1.TriageReason_TRIAGE_REASON_DETECTED_UNMITIGATED
	}
	payload, err := protojson.Marshal(event)
	if err != nil {
		return "", &telemetryv1.RejectedEvent{EventId: event.Id, Code: "invalid_event", Message: err.Error()}, nil
	}
	payloadSum := sha256.Sum256(payload)
	payloadDigest := "sha256:" + hex.EncodeToString(payloadSum[:])
	traces, err := protojson.Marshal(&eventv1.Event{ReleaseTraces: event.ReleaseTraces})
	if err != nil {
		return "", &telemetryv1.RejectedEvent{EventId: event.Id, Code: "invalid_event", Message: err.Error()}, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	receiptTag, err := tx.Exec(ctx, `INSERT INTO event_receipts(event_id,payload_digest,occurred_at)
		VALUES($1,$2,$3) ON CONFLICT(event_id) DO NOTHING`, event.Id, payloadDigest, event.OccurredAt.AsTime())
	if err != nil {
		return "", nil, err
	}
	if receiptTag.RowsAffected() == 0 {
		var storedDigest string
		if err := tx.QueryRow(ctx, `SELECT payload_digest FROM event_receipts WHERE event_id=$1`, event.Id).Scan(&storedDigest); err != nil {
			return "", nil, err
		}
		if storedDigest != payloadDigest {
			return "", &telemetryv1.RejectedEvent{EventId: event.Id, Code: "event_id_conflict",
				Message: "event id was already used with different content", PayloadDigest: payloadDigest}, nil
		}
		if err := tx.Commit(ctx); err != nil {
			return "", nil, err
		}
		return "deduped", nil, nil
	}
	_, err = tx.Exec(ctx, `INSERT INTO events(event_id, unit_id, asset_id, request_id, occurred_at, source, kind, verdict,
		payload, release_traces,payload_digest)
	VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb,$10::jsonb,$11)`,
		event.Id, unitID, event.AssetId, event.RequestId, event.OccurredAt.AsTime(), event.Source,
		eventKindString(event.Kind), eventVerdictString(event.Verdict), string(payload), traces, payloadDigest)
	if err != nil {
		return "", nil, err
	}
	if _, err := freezeCheckTicket(ctx, tx, unitID, event); err != nil {
		return "", nil, err
	}
	if s.agents != nil {
		if err := s.maybeEnqueueTriage(ctx, tx, event); err != nil {
			return "", nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return "", nil, err
	}
	return "accepted", nil, nil
}

func (s *TelemetryServer) maybeEnqueueTriage(ctx context.Context, tx dbTX, event *eventv1.Event) error {
	facts := triageFacts{
		Accepted:     true,
		VerdictAllow: eventVerdictAllow(event),
		HasHTTP:      eventHasHTTP(event),
	}
	var err error
	facts.EventAlreadyQueued, err = triageEventQueued(ctx, tx, event.Id)
	if err != nil {
		return err
	}
	path := ""
	method := ""
	if h := event.GetHttp(); h != nil {
		path = h.Path
		method = h.Method
	}
	facts.OpenRuleOnPath, err = openRuleOnPath(ctx, tx, event.AssetId, path)
	if err != nil {
		return err
	}
	facts.PendingSamePath, err = pendingTriageSamePath(ctx, tx, event.AssetId, method, path)
	if err != nil {
		return err
	}
	facts.JarvisHasPubkey, err = agentHasPubkey(ctx, tx, s.jarvisID)
	if err != nil {
		return err
	}
	reason := event.GetTriageReason()
	if reason == 0 && len(event.GetDetections()) > 0 {
		reason = commonv1.TriageReason_TRIAGE_REASON_DETECTED_UNMITIGATED
		event.TriageReason = reason
	}
	if s.demoTriage {
		if !shouldEnqueueTriage(facts) {
			return nil
		}
		if reason == commonv1.TriageReason_TRIAGE_REASON_UNSPECIFIED {
			reason = commonv1.TriageReason_TRIAGE_REASON_DETECTED_UNMITIGATED
			event.TriageReason = reason
		}
	} else if !shouldEnqueueProduction(true, facts.JarvisHasPubkey, reason, false) {
		return nil
	}
	clusterID, err := upsertTriageCluster(ctx, tx, event, time.Now())
	if err != nil {
		return err
	}
	payloadRef := clusterID
	tools := demoTriageInstructionTools
	bindings := []string{assetBinding(event.AssetId)}
	if s.demoTriage {
		pending, err := pendingTriageCluster(ctx, tx, clusterID)
		if err != nil {
			return err
		}
		if pending {
			return nil
		}
	} else {
		pending, err := pendingTriageSource(ctx, tx, clusterID)
		if err != nil {
			return err
		}
		if pending {
			return nil
		}
		payloadRef, err = ensureTriageTurn(ctx, tx, s.jarvisID, clusterID)
		if err != nil {
			return err
		}
		queued, err := triageTurnQueued(ctx, tx, payloadRef)
		if err != nil {
			return err
		}
		if queued {
			return nil
		}
		tools = triageInstructionTools
		bindings = append(bindings, turnBinding(payloadRef))
	}
	err = s.agents.enqueueInstruction(ctx, tx, s.jarvisID, instructionTriage, payloadRef, tools, bindings)
	if errors.Is(err, errNoAgentKey) {
		return nil
	}
	return err
}

func triageEventQueued(ctx context.Context, db dbTX, eventID string) (bool, error) {
	var n int
	err := db.QueryRow(ctx, `SELECT count(*) FROM agent_instructions WHERE kind=$1 AND payload_ref=$2`, instructionTriage, eventID).Scan(&n)
	return n > 0, err
}

func agentHasPubkey(ctx context.Context, db dbTX, agentID string) (bool, error) {
	var pub string
	err := db.QueryRow(ctx, `SELECT public_key FROM agents WHERE agent_id=$1`, agentID).Scan(&pub)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return strings.TrimSpace(pub) != "", err
}

func openRuleOnPath(ctx context.Context, db dbTX, assetID, path string) (bool, error) {
	rows, err := db.Query(ctx, `SELECT r.artifact FROM releases r
		JOIN release_assets ra ON ra.release_id = r.release_id
		WHERE ra.asset_id=$1 AND r.state <> 'retired'`, assetID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return false, err
		}
		var a artifactv1.Artifact
		if err := protojson.Unmarshal(raw, &a); err != nil {
			continue
		}
		if a.Kind != artifactv1.Kind_KIND_RULE {
			continue
		}
		sel := ""
		if a.Scope != nil {
			sel = a.Scope.RouteSelector
		}
		if sel == "" || strings.HasPrefix(path, sel) {
			return true, nil
		}
	}
	return false, rows.Err()
}

func pendingTriageSamePath(ctx context.Context, db dbTX, assetID, method, path string) (bool, error) {
	rows, err := db.Query(ctx, `SELECT e.payload FROM agent_instructions i
		JOIN events e ON e.event_id = i.payload_ref
		WHERE i.kind=$1 AND i.status IN ('pending','leased') AND e.asset_id=$2`, instructionTriage, assetID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return false, err
		}
		var ev eventv1.Event
		if err := protojson.Unmarshal(raw, &ev); err != nil {
			continue
		}
		h := ev.GetHttp()
		if h != nil && h.Method == method && h.Path == path {
			return true, nil
		}
	}
	return false, rows.Err()
}

func eventID(e *eventv1.Event) string {
	if e == nil {
		return ""
	}
	return e.Id
}

func eventKindString(k eventv1.Kind) string {
	switch k {
	case eventv1.Kind_KIND_TRAFFIC:
		return "traffic"
	case eventv1.Kind_KIND_SENSOR:
		return "sensor"
	case eventv1.Kind_KIND_INTEL:
		return "intel"
	case eventv1.Kind_KIND_AGENT:
		return "agent"
	case eventv1.Kind_KIND_MODEL_ALERT:
		return "model_alert"
	case eventv1.Kind_KIND_MODEL_REVIEW_SAMPLE:
		return "model_review_sample"
	default:
		return "unspecified"
	}
}

func eventVerdictString(v eventv1.Verdict) string {
	switch v {
	case eventv1.Verdict_VERDICT_ALLOW:
		return "allow"
	case eventv1.Verdict_VERDICT_BLOCK:
		return "block"
	case eventv1.Verdict_VERDICT_OBSERVE:
		return "observe"
	case eventv1.Verdict_VERDICT_ESCALATE:
		return "escalate"
	default:
		return "unspecified"
	}
}
