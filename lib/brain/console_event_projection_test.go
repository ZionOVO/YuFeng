package brain

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "yufeng/proto/gen/commonv1"
	consolev1 "yufeng/proto/gen/consolev1"
	eventv1 "yufeng/proto/gen/eventv1"
)

func TestConsoleEventProjectsModelInferenceAndJarvisDeliveryWithinAssetScope(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	h := newOnboardHarness(t, st)
	if err := SeedOnboardingState(ctx, st.Pool(), OnboardingStateCompleted, h.local); err != nil {
		t.Fatal(err)
	}
	unitID, assetID, _ := seedUnitAsset(t, ctx, st, "console-model")
	var generationID string
	var generationSequence int64
	if err := st.Pool().QueryRow(ctx, `SELECT generation_id,generation_seq FROM asset_generations
		WHERE asset_id=$1 ORDER BY generation_seq DESC LIMIT 1`, assetID).Scan(&generationID, &generationSequence); err != nil {
		t.Fatal(err)
	}
	eventID := "event-model-console-" + newTestSuffix()
	requestID := "request-model-console-" + newTestSuffix()
	caseID := "case-model-console-" + newTestSuffix()
	clusterID := "cluster-model-console-" + newTestSuffix()
	threadID := "thread-model-console-" + newTestSuffix()
	turnID := "turn-model-console-" + newTestSuffix()
	instructionID := "instruction-model-console-" + newTestSuffix()
	now := time.Now().UTC().Truncate(time.Microsecond)
	event := &eventv1.Event{
		Id: eventID, OccurredAt: timestamppb.New(now), AssetId: assetID, UnitId: unitID,
		RequestId: requestID, Source: "yufeng-modelside", Kind: eventv1.Kind_KIND_MODEL_ALERT,
		Verdict: eventv1.Verdict_VERDICT_ESCALATE, GenerationId: generationID, GenerationSeq: generationSequence,
		TriageReason: commonv1.TriageReason_TRIAGE_REASON_SUSPECTED_MISS,
		Traffic:      &eventv1.Event_Http{Http: &eventv1.Http{Method: "POST", Path: "/login"}},
	}
	eventRaw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO event_receipts(event_id,payload_digest,occurred_at) VALUES($1,$2,$3)`, eventID, "sha256:event", now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO events(event_id,unit_id,asset_id,request_id,occurred_at,source,kind,verdict,payload,generation_seq,payload_digest)
		VALUES($1,$2,$3,$4,$5,'yufeng-modelside','model_alert','escalate',$6::jsonb,$7,'sha256:event')`,
		eventID, unitID, assetID, requestID, now, eventRaw, generationSequence); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO model_inferences(inference_id,event_id,model_group,model_type,model_version,
		threshold,score,attack_class,taxonomy_version,recorded_at,model_profile_digest,request_id,result_kind)
		VALUES($1,$2,'http-threat','PVM','gpvm-e9eceef3',0.9,0.97,'ATTACK_CLASS_SQLI','taxonomy/v1',$3,'sha256:profile',$4,'MODEL_ALERT')`,
		"inference-model-console-"+newTestSuffix(), eventID, now, requestID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO investigation_cases(case_id,module_id,asset_id,state,priority,title)
		VALUES($1,'traffic-interception',$2,'open',97,'模型告警案件')`, caseID, assetID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO triage_clusters(
		cluster_id,asset_id,route_template,method,identity_key,reason,event_ids,representative)
		VALUES($1,$2,'/login','POST',$3,'TRIAGE_REASON_SUSPECTED_MISS',$4::jsonb,$5)`,
		clusterID, assetID, "identity-"+clusterID, `["`+eventID+`"]`, eventID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO agent_threads(thread_id,source_kind,source_ref,agent_id)
		VALUES($1,$2,$3,'jarvis-console')`, threadID, threadSourceTriage, clusterID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO agent_turns(turn_id,thread_id,source_version,input_snapshot)
		VALUES($1,$2,1,'{}'::jsonb)`, turnID, threadID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO model_result_receipts(result_id,payload_digest,modelside_id,request_id,
		unit_id,asset_id,generation_id,generation_seq,model_profile_digest,result_kind,score,method,route,event_id,case_id,occurred_at)
		VALUES($1,'sha256:result','modelside-console',$2,$3,$4,$5,$6,'sha256:profile','MODEL_ALERT',0.97,'POST','/login',$7,$8,$9)`,
		"result-model-console-"+newTestSuffix(), requestID, unitID, assetID, generationID, generationSequence, eventID, caseID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO agent_instructions(
		instruction_id,agent_id,kind,payload_ref,turn_id,status,created_at,acked_at)
		VALUES($1,'jarvis-console','EVENT_TRIAGE',$2,$2,'acked',$3,$3)`, instructionID, turnID, now); err != nil {
		t.Fatal(err)
	}

	console := NewConsoleServer(st.Pool())
	if err := writeAdminSystemGrant(ctx, st.Pool(), h.adminID, h.local); err != nil {
		t.Fatal(err)
	}
	if _, err := console.GetEvent(ctx, bearerReq(h.adminTok, &consolev1.GetEventRequest{EventId: eventID})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("event outside asset scope want permission_denied, got %v", err)
	}
	if err := writeAdminSystemGrant(ctx, st.Pool(), h.adminID, assetID); err != nil {
		t.Fatal(err)
	}
	response, err := console.GetEvent(ctx, bearerReq(h.adminTok, &consolev1.GetEventRequest{EventId: eventID}))
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Msg.GetModelInferences()) != 1 {
		t.Fatalf("model inferences=%v", response.Msg.GetModelInferences())
	}
	inference := response.Msg.GetModelInferences()[0]
	if inference.GetModelVersion() != "gpvm-e9eceef3" || inference.GetScore() != 0.97 ||
		inference.GetAttackClass() != commonv1.AttackClass_ATTACK_CLASS_SQLI || inference.GetResultKind() != "MODEL_ALERT" {
		t.Fatalf("model inference projection=%v", inference)
	}
	if len(response.Msg.GetTriageDeliveries()) != 1 {
		t.Fatalf("triage deliveries=%v", response.Msg.GetTriageDeliveries())
	}
	delivery := response.Msg.GetTriageDeliveries()[0]
	if delivery.GetCaseId() != caseID || delivery.GetInstructionId() != instructionID ||
		delivery.GetHandlerId() != "jarvis-console" || delivery.GetStatus() != "acked" || delivery.GetAcknowledgedAt() == nil {
		t.Fatalf("triage delivery projection=%v", delivery)
	}
	dashboard, err := console.Dashboard(ctx, bearerReq(h.adminTok, &consolev1.DashboardRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if dashboard.Msg.GetModelAlerts_24H() != 1 {
		t.Fatalf("model alerts in 24h=%d", dashboard.Msg.GetModelAlerts_24H())
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE grants SET tools='[]'::jsonb WHERE subject_kind='user' AND subject_id=$1 AND created_by='system'`, h.adminID); err != nil {
		t.Fatal(err)
	}
	if _, err := console.GetEvent(ctx, bearerReq(h.adminTok, &consolev1.GetEventRequest{EventId: eventID})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("event without console.read want permission_denied, got %v", err)
	}
}
