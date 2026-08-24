package brain

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	"yufeng/lib/edgecore"
	"yufeng/lib/kernel"
	artifactv1 "yufeng/proto/gen/artifactv1"
	assetv1 "yufeng/proto/gen/assetv1"
	commonv1 "yufeng/proto/gen/commonv1"
	registryv1 "yufeng/proto/gen/registryv1"
	unitv1 "yufeng/proto/gen/unitv1"
)

func testEdgeCapabilities() *unitv1.ProducerCapabilities {
	return edgecore.ProducerCapabilities()
}

func testEdgeCapabilitiesJSON(t *testing.T) string {
	t.Helper()
	raw, err := protojson.Marshal(testEdgeCapabilities())
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestProducerCapabilitiesGateGenerationAndRemainReadOnly(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	unitID, assetID, token := seedUnitAsset(t, ctx, st, "producer-gate")
	listenPlanRaw, err := protojson.Marshal(&artifactv1.UnitListenPlan{
		UnitId: unitID, Version: 1, Posture: commonv1.IngressPosture_INGRESS_POSTURE_REVERSE_PROXY,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO unit_listen_plans(unit_id, version, envelope, signed) VALUES($1,1,$2::jsonb,true)`, unitID, string(listenPlanRaw)); err != nil {
		t.Fatal(err)
	}
	capabilities := testEdgeCapabilities()
	capabilities.Outputs = []unitv1.ProducerOutput{
		unitv1.ProducerOutput_PRODUCER_OUTPUT_CRITICAL_EVENT,
		unitv1.ProducerOutput_PRODUCER_OUTPUT_ORDINARY_SAMPLE,
	}
	raw, err := protojson.Marshal(capabilities)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE units SET producer_capabilities=$1::jsonb WHERE unit_id=$2`, string(raw), unitID); err != nil {
		t.Fatal(err)
	}
	artifacts := NewArtifactServer(st.Pool())
	req := connect.NewRequest(&artifactv1.ListGenerationsRequest{UnitId: unitID, AssetId: assetID})
	req.Header().Set("Authorization", "Bearer "+token)
	if _, err := artifacts.ListGenerations(ctx, req); connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "producer_capability_mismatch") {
		t.Fatalf("incompatible generation want producer capability failure, got %v", err)
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE units SET producer_capabilities=$1::jsonb WHERE unit_id=$2`, testEdgeCapabilitiesJSON(t), unitID); err != nil {
		t.Fatal(err)
	}
	got, err := artifacts.ListGenerations(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Msg.GetGenerations()) == 0 {
		t.Fatal("compatible generation was not delivered")
	}
}

func TestProducerCapabilityHealthIsProjectedWithoutGrantingAssetBinding(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	boot := "producer-boot-" + newTestSuffix()
	registry := NewRegistryServer(st.Pool(), pub, boot)
	assetID := "asset-producer-" + newTestSuffix()
	unitID := "unit-producer-" + newTestSuffix()
	registration := connect.NewRequest(&registryv1.RegisterRequest{
		UnitId: unitID, Kind: registryv1.UnitKind_UNIT_KIND_EDGE, Version: "v1.2.3", ContractVersion: "v1",
		PubkeyHint: kernel.KeyID(pub), Asset: &assetv1.Asset{Id: assetID, DisplayName: assetID}, Capabilities: testEdgeCapabilities(),
	})
	registration.Header().Set("Authorization", "Bearer "+boot)
	session, err := registry.Register(ctx, registration)
	if err != nil {
		t.Fatal(err)
	}
	loadedGenerationID := "generation-loaded-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO asset_generations(generation_id,asset_id,generation_seq,envelope,signed)
		VALUES($1,$2,1,'{}',true)`, loadedGenerationID, assetID); err != nil {
		t.Fatal(err)
	}
	heartbeat := connect.NewRequest(&registryv1.HeartbeatRequest{
		UnitId: unitID, Generation: 1, Version: "v1.2.4",
		Posture: commonv1.IngressPosture_INGRESS_POSTURE_REVERSE_PROXY, TrafficKey: "site-a",
		Capabilities: testEdgeCapabilities(),
		ProducerHealth: &unitv1.ProducerHealth{
			BufferedCriticalEvents: 3, DroppedOrdinarySamples: 2,
			HealthyProjectionVersions: []string{"event/v1"},
		},
		CurrentGenerationId: loadedGenerationID, CurrentGenerationSeq: 1,
	})
	heartbeat.Header().Set("Authorization", "Bearer "+session.Msg.GetToken())
	if _, err := registry.Heartbeat(ctx, heartbeat); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE units SET health='tap_silent' WHERE unit_id=$1`, unitID); err != nil {
		t.Fatal(err)
	}
	var bindingCount, grantCount int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM unit_assets WHERE unit_id=$1`, unitID).Scan(&bindingCount); err != nil {
		t.Fatal(err)
	}
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM grants WHERE subject_id=$1`, unitID).Scan(&grantCount); err != nil {
		t.Fatal(err)
	}
	if bindingCount != 1 || grantCount != 0 {
		t.Fatalf("capability heartbeat changed authority: bindings=%d grants=%d", bindingCount, grantCount)
	}
	detail, err := NewAssetServer(st.Pool()).assetDetail(ctx, &assetv1.Asset{Id: assetID})
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.GetUnits()) != 1 {
		t.Fatalf("unit projections=%d", len(detail.GetUnits()))
	}
	projection := detail.GetUnits()[0]
	if projection.GetHealth() != commonv1.UnitHealth_UNIT_HEALTH_TAP_SILENT || projection.GetPosture() != commonv1.IngressPosture_INGRESS_POSTURE_REVERSE_PROXY || projection.GetProducerHealth().GetBufferedCriticalEvents() != 3 || projection.GetProducerHealth().GetDroppedOrdinarySamples() != 2 || projection.GetCurrentGenerationId() != loadedGenerationID || projection.GetCurrentGenerationSeq() != 1 {
		t.Fatalf("unit projection=%+v", projection)
	}

	otherAssetID := "asset-hidden-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO assets(asset_id, display_name) VALUES($1,$1)`, otherAssetID); err != nil {
		t.Fatal(err)
	}
	otherGeneration := connect.NewRequest(&artifactv1.ListGenerationsRequest{UnitId: unitID, AssetId: otherAssetID})
	otherGeneration.Header().Set("Authorization", "Bearer "+session.Msg.GetToken())
	if _, err := NewArtifactServer(st.Pool()).ListGenerations(ctx, otherGeneration); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("capability advertisement expanded generation visibility: %v", err)
	}
}
