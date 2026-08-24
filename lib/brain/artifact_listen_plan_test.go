package brain

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	"yufeng/lib/kernel"
	artifactv1 "yufeng/proto/gen/artifactv1"
	commonv1 "yufeng/proto/gen/commonv1"
)

func TestListUnitListenPlansScopesToAuthenticatedUnit(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	unitID, _, token := seedUnitAsset(t, ctx, st, "listen-plan")
	otherID, _, _ := seedUnitAsset(t, ctx, st, "listen-plan-other")
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	plan := &artifactv1.UnitListenPlan{
		UnitId: unitID, Posture: commonv1.IngressPosture_INGRESS_POSTURE_REVERSE_PROXY,
		TrafficKey: "site-a", Version: 1, ListenAddress: ":18080", UpstreamUrl: "http://app:8080",
	}
	if err := kernel.SignUnitListenPlan(plan, priv); err != nil {
		t.Fatal(err)
	}
	raw, err := protojson.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO unit_listen_plans(unit_id, version, envelope, signed)
		VALUES($1,1,$2::jsonb,true)`, unitID, string(raw)); err != nil {
		t.Fatal(err)
	}
	server := NewArtifactServer(st.Pool())
	req := connect.NewRequest(&artifactv1.ListUnitListenPlansRequest{UnitId: unitID})
	req.Header().Set("Authorization", "Bearer "+token)
	resp, err := server.ListUnitListenPlans(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Msg.Plans) != 1 || resp.Msg.Plans[0].GetUnitId() != unitID {
		t.Fatalf("plans = %#v", resp.Msg.Plans)
	}
	cross := connect.NewRequest(&artifactv1.ListUnitListenPlansRequest{UnitId: otherID})
	cross.Header().Set("Authorization", "Bearer "+token)
	if _, err := server.ListUnitListenPlans(ctx, cross); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("cross-unit pull want permission_denied, got %v", err)
	}
}
