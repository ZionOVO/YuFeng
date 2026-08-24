package brain

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"connectrpc.com/connect"

	commonv1 "yufeng/proto/gen/commonv1"
	registryv1 "yufeng/proto/gen/registryv1"
)

func TestHeartbeatRejectsTwoInterceptUnits(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	reg := NewRegistryServer(st.Pool(), pub, "boot")
	u1, _, tok1 := seedUnitAsset(t, ctx, st, "int1")
	u2, _, tok2 := seedUnitAsset(t, ctx, st, "int2")
	hb1 := connect.NewRequest(&registryv1.HeartbeatRequest{
		UnitId: u1, Generation: 1,
		Posture: commonv1.IngressPosture_INGRESS_POSTURE_REVERSE_PROXY, TrafficKey: "app-a",
		WindowRequests: 3,
	})
	hb1.Header().Set("Authorization", "Bearer "+tok1)
	if _, err := reg.Heartbeat(ctx, hb1); err != nil {
		t.Fatal(err)
	}
	hb2 := connect.NewRequest(&registryv1.HeartbeatRequest{
		UnitId: u2, Generation: 1,
		Posture: commonv1.IngressPosture_INGRESS_POSTURE_EXT_AUTHZ, TrafficKey: "app-a",
		WindowRequests: 1,
	})
	hb2.Header().Set("Authorization", "Bearer "+tok2)
	if _, err := reg.Heartbeat(ctx, hb2); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("duplicate intercept want failed_precondition got %v", err)
	}
}

func TestHeartbeatTapSilent(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	reg := NewRegistryServer(st.Pool(), pub, "boot")
	edge, _, edgeTok := seedUnitAsset(t, ctx, st, "edge")
	tap, _, tapTok := seedUnitAsset(t, ctx, st, "tap")
	for i := 0; i < 2; i++ {
		hb := connect.NewRequest(&registryv1.HeartbeatRequest{
			UnitId: edge, Generation: uint64(i + 1),
			Posture: commonv1.IngressPosture_INGRESS_POSTURE_REVERSE_PROXY, TrafficKey: "app-b",
			WindowRequests: 5,
		})
		hb.Header().Set("Authorization", "Bearer "+edgeTok)
		if _, err := reg.Heartbeat(ctx, hb); err != nil {
			t.Fatal(err)
		}
		th := connect.NewRequest(&registryv1.HeartbeatRequest{
			UnitId: tap, Generation: uint64(i + 1),
			Posture: commonv1.IngressPosture_INGRESS_POSTURE_TAP_ALERT, TrafficKey: "app-b",
			WindowRequests: 0,
		})
		th.Header().Set("Authorization", "Bearer "+tapTok)
		resp, err := reg.Heartbeat(ctx, th)
		if err != nil {
			t.Fatal(err)
		}
		if i == 1 && resp.Msg.Health != commonv1.UnitHealth_UNIT_HEALTH_TAP_SILENT {
			t.Fatalf("second silent window want tap_silent got %v", resp.Msg.Health)
		}
	}
	var stored string
	if err := st.Pool().QueryRow(ctx, `SELECT health FROM units WHERE unit_id=$1`, tap).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "tap_silent" {
		t.Fatalf("unit health not persisted, got %s", stored)
	}
}

func TestAssetListSurfacesTapSilent(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	reg := NewRegistryServer(st.Pool(), pub, "boot")
	edge, assetID, edgeTok := seedUnitAsset(t, ctx, st, "edge2")
	tap, _, tapTok := seedUnitAsset(t, ctx, st, "tap2")
	if _, err := st.Pool().Exec(ctx, `INSERT INTO unit_assets(unit_id, asset_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, tap, assetID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		hb := connect.NewRequest(&registryv1.HeartbeatRequest{
			UnitId: edge, Generation: uint64(i + 1),
			Posture: commonv1.IngressPosture_INGRESS_POSTURE_REVERSE_PROXY, TrafficKey: "app-c",
			WindowRequests: 5,
		})
		hb.Header().Set("Authorization", "Bearer "+edgeTok)
		if _, err := reg.Heartbeat(ctx, hb); err != nil {
			t.Fatal(err)
		}
		th := connect.NewRequest(&registryv1.HeartbeatRequest{
			UnitId: tap, Generation: uint64(i + 1),
			Posture: commonv1.IngressPosture_INGRESS_POSTURE_TAP_ALERT, TrafficKey: "app-c",
			WindowRequests: 0,
		})
		th.Header().Set("Authorization", "Bearer "+tapTok)
		if _, err := reg.Heartbeat(ctx, th); err != nil {
			t.Fatal(err)
		}
	}
	got, err := assetHealthFromUnits(ctx, st.Pool(), assetID)
	if err != nil {
		t.Fatal(err)
	}
	if got != "tap_silent" {
		t.Fatalf("asset health want tap_silent got %s", got)
	}
}
