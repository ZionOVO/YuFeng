package brain

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	"yufeng/lib/edgecore"
	"yufeng/lib/store"
	artifactv1 "yufeng/proto/gen/artifactv1"
	commonv1 "yufeng/proto/gen/commonv1"
	eventv1 "yufeng/proto/gen/eventv1"
	telemetryv1 "yufeng/proto/gen/telemetryv1"
)

func TestAcceptedEventFreezesPinnedTicketWithoutLegacyAnalysisDispatch(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	unitID, assetID, token := seedUnitAsset(t, ctx, st, "ticket-ready")
	generationID := seedRoutingGeneration(t, ctx, st, assetID, 2,
		commonv1.EvidenceDigestAlgorithm_EVIDENCE_DIGEST_ALGORITHM_SPAN_SHA256,
		commonv1.ForwardPolicyKind_FORWARD_POLICY_KIND_WORKER_SCORE)
	seedRoutingGeneration(t, ctx, st, assetID, 3,
		commonv1.EvidenceDigestAlgorithm_EVIDENCE_DIGEST_ALGORITHM_CHARSET_HIST,
		commonv1.ForwardPolicyKind_FORWARD_POLICY_KIND_NONE)

	eventID := "evt-ticket-ready-" + newTestSuffix()
	event := ticketEvent(eventID, unitID, assetID, generationID, 2)
	uploadEventForTicket(t, ctx, st, token, event)

	var status, rawTicket, digest, forward, reason string
	if err := st.Pool().QueryRow(ctx, `SELECT status, ticket::text, ticket_digest, forward_policy, quarantine_reason
		FROM check_tickets WHERE event_id=$1`, eventID).Scan(&status, &rawTicket, &digest, &forward, &reason); err != nil {
		t.Fatal(err)
	}
	if status != "ready" || reason != "" || forward != "FORWARD_POLICY_KIND_WORKER_SCORE" {
		t.Fatalf("status=%q forward=%q reason=%q", status, forward, reason)
	}
	var ticket eventv1.CheckTicket
	if err := protojson.Unmarshal([]byte(rawTicket), &ticket); err != nil {
		t.Fatal(err)
	}
	if ticket.GetGenerationId() != generationID || ticket.GetGenerationSeq() != 2 ||
		ticket.GetEvidence().GetAlgorithm() != commonv1.EvidenceDigestAlgorithm_EVIDENCE_DIGEST_ALGORITHM_SPAN_SHA256 {
		t.Fatalf("ticket did not retain pinned generation: %+v", &ticket)
	}
	if got, err := digestCheckTicket(&ticket); err != nil || got != digest {
		t.Fatalf("ticket digest=%q got=%q err=%v", digest, got, err)
	}
	var events, tickets, outbox, analysisWork int
	if err := st.Pool().QueryRow(ctx, `SELECT
		(SELECT count(*) FROM events WHERE event_id=$1),
		(SELECT count(*) FROM check_tickets WHERE event_id=$1),
		(SELECT count(*) FROM outbox WHERE dedupe_key=$1),
		(SELECT count(*) FROM analysis_work_items WHERE event_id=$1)`, eventID).
		Scan(&events, &tickets, &outbox, &analysisWork); err != nil {
		t.Fatal(err)
	}
	if events != 1 || tickets != 1 || outbox != 0 || analysisWork != 0 {
		t.Fatalf("events=%d tickets=%d outbox=%d analysis_work=%d", events, tickets, outbox, analysisWork)
	}
	uploadEventForTicket(t, ctx, st, token, event)
	if _, err := st.Pool().Exec(ctx, `UPDATE check_tickets SET ticket_digest='changed' WHERE event_id=$1`, eventID); err == nil {
		t.Fatal("frozen ticket must reject mutation")
	}
}

