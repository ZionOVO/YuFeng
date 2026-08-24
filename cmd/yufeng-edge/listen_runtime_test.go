package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"yufeng/lib/edgecore"
	"yufeng/lib/kernel"

	artifactv1 "yufeng/proto/gen/artifactv1"
	commonv1 "yufeng/proto/gen/commonv1"
	modelsidev1 "yufeng/proto/gen/modelsidev1"
	unitv1 "yufeng/proto/gen/unitv1"
)

type blockingModelTrafficSender struct {
	started chan struct{}
	release chan struct{}
}

func (s *blockingModelTrafficSender) SubmitTraffic(ctx context.Context, batch *edgecore.ModelIngressBatch) (uint32, error) {
	s.started <- struct{}{}
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-s.release:
		return uint32(len(batch.Traffic)), nil
	}
}

func TestModelIngressStartsExactlyTheBudgetedSenderCount(t *testing.T) {
	sender := &blockingModelTrafficSender{started: make(chan struct{}, kernel.ModelSideIngressWorkers+2), release: make(chan struct{})}
	runtime := &edgeRuntime{
		modelQueue:   edgecore.NewModelIngressQueue(),
		modelSender:  sender,
		observations: make(chan edgeObservation),
	}
	for index := 0; index < kernel.ModelSideIngressWorkers+1; index++ {
		if !runtime.modelQueue.Offer(&edgecore.ModelIngressItem{
			Profile: kernel.DefaultModelProfile(),
			Traffic: &modelsidev1.NormalizedTraffic{RequestId: "request", ModelProfileDigest: fmt.Sprintf("profile-%d", index)},
		}) {
			t.Fatal("model ingress fixture must fit the queue")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		close(sender.release)
	})
	runtime.startBackground(ctx)
	for range kernel.ModelSideIngressWorkers {
		select {
		case <-sender.started:
		case <-time.After(time.Second):
			t.Fatal("budgeted model ingress sender did not start")
		}
	}
	select {
	case <-sender.started:
		t.Fatalf("model ingress started more than %d senders", kernel.ModelSideIngressWorkers)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestEdgeBindsOnlyAfterVerifiedListenPlan(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	set := edgecore.NewReleaseSet()
	if _, err := newEdgeRuntime(set, "local-1", "asset-1", nil, edgecore.SourcePseudonymizer{}, nil, nil); err == nil {
		t.Fatal("edge must not bind without a verified listen plan")
	}

	wrongUnit := signedRuntimeListenPlan(t, priv, "other-unit", 1, ":18080")
	if err := set.ApplyListenPlan(wrongUnit, pub, "local-1"); err == nil {
		t.Fatal("wrong-unit plan must be rejected")
	}
	tampered := signedRuntimeListenPlan(t, priv, "local-1", 1, ":18080")
	tampered.UpstreamUrl = "http://tampered:8080"
	if err := set.ApplyListenPlan(tampered, pub, "local-1"); err == nil {
		t.Fatal("tampered plan must be rejected")
	}

	plan := signedRuntimeListenPlan(t, priv, "local-1", 1, ":18080")
	if err := set.ApplyListenPlan(plan, pub, "local-1"); err != nil {
		t.Fatal(err)
	}
	runtime, err := newEdgeRuntime(set, "local-1", "asset-1", nil, edgecore.SourcePseudonymizer{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.plan().GetUpstreamUrl() != "http://app:8080" {
		t.Fatalf("runtime plan=%v", runtime.plan())
	}
}

func TestEdgeReadyProbeDoesNotEnterBusinessHandler(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	set := edgecore.NewReleaseSet()
	plan := signedRuntimeListenPlan(t, priv, "local-1", 1, ":18080")
	if err := set.ApplyListenPlan(plan, pub, "local-1"); err != nil {
		t.Fatal(err)
	}
	runtime, err := newEdgeRuntime(set, "local-1", "asset-1", nil, edgecore.SourcePseudonymizer{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	var businessCalls atomic.Int64
	runtime.current.Store(&edgeBinding{
		plan: plan,
		handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			businessCalls.Add(1)
		}),
	})
	probe := httptest.NewRecorder()
	edgeAdminHandler(runtime).ServeHTTP(probe, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if probe.Code != http.StatusServiceUnavailable {
		t.Fatalf("plan without generation status=%d", probe.Code)
	}
	if businessCalls.Load() != 0 {
		t.Fatal("management probe entered business handler")
	}

	installRuntimeGeneration(t, set, priv)
	probe = httptest.NewRecorder()
	edgeAdminHandler(runtime).ServeHTTP(probe, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if probe.Code != http.StatusOK {
		t.Fatalf("ready status=%d body=%s", probe.Code, probe.Body.String())
	}
	if businessCalls.Load() != 0 {
		t.Fatal("ready probe produced business traffic")
	}
}

func TestListenPlanUpdateIsMonotonicAndRebindsByRestart(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	set := edgecore.NewReleaseSet()
	first := signedRuntimeListenPlan(t, priv, "local-1", 1, ":18080")
	if err := set.ApplyListenPlan(first, pub, "local-1"); err != nil {
		t.Fatal(err)
	}
	runtime, err := newEdgeRuntime(set, "local-1", "asset-1", nil, edgecore.SourcePseudonymizer{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	second := signedRuntimeListenPlan(t, priv, "local-1", 2, ":18080")
	second.UpstreamUrl = "http://app-v2:8080"
	second.Signature = nil
	if err := kernel.SignUnitListenPlan(second, priv); err != nil {
		t.Fatal(err)
	}
	if err := runtime.applyPlan(second, pub, t.TempDir()+"/listen.json"); err != nil {
		t.Fatalf("same-address update: %v", err)
	}
	if runtime.plan().GetUpstreamUrl() != "http://app-v2:8080" {
		t.Fatal("handler did not switch to new plan")
	}
	if err := runtime.applyPlan(first, pub, t.TempDir()+"/listen.json"); err == nil {
		t.Fatal("listen plan version must not move backward")
	}

	third := signedRuntimeListenPlan(t, priv, "local-1", 3, ":18081")
	if err := runtime.applyPlan(third, pub, t.TempDir()+"/listen.json"); err != errListenAddressChanged {
		t.Fatalf("address change want restart signal, got %v", err)
	}
}

func TestListenPlanUpdateReconfiguresTheEdgeModelIngressWindow(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	set := edgecore.NewReleaseSet()
	first := signedRuntimeListenPlan(t, priv, "local-1", 1, ":18080")
	if err := set.ApplyListenPlan(first, pub, "local-1"); err != nil {
		t.Fatal(err)
	}
	hard := &artifactv1.ModelIngressWindow{MaxItems: 2048, MaxRetainedBytes: 64 << 20, MaxQueueAge: durationpb.New(time.Second)}
	runtime, err := newEdgeRuntime(set, "local-1", "asset-1", nil, edgecore.SourcePseudonymizer{}, &blockingModelTrafficSender{}, hard)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot := runtime.modelQueue.Snapshot(); snapshot.State != unitv1.ModelIngressWindowState_MODEL_INGRESS_WINDOW_STATE_DEGRADED {
		t.Fatalf("initial snapshot=%+v", snapshot)
	}

	second := signedRuntimeListenPlan(t, priv, "local-1", 2, ":18080")
	second.ModelIngressWindow = &artifactv1.ModelIngressWindow{MaxItems: 1024, MaxRetainedBytes: 32 << 20, MaxQueueAge: durationpb.New(500 * time.Millisecond)}
	second.Signature = nil
	if err := kernel.SignUnitListenPlan(second, priv); err != nil {
		t.Fatal(err)
	}
	if err := runtime.applyPlan(second, pub, t.TempDir()+"/listen.json"); err != nil {
		t.Fatal(err)
	}
	snapshot := runtime.modelQueue.Snapshot()
	if snapshot.State != unitv1.ModelIngressWindowState_MODEL_INGRESS_WINDOW_STATE_APPLIED ||
		!kernel.EqualModelIngressWindow(snapshot.Desired, second.GetModelIngressWindow()) ||
		!kernel.EqualModelIngressWindow(snapshot.Effective, second.GetModelIngressWindow()) {
		t.Fatalf("updated snapshot=%+v", snapshot)
	}
}

func signedRuntimeListenPlan(t *testing.T, priv ed25519.PrivateKey, unitID string, version uint64, address string) *artifactv1.UnitListenPlan {
	t.Helper()
	plan := &artifactv1.UnitListenPlan{
		UnitId: unitID, Version: version, Posture: commonv1.IngressPosture_INGRESS_POSTURE_REVERSE_PROXY,
		TrafficKey: "site-a", ListenAddress: address, UpstreamUrl: "http://app:8080",
		ModelIngressWindow: kernel.DefaultModelIngressWindow(),
	}
	if err := kernel.SignUnitListenPlan(plan, priv); err != nil {
		t.Fatal(err)
	}
	return plan
}

func installRuntimeGeneration(t *testing.T, set *edgecore.ReleaseSet, priv ed25519.PrivateKey) {
	t.Helper()
	payload, err := edgecore.MarshalRules([]edgecore.Rule{{ID: "runtime-test", Pattern: "blocked"}})
	if err != nil {
		t.Fatal(err)
	}
	artifact := &artifactv1.Artifact{
		Kind: artifactv1.Kind_KIND_RULE, Payload: payload, PayloadSchema: edgecore.RulePayloadSchema,
		Ttl: durationpb.New(time.Hour), CreatedAt: timestamppb.Now(), CreatedBy: "test",
	}
	if err := kernel.SignArtifact(artifact, priv); err != nil {
		t.Fatal(err)
	}
	if err := set.ApplyGeneration(signedCacheGeneration(t, priv, "gen-runtime", "", 1, artifact), priv.Public().(ed25519.PublicKey)); err != nil {
		t.Fatal(err)
	}
}
