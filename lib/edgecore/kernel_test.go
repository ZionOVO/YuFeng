package edgecore

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"yufeng/lib/kernel"
	artifactv1 "yufeng/proto/gen/artifactv1"
	commonv1 "yufeng/proto/gen/commonv1"
)

// silentEye 只出发现，故意不提供 Action——新眼睛不能单靠接口 403。
type silentEye struct{ hits []Detection }

func (s silentEye) ID() string { return "silent-eye" }
func (s silentEye) Inspect(context.Context, InspectionInput) (Inspection, error) {
	return Inspection{Detections: s.hits}, nil
}

func TestNewInspectorCannotBlockWithoutPolicy(t *testing.T) {
	RegisterInspector("silent-eye", func(*artifactv1.DetectorManifest) (Inspector, error) {
		return silentEye{hits: []Detection{{InspectorID: "silent-eye", RuleID: "x1", Location: commonv1.InspectionSurface_INSPECTION_SURFACE_QUERY}}}, nil
	})
	pub, priv, err := newKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	set := NewReleaseSet()
	a, err := SignNamedDetectorManifest(priv, "silent-eye")
	if err != nil {
		t.Fatal(err)
	}
	if err := set.Apply(&artifactv1.ReleaseItem{ReleaseId: "rel-eye", Artifact: a}, pub); err != nil {
		t.Fatal(err)
	}
	req := Request{Method: "GET", Path: "/api", Query: "id=1"}
	dec := set.Check(context.Background(), req, "r1")
	if dec.Action != ActionAllow {
		t.Fatalf("inspector hit without policy must allow, got %v", dec.Action)
	}
	if len(dec.Detections) == 0 {
		t.Fatal("inspector must still emit detections")
	}
}

func TestUnloadedOptionalProfileInspectorProducesNoDetection(t *testing.T) {
	if _, registered := LookupInspector("profile"); registered {
		t.Fatal("optional profile inspector must not be registered without a signed implementation")
	}
	pub, priv, err := newKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	set := NewReleaseSet()
	if err := InstallSignedCRS(set, pub, priv); err != nil {
		t.Fatal(err)
	}
	decision := set.Check(context.Background(), Request{Method: "GET", Path: "/api/items", Query: "page=2"}, "req-profile-absent")
	for _, detection := range decision.Detections {
		if detection.InspectorID == "profile" {
			t.Fatalf("unloaded optional inspector emitted detection: %+v", detection)
		}
	}
}

func TestReplaySameViewSameGeneration(t *testing.T) {
	pub, priv, err := newKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	set := NewReleaseSet()
	if err := InstallSignedCRS(set, pub, priv); err != nil {
		t.Fatal(err)
	}
	view := Canonicalize("GET", "/api/items", "id=1+UNION+SELECT+pw", nil, nil, DefaultInspectionProfile())
	a, actA := set.Replay(view)
	b, actB := set.Replay(view)
	if actA != actB {
		t.Fatalf("gate action drifted %v vs %v", actA, actB)
	}
	if len(a.Detections) != len(b.Detections) {
		t.Fatalf("detection set drifted %#v vs %#v", a.Detections, b.Detections)
	}
	if len(a.Coverage) != len(b.Coverage) {
		t.Fatalf("coverage drifted")
	}
	if actA != ActionAllow {
		t.Fatal("crs hit without policy must allow")
	}
	if len(a.Detections) == 0 {
		t.Fatal("crs should detect union select")
	}
}

func TestObserveShellNever403(t *testing.T) {
	pub, priv, err := newKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	set := NewReleaseSet()
	set.SetPosture(commonv1.IngressPosture_INGRESS_POSTURE_TAP_ALERT)
	payload, _ := protojson.Marshal(&artifactv1.PolicyCandidate{
		Action: "block",
		Predicate: &artifactv1.PolicyPredicate{
			DetectionKeys: []*commonv1.DetectionKey{{RuleId: "942100", TargetLocation: commonv1.InspectionSurface_INSPECTION_SURFACE_QUERY}},
		},
	})
	a := &artifactv1.Artifact{
		Kind: artifactv1.Kind_KIND_POLICY, Payload: payload, PayloadSchema: PolicyPayloadSchema,
		Ttl: durationpb.New(time.Hour), CreatedAt: timestamppb.Now(),
	}
	if err := kernel.SignArtifact(a, priv); err != nil {
		t.Fatal(err)
	}
	if err := set.Apply(&artifactv1.ReleaseItem{ReleaseId: "rel-p", Artifact: a, Mode: commonv1.ReleaseMode_RELEASE_MODE_ENFORCE}, pub); err != nil {
		t.Fatal(err)
	}
	found := []Detection{{RuleID: "942100", Location: commonv1.InspectionSurface_INSPECTION_SURFACE_QUERY}}
	view := Canonicalize("GET", "/api", "id=1", nil, nil, DefaultInspectionProfile())
	dec := set.Gate(context.Background(), Request{Path: "/api", Query: "id=1"}, "r", found, view)
	if dec.Action != ActionAllow {
		t.Fatalf("tap must allow, got %v", dec.Action)
	}
	if !dec.WouldHaveBlocked {
		t.Fatal("tap must report would_have_blocked")
	}
}

