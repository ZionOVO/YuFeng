package edgecore

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"

	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"
	"yufeng/lib/kernel"
	"yufeng/lib/observability"
	artifactv1 "yufeng/proto/gen/artifactv1"
)

func TestGenerationRejectsOlderWithoutRollback(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a := mustSignedRule(t, priv)
	store := &GenerationStore{}
	if err := store.Load(mustSignedGen(t, priv, "g2", "a1", 2, "r1", a), pub); err != nil {
		t.Fatal(err)
	}
	if err := store.Load(mustSignedGen(t, priv, "g1", "a1", 1, "r0", a), pub); err == nil {
		t.Fatal("older generation without rollback must fail")
	}
	if store.Current().GenerationSeq != 2 {
		t.Fatal("must keep current")
	}
}

func TestGenerationAcceptsOlderOnlyWithSignedRollbackLink(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a := mustSignedRule(t, priv)
	store := &GenerationStore{}
	current := mustSignedGen(t, priv, "g2", "a1", 2, "r2", a)
	if err := store.Load(current, pub); err != nil {
		t.Fatal(err)
	}
	rollback := mustSignedGen(t, priv, "g1-rollback", "a1", 1, "r1", a)
	rollback.ParentGenerationId = current.GenerationId
	rollback.RollbackOf = current.GenerationSeq
	rollback.EnvelopeSignature = nil
	if err := kernel.SignGeneration(rollback, priv); err != nil {
		t.Fatal(err)
	}
	if err := store.Load(rollback, pub); err != nil {
		t.Fatal(err)
	}
	if store.Current().GenerationSeq != 1 {
		t.Fatal("signed rollback must activate requested generation")
	}
}

func TestGenerationKeepsPreviousOnBadMember(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	good := mustSignedRule(t, priv)
	store := &GenerationStore{}
	if err := store.Load(mustSignedGen(t, priv, "g1", "a1", 1, "r1", good), pub); err != nil {
		t.Fatal(err)
	}
	bad := mustSignedRule(t, priv)
	if bad.Signature != nil {
		bad.Signature.Sig = []byte("deadbeef")
	}
	if err := store.Load(mustSignedGen(t, priv, "g2", "a1", 2, "r2", bad), pub); err == nil {
		t.Fatal("bad signature must fail")
	}
	if store.Current() == nil || store.Current().GenerationSeq != 1 {
		t.Fatal("must keep previous generation")
	}
	if store.LoadErrors == 0 {
		t.Fatal("load error metric must increase")
	}
	if observability.Default().Get(observability.MetricDetectorErrors) < 1 {
		t.Fatal("detector error prometheus counter")
	}
}

func TestGenerationDiskFullRefusesAndDegrades(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a := mustSignedRule(t, priv)
	store := &GenerationStore{}
	if err := store.Load(mustSignedGen(t, priv, "g1", "a1", 1, "r1", a), pub); err != nil {
		t.Fatal(err)
	}
	store.SetDiskFull(true)
	if err := store.Load(mustSignedGen(t, priv, "g2", "a1", 2, "r2", a), pub); err == nil {
		t.Fatal("disk full must refuse")
	}
	if !store.Degraded() || store.Current().GenerationSeq != 1 {
		t.Fatal("must keep previous and mark degraded")
	}
}

func TestGenerationNotBeforeClockSkew(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a := mustSignedRule(t, priv)
	store := &GenerationStore{}
	future := timestamppb.New(time.Now().Add(10 * time.Minute))
	g := &artifactv1.AssetGeneration{
		GenerationId: "g1", AssetId: "a1", GenerationSeq: 1, NotBefore: future,
		Members: []*artifactv1.ReleaseItem{{ReleaseId: "r1", Artifact: a}},
	}
	if err := kernel.SignGeneration(g, priv); err != nil {
		t.Fatal(err)
	}
	if err := store.Load(g, pub); err == nil {
		t.Fatal("future not_before beyond skew must wait")
	}
}

func TestGenerationRejectsSkip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a := mustSignedRule(t, priv)
	store := &GenerationStore{}
	if err := store.Load(mustSignedGen(t, priv, "g1", "a1", 1, "r1", a), pub); err != nil {
		t.Fatal(err)
	}
	if err := store.Load(mustSignedGen(t, priv, "g3", "a1", 3, "r3", a), pub); err == nil {
		t.Fatal("must not skip generation seq")
	}
}

