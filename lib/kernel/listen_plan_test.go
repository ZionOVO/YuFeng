package kernel

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	artifactv1 "yufeng/proto/gen/artifactv1"
	commonv1 "yufeng/proto/gen/commonv1"
)

func TestUnitListenPlanSignatureCoversBinding(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	plan := &artifactv1.UnitListenPlan{
		UnitId: "unit-a", Posture: commonv1.IngressPosture_INGRESS_POSTURE_REVERSE_PROXY,
		TrafficKey: "site-a", Version: 1, ListenAddress: ":18080", UpstreamUrl: "http://app:8080",
		ModelIngressWindow: DefaultModelIngressWindow(),
	}
	if err := SignUnitListenPlan(plan, priv); err != nil {
		t.Fatal(err)
	}
	if err := VerifyUnitListenPlan(plan, pub); err != nil {
		t.Fatal(err)
	}
	plan.ModelIngressWindow.MaxItems++
	if err := VerifyUnitListenPlan(plan, pub); err == nil {
		t.Fatal("tampered model ingress window passed signature verification")
	}
}