func TestHTTPStatusOversizeAndRejected(t *testing.T) {
	view := CanonicalView{Rejected: true}
	code, _ := HTTPStatus(StatusInput{Posture: commonv1.IngressPosture_INGRESS_POSTURE_REVERSE_PROXY, View: view})
	if code != http.StatusBadRequest {
		t.Fatalf("rejected reverse proxy want 400 got %d", code)
	}
	code, _ = HTTPStatus(StatusInput{Posture: commonv1.IngressPosture_INGRESS_POSTURE_REVERSE_PROXY, Oversize: true})
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize reverse proxy want 413 got %d", code)
	}
	code, _ = HTTPStatus(StatusInput{Posture: commonv1.IngressPosture_INGRESS_POSTURE_EXT_AUTHZ, Oversize: true, BodyPresent: true})
	if code != http.StatusForbidden {
		t.Fatalf("oversize ext_authz want 403 got %d", code)
	}
	code, would := HTTPStatus(StatusInput{
		Posture:    commonv1.IngressPosture_INGRESS_POSTURE_TAP_ALERT,
		GateAction: ActionBlock, Oversize: true, EngineCrash: true, MissingRequestID: true,
	})
	if code != http.StatusOK || !would {
		t.Fatalf("observe shell want 200 would_block, got %d would=%v", code, would)
	}
}

func TestListenPlanRejectsTwoInterceptUnits(t *testing.T) {
	err := ValidateListenPlans([]*artifactv1.UnitListenPlan{
		{UnitId: "a", Posture: commonv1.IngressPosture_INGRESS_POSTURE_REVERSE_PROXY, TrafficKey: "k"},
		{UnitId: "b", Posture: commonv1.IngressPosture_INGRESS_POSTURE_EXT_AUTHZ, TrafficKey: "k"},
	})
	if err == nil {
		t.Fatal("two intercept units on same traffic key must fail")
	}
}

func TestListenPlanRejectsTwoPosturesForOneUnit(t *testing.T) {
	err := ValidateListenPlans([]*artifactv1.UnitListenPlan{
		{UnitId: "a", Posture: commonv1.IngressPosture_INGRESS_POSTURE_REVERSE_PROXY, TrafficKey: "k"},
		{UnitId: "a", Posture: commonv1.IngressPosture_INGRESS_POSTURE_TAP_ALERT, TrafficKey: "k"},
	})
	if err == nil {
		t.Fatal("one unit with two postures must fail")
	}
}

func TestTapSilentAndSkew(t *testing.T) {
	health := EvaluateTapHealth([]TapWindow{
		{UnitID: "edge", Posture: commonv1.IngressPosture_INGRESS_POSTURE_REVERSE_PROXY, TrafficKey: "k", WindowReqs: 10, TotalRequests: 10},
		{UnitID: "tap", Posture: commonv1.IngressPosture_INGRESS_POSTURE_TAP_ALERT, TrafficKey: "k", WindowReqs: 0, PrevWindowReqs: 0},
	})
	if health["tap"] != commonv1.UnitHealth_UNIT_HEALTH_TAP_SILENT {
		t.Fatalf("tap silent got %v", health["tap"])
	}
	health = EvaluateTapHealth([]TapWindow{
		{UnitID: "edge", Posture: commonv1.IngressPosture_INGRESS_POSTURE_REVERSE_PROXY, TrafficKey: "k", FollowUnitID: "", Routes: []string{"GET /a", "GET /b"}, TotalRequests: 100, WindowReqs: 100},
		{UnitID: "tap", Posture: commonv1.IngressPosture_INGRESS_POSTURE_TAP_ALERT, TrafficKey: "k", FollowUnitID: "edge", Routes: []string{"POST /z"}, TotalRequests: 100, WindowReqs: 100},
	})
	if health["tap"] != commonv1.UnitHealth_UNIT_HEALTH_TAP_SKEW {
		t.Fatalf("tap skew got %v", health["tap"])
	}
}

