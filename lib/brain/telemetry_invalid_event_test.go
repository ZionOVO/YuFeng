package brain

import (
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "yufeng/proto/gen/commonv1"
	eventv1 "yufeng/proto/gen/eventv1"
	telemetryv1 "yufeng/proto/gen/telemetryv1"
)

func TestUploadEventsRejectsNullCharacterWithoutPoisoningBatch(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	unitID, assetID, token := seedUnitAsset(t, ctx, st, "event-null-character")
	now := timestamppb.Now()
	poisonedID := "event-null-character-poisoned-" + newTestSuffix()
	acceptedID := "event-null-character-accepted-" + newTestSuffix()
	event := func(id, selector string) *eventv1.Event {
		return &eventv1.Event{
			Id: id, OccurredAt: now, UnitId: unitID, AssetId: assetID, Source: "yufeng-edge",
			Kind: eventv1.Kind_KIND_TRAFFIC, Verdict: eventv1.Verdict_VERDICT_OBSERVE,
			Traffic: &eventv1.Event_Http{Http: &eventv1.Http{Method: "POST", Path: "/dataset"}},
			Detections: []*eventv1.Detection{{
				DetectorId: "crs", RuleId: "941100",
				Key: &commonv1.DetectionKey{DetectorId: "crs", RuleId: "941100", TargetSelector: selector},
			}},
		}
	}
	req := connect.NewRequest(&telemetryv1.UploadEventsRequest{Events: []*eventv1.Event{
		event(poisonedID, "arg.user\x00name"),
		event(acceptedID, "arg.user"),
	}})
	req.Header().Set("Authorization", "Bearer "+token)
	response, err := NewTelemetryServer(st.Pool(), nil, nil, "").UploadEvents(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if response.Msg.GetAccepted() != 1 || response.Msg.GetDeduped() != 0 || len(response.Msg.GetRejected()) != 1 {
		t.Fatalf("upload accounting=%v", response.Msg)
	}
	rejected := response.Msg.GetRejected()[0]
	if rejected.GetEventId() != poisonedID || rejected.GetCode() != "invalid_event" {
		t.Fatalf("rejected event=%v", rejected)
	}
	var accepted, poisoned int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM events WHERE event_id=$1`, acceptedID).Scan(&accepted); err != nil {
		t.Fatal(err)
	}
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM events WHERE event_id=$1`, poisonedID).Scan(&poisoned); err != nil {
		t.Fatal(err)
	}
	if accepted != 1 || poisoned != 0 {
		t.Fatalf("stored accepted=%d poisoned=%d", accepted, poisoned)
	}
}