func TestAcceptedEventQuarantinesMissingProjectionMaterial(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	unitID, assetID, token := seedUnitAsset(t, ctx, st, "ticket-quarantine")
	var generationID string
	if err := st.Pool().QueryRow(ctx, `SELECT generation_id FROM asset_generations
		WHERE asset_id=$1 AND generation_seq=1`, assetID).Scan(&generationID); err != nil {
		t.Fatal(err)
	}

	eventID := "evt-ticket-quarantine-" + newTestSuffix()
	uploadEventForTicket(t, ctx, st, token, ticketEvent(eventID, unitID, assetID, generationID, 1))
	var status, reason string
	if err := st.Pool().QueryRow(ctx, `SELECT status, quarantine_reason FROM check_tickets WHERE event_id=$1`, eventID).
		Scan(&status, &reason); err != nil {
		t.Fatal(err)
	}
	if status != "quarantined" || reason != quarantineEvidenceDigestMissing {
		t.Fatalf("status=%q reason=%q", status, reason)
	}
}

func seedRoutingGeneration(t *testing.T, ctx context.Context, st *store.Store, assetID string, sequence int64, algorithm commonv1.EvidenceDigestAlgorithm, forward commonv1.ForwardPolicyKind) string {
	t.Helper()
	digestPayload, err := protojson.Marshal(&artifactv1.EvidenceDigest{
		Algorithm: algorithm, MaxSpanBytes: 128,
		Fields: []string{"method", "route_template", "span_hash"},
	})
	if err != nil {
		t.Fatal(err)
	}
	forwardPayload, err := protojson.Marshal(&artifactv1.ForwardPolicy{Kind: forward})
	if err != nil {
		t.Fatal(err)
	}
	generationID := "gen-route-" + newTestSuffix()
	generation := &artifactv1.AssetGeneration{
		GenerationId: generationID, AssetId: assetID, GenerationSeq: sequence,
		Members: []*artifactv1.ReleaseItem{
			{ReleaseId: "rel-digest-" + newTestSuffix(), AssetId: assetID, Artifact: &artifactv1.Artifact{
				Kind: artifactv1.Kind_KIND_EVIDENCE_DIGEST, PayloadSchema: edgecore.EvidenceDigestSchema, Payload: digestPayload,
			}},
			{ReleaseId: "rel-forward-" + newTestSuffix(), AssetId: assetID, Artifact: &artifactv1.Artifact{
				Kind: artifactv1.Kind_KIND_FORWARD_POLICY, PayloadSchema: edgecore.ForwardPolicySchema, Payload: forwardPayload,
			}},
		},
	}
	raw, err := protojson.Marshal(generation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO asset_generations(generation_id, asset_id, generation_seq, envelope, signed)
		VALUES($1,$2,$3,$4::jsonb,true)`, generationID, assetID, sequence, raw); err != nil {
		t.Fatal(err)
	}
	return generationID
}

func ticketEvent(eventID, unitID, assetID, generationID string, generationSequence int64) *eventv1.Event {
	return &eventv1.Event{
		Id: eventID, UnitId: unitID, AssetId: assetID, OccurredAt: timestamppb.Now(),
		Source: "test", Kind: eventv1.Kind_KIND_TRAFFIC, Verdict: eventv1.Verdict_VERDICT_ALLOW,
		GenerationId: generationID, GenerationSeq: generationSequence,
		IngressPosture: commonv1.IngressPosture_INGRESS_POSTURE_REVERSE_PROXY,
		Traffic: &eventv1.Event_Http{Http: &eventv1.Http{
			Method: "GET", Path: "/api/items", QueryRedacted: "id=redacted",
		}},
		Coverage: []*commonv1.InspectionCoverage{{
			Target: commonv1.InspectionSurface_INSPECTION_SURFACE_QUERY,
			Status: commonv1.CoverageStatus_COVERAGE_STATUS_FULL,
		}},
	}
}

func uploadEventForTicket(t *testing.T, ctx context.Context, st *store.Store, token string, event *eventv1.Event) {
	t.Helper()
	telemetry := NewTelemetryServer(st.Pool(), nil, nil, "")
	request := connect.NewRequest(&telemetryv1.UploadEventsRequest{Events: []*eventv1.Event{event}})
	request.Header().Set("Authorization", "Bearer "+token)
	response, err := telemetry.UploadEvents(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if response.Msg.Accepted+response.Msg.Deduped != 1 || len(response.Msg.Rejected) != 0 {
		t.Fatalf("upload response=%+v", response.Msg)
	}
}
