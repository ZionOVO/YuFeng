package edgecore

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"

	"yufeng/lib/kernel"
	artifactv1 "yufeng/proto/gen/artifactv1"
	commonv1 "yufeng/proto/gen/commonv1"
)

func TestListenPlanActivationRequiresSignatureTargetAndMonotonicVersion(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	makePlan := func(version uint64, upstream string) *artifactv1.UnitListenPlan {
		plan := &artifactv1.UnitListenPlan{
			UnitId: "unit-a", Posture: commonv1.IngressPosture_INGRESS_POSTURE_REVERSE_PROXY,
			TrafficKey: "site-a", Version: version, ListenAddress: ":18080", UpstreamUrl: upstream,
			ModelIngressWindow: kernel.DefaultModelIngressWindow(),
		}
		if err := kernel.SignUnitListenPlan(plan, priv); err != nil {
			t.Fatal(err)
		}
		return plan
	}
	set := NewReleaseSet()
	first := makePlan(1, "http://app:8080")
	if err := set.ApplyListenPlan(first, pub, "unit-b"); err == nil {
		t.Fatal("wrong target unit must fail")
	}
	if err := set.ApplyListenPlan(first, pub, "unit-a"); err != nil {
		t.Fatal(err)
	}
	if got := set.CurrentListenPlan(); got == nil || got.Version != 1 {
		t.Fatalf("active plan = %#v", got)
	}
	if err := set.ApplyListenPlan(makePlan(3, "http://app:8080"), pub, "unit-a"); err == nil {
		t.Fatal("skipped version must fail")
	}
	second := makePlan(2, "http://app-v2:8080")
	if err := set.ApplyListenPlan(second, pub, "unit-a", func(_, _ *artifactv1.UnitListenPlan) error {
		return errPersistListenPlan
	}); err == nil {
		t.Fatal("persistence failure must reject activation")
	}
	if got := set.CurrentListenPlan(); got == nil || got.Version != 1 {
		t.Fatalf("persistence failure changed active plan: %#v", got)
	}
	if err := set.ApplyListenPlan(second, pub, "unit-a"); err != nil {
		t.Fatal(err)
	}
	if got := set.CurrentListenPlan(); got == nil || got.UpstreamUrl != "http://app-v2:8080" {
		t.Fatalf("active plan = %#v", got)
	}
}

var errPersistListenPlan = errors.New("disk full")