func TestGenerationRejectsMemberWithoutReleaseID(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a := mustSignedRule(t, priv)
	store := &GenerationStore{}
	if err := store.Load(&artifactv1.AssetGeneration{
		GenerationId: "g1", AssetId: "a1", GenerationSeq: 1,
		Members: []*artifactv1.ReleaseItem{{Artifact: a}},
	}, pub); err == nil {
		t.Fatal("member without release_id must fail")
	}
}

func TestGenerationRejectsUnitListenPlan(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"unitId":"unit-a","posture":"INGRESS_POSTURE_REVERSE_PROXY","trafficKey":"site-a","version":"1","listenAddress":":18080","upstreamUrl":"http://app:8080"}`)
	art := &artifactv1.Artifact{
		Kind: artifactv1.Kind_KIND_LISTEN_PLAN, Payload: payload, PayloadSchema: ListenPlanSchema,
		CreatedAt: timestamppb.Now(), CreatedBy: "test",
	}
	if err := kernel.SignArtifact(art, priv); err != nil {
		t.Fatal(err)
	}
	gen := mustSignedGen(t, priv, "g1", "a1", 1, "listen", art)
	if err := NewReleaseSet().ApplyGeneration(gen, pub); err == nil {
		t.Fatal("asset generation containing unit listen plan must fail")
	}
}

func TestReleaseSetActivatesTrafficReviewPolicyFromGeneration(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	policy := DefaultTrafficReviewPolicy()
	policy.Mode = artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_EVIDENCE_ON_APPROVAL
	payload, err := protojson.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	artifact := &artifactv1.Artifact{
		Kind: artifactv1.Kind_KIND_TRAFFIC_REVIEW_POLICY, Payload: payload,
		PayloadSchema: TrafficReviewPolicySchema, CreatedAt: timestamppb.Now(), CreatedBy: "test",
	}
	if err := kernel.SignArtifact(artifact, priv); err != nil {
		t.Fatal(err)
	}
	set := NewReleaseSet()
	if err := set.ApplyGeneration(mustSignedGen(t, priv, "generation-review", "asset-1", 1, "review-policy", artifact), pub); err != nil {
		t.Fatal(err)
	}
	got := set.TrafficReviewPolicy()
	if got == nil || got.GetMode() != policy.GetMode() {
		t.Fatalf("activated policy=%v", got)
	}
	got.Mode = artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_OFF
	if set.TrafficReviewPolicy().GetMode() != policy.GetMode() {
		t.Fatal("caller mutation changed the active signed traffic review policy")
	}
	if set.TrafficReviewPolicyDigest() != artifact.GetId() {
		t.Fatalf("policy digest=%q want=%q", set.TrafficReviewPolicyDigest(), artifact.GetId())
	}
	current := set.CurrentGeneration()
	withoutReview := signedNextGeneration(t, priv, current, "generation-without-review", "rule", mustSignedRule(t, priv))
	if err := set.ApplyGeneration(withoutReview, pub); err != nil {
		t.Fatal(err)
	}
	if set.TrafficReviewPolicy() != nil || set.TrafficReviewPolicyDigest() != "" {
		t.Fatalf("generation without review policy retained stale state: policy=%v digest=%q", set.TrafficReviewPolicy(), set.TrafficReviewPolicyDigest())
	}
}

func TestReleaseSetPersistenceFailureKeepsCurrentGeneration(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a := mustSignedRule(t, priv)
	set := NewReleaseSet()
	first := mustSignedGen(t, priv, "g1", "a1", 1, "r1", a)
	if err := set.ApplyGeneration(first, pub); err != nil {
		t.Fatal(err)
	}
	second := mustSignedGen(t, priv, "g2", "a1", 2, "r2", a)
	second.ParentGenerationId = first.GenerationId
	second.EnvelopeSignature = nil
	if err := kernel.SignGeneration(second, priv); err != nil {
		t.Fatal(err)
	}
	err = set.ApplyGeneration(second, pub, func(_, _ *artifactv1.AssetGeneration) error {
		return errors.New("disk full")
	})
	if err == nil {
		t.Fatal("persistence failure must reject activation")
	}
	if set.CurrentGenerationSeq() != 1 {
		t.Fatalf("generation=%d", set.CurrentGenerationSeq())
	}
}

func TestReleaseSetInvalidGenerationKeepsCurrent(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	currentArtifact := mustSignedRule(t, priv)
	current := mustSignedGen(t, priv, "g2", "a1", 2, "r2", currentArtifact)

	tests := []struct {
		name  string
		build func(*testing.T) *artifactv1.AssetGeneration
	}{
		{
			name: "bad artifact signature",
			build: func(t *testing.T) *artifactv1.AssetGeneration {
				bad := mustSignedRule(t, priv)
				bad.Signature.Sig = []byte("invalid")
				return signedNextGeneration(t, priv, current, "g3-bad-signature", "r3", bad)
			},
		},
		{
			name: "backward sequence",
			build: func(t *testing.T) *artifactv1.AssetGeneration {
				return mustSignedGen(t, priv, "g1-backward", "a1", 1, "r1", mustSignedRule(t, priv))
			},
		},
		{
			name: "missing member artifact",
			build: func(t *testing.T) *artifactv1.AssetGeneration {
				gen := &artifactv1.AssetGeneration{
					GenerationId: "g3-missing-member", AssetId: "a1", GenerationSeq: 3,
					ParentGenerationId: current.GenerationId, MinEdgeVersion: kernel.MinimumEdgeVersion,
					Members: []*artifactv1.ReleaseItem{{ReleaseId: "r3"}},
				}
				if err := kernel.SignGeneration(gen, priv); err != nil {
					t.Fatal(err)
				}
				return gen
			},
		},
		{
			name: "shape compile failure",
			build: func(t *testing.T) *artifactv1.AssetGeneration {
				bad := &artifactv1.Artifact{
					Kind: artifactv1.Kind_KIND_SHAPE, PayloadSchema: "shape/v1",
					Payload:   []byte(`{"methods":["GET"],"pathPrefix":"/"}`),
					CreatedAt: timestamppb.Now(), CreatedBy: "test",
				}
				if err := kernel.SignArtifact(bad, priv); err != nil {
					t.Fatal(err)
				}
				return signedNextGeneration(t, priv, current, "g3-compile-failure", "r3", bad)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			set := NewReleaseSet()
			if err := set.ApplyGeneration(current, pub); err != nil {
				t.Fatal(err)
			}
			if err := set.ApplyGeneration(tt.build(t), pub); err == nil {
				t.Fatal("invalid generation must fail")
			}
			got := set.CurrentGeneration()
			if got == nil || got.GenerationId != current.GenerationId || got.GenerationSeq != current.GenerationSeq {
				t.Fatalf("current generation changed: %+v", got)
			}
		})
	}
}

func TestReleaseSetRejectsIncompatibleMinimumVersion(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	gen := mustSignedGen(t, priv, "g1", "a1", 1, "r1", mustSignedRule(t, priv))
	gen.MinEdgeVersion = "2.0.0"
	gen.EnvelopeSignature = nil
	if err := kernel.SignGeneration(gen, priv); err != nil {
		t.Fatal(err)
	}
	if err := NewReleaseSet("1.9.9").ApplyGeneration(gen, pub); err == nil {
		t.Fatal("older edge version must reject generation")
	}
	if err := NewReleaseSet("2.1.0").ApplyGeneration(gen, pub); err != nil {
		t.Fatal(err)
	}
}

func mustSignedGen(t *testing.T, priv ed25519.PrivateKey, id, asset string, seq int64, rel string, a *artifactv1.Artifact) *artifactv1.AssetGeneration {
	t.Helper()
	g := &artifactv1.AssetGeneration{
		GenerationId: id, AssetId: asset, GenerationSeq: seq,
		MinEdgeVersion: kernel.MinimumEdgeVersion,
		Members:        []*artifactv1.ReleaseItem{{ReleaseId: rel, Artifact: a}},
	}
	if err := kernel.SignGeneration(g, priv); err != nil {
		t.Fatal(err)
	}
	return g
}

func signedNextGeneration(t *testing.T, priv ed25519.PrivateKey, current *artifactv1.AssetGeneration, id, rel string, a *artifactv1.Artifact) *artifactv1.AssetGeneration {
	t.Helper()
	gen := mustSignedGen(t, priv, id, current.AssetId, current.GenerationSeq+1, rel, a)
	gen.ParentGenerationId = current.GenerationId
	gen.EnvelopeSignature = nil
	if err := kernel.SignGeneration(gen, priv); err != nil {
		t.Fatal(err)
	}
	return gen
}

func mustSignedRule(t *testing.T, priv ed25519.PrivateKey) *artifactv1.Artifact {
	t.Helper()
	raw, err := MarshalRules([]Rule{{ID: "r", Pattern: "x"}})
	if err != nil {
		t.Fatal(err)
	}
	a := &artifactv1.Artifact{
		Kind: artifactv1.Kind_KIND_RULE, Payload: raw, PayloadSchema: RulePayloadSchema,
		CreatedAt: timestamppb.Now(), CreatedBy: "t",
	}
	if err := kernel.SignArtifact(a, priv); err != nil {
		t.Fatal(err)
	}
	return a
}
