package brain

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"connectrpc.com/connect"

	"yufeng/lib/kernel"

	artifactv1 "yufeng/proto/gen/artifactv1"
	assetv1 "yufeng/proto/gen/assetv1"
	registryv1 "yufeng/proto/gen/registryv1"
)

func TestBaselineGenerationReachesGenerationFeed(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	unitID := "unit-basefeed-" + newTestSuffix()
	assetID := "asset-basefeed-" + newTestSuffix()
	raw, hash, err := newSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO units(unit_id, kind, token_hash, producer_capabilities) VALUES($1,'edge',$2,$3::jsonb)`, unitID, hash, testEdgeCapabilitiesJSON(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO assets(asset_id, display_name, max_auto_tier) VALUES($1,$1,'L1')`, assetID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO unit_assets(unit_id, asset_id) VALUES($1,$2)`, unitID, assetID); err != nil {
		t.Fatal(err)
	}

	artifacts := NewArtifactServer(st.Pool())
	before := connect.NewRequest(&artifactv1.ListGenerationsRequest{UnitId: unitID, AssetId: assetID})
	before.Header().Set("Authorization", "Bearer "+raw)
	first, err := artifacts.ListGenerations(ctx, before)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Msg.Generations) != 0 {
		t.Fatalf("empty generation feed want 0 got %d", len(first.Msg.Generations))
	}

	if _, err := publishBaselineGeneration(ctx, st.Pool(), priv, nil, assetID, "jarvis"); err != nil {
		t.Fatal(err)
	}
	inc := connect.NewRequest(&artifactv1.ListGenerationsRequest{UnitId: unitID, AssetId: assetID})
	inc.Header().Set("Authorization", "Bearer "+raw)
	got, err := artifacts.ListGenerations(ctx, inc)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Msg.Generations) != 1 {
		t.Fatalf("generation feed after baseline want 1 got %d", len(got.Msg.Generations))
	}
	kinds := map[artifactv1.Kind]bool{}
	for _, it := range got.Msg.Generations[0].GetMembers() {
		if it.Artifact != nil {
			kinds[it.Artifact.Kind] = true
		}
	}
	for _, kind := range []artifactv1.Kind{
		artifactv1.Kind_KIND_DETECTOR_MANIFEST,
		artifactv1.Kind_KIND_TAXONOMY_MAPPER,
		artifactv1.Kind_KIND_NORMALIZER_PROFILE,
		artifactv1.Kind_KIND_EVIDENCE_DIGEST,
		artifactv1.Kind_KIND_FORWARD_POLICY,
		artifactv1.Kind_KIND_MODEL_PROFILE,
	} {
		if !kinds[kind] {
			t.Errorf("baseline generation missing %s", kind)
		}
	}
}

func TestRegisterCanPullExistingBaselineGeneration(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	assetID := "asset-regfeed-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO assets(asset_id, display_name, max_auto_tier) VALUES($1,$1,'L1')`, assetID); err != nil {
		t.Fatal(err)
	}
	if _, err := publishBaselineGeneration(ctx, st.Pool(), priv, nil, assetID, "jarvis"); err != nil {
		t.Fatal(err)
	}
	boot := "boot-regfeed-" + newTestSuffix()
	reg := NewRegistryServer(st.Pool(), pub, boot)
	unitID := "unit-regfeed-" + newTestSuffix()
	req := connect.NewRequest(&registryv1.RegisterRequest{
		UnitId: unitID, Kind: registryv1.UnitKind_UNIT_KIND_EDGE, Version: "t",
		ContractVersion: "v1", PubkeyHint: kernel.KeyID(pub),
		Asset: &assetv1.Asset{Id: assetID, DisplayName: assetID}, Capabilities: testEdgeCapabilities(),
	})
	req.Header().Set("Authorization", "Bearer "+boot)
	ok, err := reg.Register(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	artifacts := NewArtifactServer(st.Pool())
	list := connect.NewRequest(&artifactv1.ListGenerationsRequest{UnitId: unitID, AssetId: assetID})
	list.Header().Set("Authorization", "Bearer "+ok.Msg.Token)
	got, err := artifacts.ListGenerations(ctx, list)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Msg.Generations) != 1 {
		t.Fatalf("baseline generations after register want 1 got %d", len(got.Msg.Generations))
	}
}
