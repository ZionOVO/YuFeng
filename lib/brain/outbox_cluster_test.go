package brain

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "yufeng/proto/gen/commonv1"
	eventv1 "yufeng/proto/gen/eventv1"
	telemetryv1 "yufeng/proto/gen/telemetryv1"
)

func TestClusterHundredEventsOneInstruction(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	_, assetID, token := seedUnitAsset(t, ctx, st, "cluster")
	jarvis := "jarvis-cluster-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO agents(agent_id, refresh_token_hash, role, public_key)
		VALUES($1,'x','orchestrator','pub')`, jarvis); err != nil {
		t.Fatal(err)
	}
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	telemetry := NewTelemetryServer(st.Pool(), nil, NewAgentServer(st.Pool(), "boot", privateKey), jarvis)
	batch := make([]*eventv1.Event, 0, 100)
	for index := 0; index < 100; index++ {
		coverage := commonv1.CoverageStatus_COVERAGE_STATUS_FULL
		if index%2 == 1 {
			coverage = commonv1.CoverageStatus_COVERAGE_STATUS_PARTIAL
		}
		batch = append(batch, productionTriageEvent("evt-cluster-"+newTestSuffix()+"-"+itoa(index), assetID, "942100", coverage))
	}
	request := connect.NewRequest(&telemetryv1.UploadEventsRequest{Events: batch})
	request.Header().Set("Authorization", "Bearer "+token)
	if _, err := telemetry.UploadEvents(ctx, request); err != nil {
		t.Fatal(err)
	}
	var count int
	var payloadRef, storedTurn string
	if err := st.Pool().QueryRow(ctx, `SELECT count(*), min(payload_ref), min(turn_id)
		FROM agent_instructions WHERE kind=$1 AND agent_id=$2`, instructionTriage, jarvis).Scan(&count, &payloadRef, &storedTurn); err != nil {
		t.Fatal(err)
	}
	if count != 1 || !strings.HasPrefix(payloadRef, "turn_") || storedTurn != payloadRef {
		t.Fatalf("cluster instruction count=%d payload=%q turn=%q", count, payloadRef, storedTurn)
	}
	var clusterID, pinnedAsset string
	var sourceVersion int64
	var pinnedEvents int
	if err := st.Pool().QueryRow(ctx, `SELECT th.source_ref, t.source_version,
		t.input_snapshot->>'assetId', jsonb_array_length(t.input_snapshot->'eventIds')
		FROM agent_turns t JOIN agent_threads th ON th.thread_id=t.thread_id WHERE t.turn_id=$1`, payloadRef).
		Scan(&clusterID, &sourceVersion, &pinnedAsset, &pinnedEvents); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(clusterID, "clu_") || sourceVersion != 1 || pinnedAsset != assetID || pinnedEvents != 1 {
		t.Fatalf("turn source=%q version=%d asset=%q events=%d", clusterID, sourceVersion, pinnedAsset, pinnedEvents)
	}
	var liveVersion int64
	var representatives int
	if err := st.Pool().QueryRow(ctx, `SELECT version, jsonb_array_length(event_ids)
		FROM triage_clusters WHERE cluster_id=$1`, clusterID).Scan(&liveVersion, &representatives); err != nil {
		t.Fatal(err)
	}
	if liveVersion != 5 || representatives != 5 {
		t.Fatalf("cluster version=%d representatives=%d", liveVersion, representatives)
	}
}

func productionTriageEvent(id, assetID, rule string, coverage commonv1.CoverageStatus) *eventv1.Event {
	return &eventv1.Event{
		Id: id, AssetId: assetID, OccurredAt: timestamppb.Now(),
		Kind: eventv1.Kind_KIND_TRAFFIC, Verdict: eventv1.Verdict_VERDICT_ALLOW,
		TriageReason: commonv1.TriageReason_TRIAGE_REASON_DETECTED_UNMITIGATED,
		Traffic:      &eventv1.Event_Http{Http: &eventv1.Http{Method: "GET", Path: "/api/items", QueryRedacted: "id=redacted"}},
		Detections: []*eventv1.Detection{{
			RuleId: rule,
			Key:    &commonv1.DetectionKey{RuleId: rule, TargetSelector: "query.id"},
		}},
		Coverage: []*commonv1.InspectionCoverage{{
			Target: commonv1.InspectionSurface_INSPECTION_SURFACE_QUERY, Status: coverage,
		}},
	}
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var buffer [12]byte
	position := len(buffer)
	for value > 0 {
		position--
		buffer[position] = byte('0' + value%10)
		value /= 10
	}
	return string(buffer[position:])
}
