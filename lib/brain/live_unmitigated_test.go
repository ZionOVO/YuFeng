package brain

import (
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"connectrpc.com/connect"

	"yufeng/lib/edgecore"

	commonv1 "yufeng/proto/gen/commonv1"
	eventv1 "yufeng/proto/gen/eventv1"
	telemetryv1 "yufeng/proto/gen/telemetryv1"
	"yufeng/proto/gen/telemetryv1/telemetryv1connect"
)

// TestCRSHitUploadsDetectedUnmitigatedOnce 走已交付路径：
// ReleaseProxy + Coraza → TrafficEvent → UploadEvents → 一条 DETECTED_UNMITIGATED 指令。
func TestCRSHitUploadsDetectedUnmitigatedOnce(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	_, assetID, token := seedUnitAsset(t, ctx, st, "live")
	jarvis := "jarvis-live-" + newTestSuffix()
	if err := EnsureBootstrapJarvis(ctx, st.Pool(), jarvis); err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tel := NewTelemetryServer(st.Pool(), nil, NewAgentServer(st.Pool(), "boot", priv), jarvis)
	h, handler := telemetryv1connect.NewTelemetryServiceHandler(tel)
	mux := http.NewServeMux()
	mux.Handle(h, handler)
	brain := httptest.NewServer(mux)
	t.Cleanup(brain.Close)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(upstream.Close)
	up, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	var lastAttack edgecore.Decision
	set := edgecore.NewReleaseSet()
	if err := edgecore.InstallSignedCRS(set, pub, priv); err != nil {
		t.Fatal(err)
	}
	proxy := edgecore.NewReleaseProxy(set, nil, up, assetID)
	proxy.SetObserver(func(req edgecore.Request, dec edgecore.Decision, requestID string) {
		if len(dec.Detections) > 0 {
			lastAttack = dec
		}
		ev := edgecore.TrafficEvent("unit-live", assetID, requestID, req, dec, edgecore.SourcePseudonymizer{})
		client := telemetryv1connect.NewTelemetryServiceClient(brain.Client(), brain.URL)
		ureq := connect.NewRequest(&telemetryv1.UploadEventsRequest{Events: []*eventv1.Event{ev}})
		ureq.Header().Set("Authorization", "Bearer "+token)
		if _, err := client.UploadEvents(ctx, ureq); err != nil {
			t.Errorf("upload: %v", err)
		}
	})
	edge := httptest.NewServer(proxy)
	t.Cleanup(edge.Close)

	attack := edge.URL + "/api/items?id=1+UNION+SELECT+pw"
	normal := edge.URL + "/api/items?page=2"
	for i := 0; i < 2; i++ {
		resp, err := http.Get(attack)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("no-policy attack want 200 got %d", resp.StatusCode)
		}
		if err := resp.Body.Close(); err != nil {
			t.Fatal(err)
		}
	}
	resp, err := http.Get(normal)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("normal want 200 got %d", resp.StatusCode)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if len(lastAttack.Detections) == 0 {
		t.Fatal("attack must produce crs detections")
	}

	var nInstr int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM agent_instructions WHERE kind='EVENT_TRIAGE' AND agent_id=$1`, jarvis).Scan(&nInstr); err != nil {
		t.Fatal(err)
	}
	if nInstr != 1 {
		t.Fatalf("want one DETECTED_UNMITIGATED instruction, got %d", nInstr)
	}
	var ref, reason string
	if err := st.Pool().QueryRow(ctx, `SELECT i.payload_ref, e.payload->>'triageReason'
		FROM agent_instructions i
		JOIN events e ON e.asset_id=$1 AND jsonb_array_length(COALESCE(e.payload->'detections','[]'::jsonb)) > 0
		WHERE i.kind='EVENT_TRIAGE' AND i.agent_id=$2
		ORDER BY e.occurred_at DESC LIMIT 1`, assetID, jarvis).Scan(&ref, &reason); err != nil {
		t.Fatal(err)
	}
	if ref == "" || reason != commonv1.TriageReason_TRIAGE_REASON_DETECTED_UNMITIGATED.String() {
		t.Fatalf("payload_ref=%q triageReason=%q", ref, reason)
	}
	var benignQ int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM agent_instructions
		WHERE kind='EVENT_TRIAGE' AND payload_ref IN (
			SELECT event_id FROM events WHERE asset_id=$1 AND payload->'http'->>'queryRedacted' LIKE 'page=%'
		)`, assetID).Scan(&benignQ); err != nil {
		t.Fatal(err)
	}
	if benignQ != 0 {
		t.Fatal("ordinary no-detection must not enqueue")
	}
}