func TestProxyOversizeReturns413(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("oversize must not reach upstream")
	}))
	t.Cleanup(upstream.Close)
	p := NewProxy(NewEngine(), commonv1.ReleaseMode_RELEASE_MODE_ENFORCE, nil, mustParseURL(t, upstream.URL), "a")
	ts := httptest.NewServer(p)
	t.Cleanup(ts.Close)
	body := strings.Repeat("x", kernel.EngineBodyLimitBytes+1)
	resp, err := http.Post(ts.URL, "application/octet-stream", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("want 413 got %d", resp.StatusCode)
	}
}

func TestEvidenceRingEvictsByCapacity(t *testing.T) {
	r := NewEvidenceRing()
	r.maxEntries = 2
	r.maxBytes = 64
	now := time.Now()
	r.Put("a", []byte("one"), now)
	r.Put("b", []byte("two"), now)
	r.Put("c", []byte("three"), now)
	if _, ok := r.Get("a", now); ok {
		t.Fatal("oldest entry must be evicted")
	}
	if _, ok := r.Get("c", now); !ok {
		t.Fatal("newest entry must remain")
	}
}

func TestModelIngressDropOldestCanBeForced(t *testing.T) {
	q := newModelIngressTestQueue(t, modelIngressTestWindow(1, 1<<20, time.Minute))
	configureModelIngressTestQueue(t, q, modelIngressTestWindow(1, 1<<20, time.Minute))
	if !q.Offer(modelIngressTestItem("1", "profile-a", []byte("x"))) {
		t.Fatal("first offer must succeed")
	}
	if !q.Offer(modelIngressTestItem("2", "profile-a", []byte("x"))) {
		t.Fatal("full window must retain the newest model ingress")
	}
	if q.Dropped() != 1 {
		t.Fatalf("dropped=%d", q.Dropped())
	}
	q.DropOldest()
	if q.Dropped() != 2 {
		t.Fatalf("drop oldest want 2 got %d", q.Dropped())
	}
	if q.Depth() != 0 || q.QueuedBodyBytes() != 0 {
		t.Fatalf("drained depth=%d bytes=%d", q.Depth(), q.QueuedBodyBytes())
	}
}

func TestTrafficEventCarriesCoverageAndPosture(t *testing.T) {
	ev := TrafficEvent("u1", "a1", "rid", Request{Method: "GET", Path: "/api"}, Decision{
		Action:           ActionAllow,
		WouldHaveBlocked: true,
		Posture:          commonv1.IngressPosture_INGRESS_POSTURE_TAP_ALERT,
		Detections:       []Detection{{InspectorID: "crs", RuleID: "942100"}},
		Inspection: Inspection{Coverage: []Coverage{{
			Target: commonv1.InspectionSurface_INSPECTION_SURFACE_QUERY,
			Status: commonv1.CoverageStatus_COVERAGE_STATUS_FULL,
		}}},
	}, SourcePseudonymizer{})
	if !ev.WouldHaveBlocked || ev.IngressPosture != commonv1.IngressPosture_INGRESS_POSTURE_TAP_ALERT {
		t.Fatalf("event posture/would: %+v", ev)
	}
	if len(ev.Coverage) != 1 || ev.Coverage[0].Status != commonv1.CoverageStatus_COVERAGE_STATUS_FULL {
		t.Fatalf("event coverage: %+v", ev.Coverage)
	}
	if len(ev.Detections) != 1 || ev.Detections[0].DetectorId != "crs" {
		t.Fatalf("event detections: %+v", ev.Detections)
	}
}

func TestObserveInFlightIsOK(t *testing.T) {
	code, _ := HTTPStatus(StatusInput{
		Posture:          commonv1.IngressPosture_INGRESS_POSTURE_TAP_ALERT,
		InFlightExceeded: true,
	})
	if code != http.StatusOK {
		t.Fatalf("observe inflight want 200 got %d", code)
	}
}
