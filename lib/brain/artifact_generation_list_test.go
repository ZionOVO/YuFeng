package brain

import (
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	"yufeng/lib/edgecore"
	artifactv1 "yufeng/proto/gen/artifactv1"
)

func TestListGenerationsHonorsByteBudget(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	unitID, assetID, tok := seedUnitAsset(t, ctx, st, "lg")
	payload, err := protojson.Marshal(edgecore.DefaultTaxonomyMapper())
	if err != nil {
		t.Fatal(err)
	}
	art := &artifactv1.Artifact{Kind: artifactv1.Kind_KIND_TAXONOMY_MAPPER, Payload: payload, PayloadSchema: edgecore.TaxonomyMapperSchema}
	gen := &artifactv1.AssetGeneration{
		GenerationId:  "gen-lg-2-" + newTestSuffix(),
		AssetId:       assetID,
		GenerationSeq: 2,
		Members:       []*artifactv1.ReleaseItem{{ReleaseId: "rel-lg-2", Artifact: art, AssetId: assetID}},
	}
	env, err := protojson.Marshal(gen)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO asset_generations(generation_id, asset_id, generation_seq, envelope, signed)
		VALUES($1,$2,2,$3::jsonb,true)`, gen.GenerationId, assetID, string(env)); err != nil {
		t.Fatal(err)
	}

	arts := NewArtifactServer(st.Pool())
	first := connect.NewRequest(&artifactv1.ListGenerationsRequest{UnitId: unitID, AssetId: assetID, MaxBytes: 1})
	first.Header().Set("Authorization", "Bearer "+tok)
	page, err := arts.ListGenerations(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Msg.Generations) != 1 || !page.Msg.GetHasMore() {
		t.Fatalf("want one generation and has_more, got n=%d has_more=%v", len(page.Msg.Generations), page.Msg.GetHasMore())
	}
	next := connect.NewRequest(&artifactv1.ListGenerationsRequest{
		UnitId: unitID, AssetId: assetID, SinceSeq: page.Msg.Generations[0].GetGenerationSeq(), MaxBytes: 1,
	})
	next.Header().Set("Authorization", "Bearer "+tok)
	page2, err := arts.ListGenerations(ctx, next)
	if err != nil {
		t.Fatal(err)
	}
	if len(page2.Msg.Generations) != 1 || page2.Msg.GetHasMore() {
		t.Fatalf("second page n=%d has_more=%v", len(page2.Msg.Generations), page2.Msg.GetHasMore())
	}
	if page2.Msg.Generations[0].GetGenerationSeq() <= page.Msg.Generations[0].GetGenerationSeq() {
		t.Fatal("second page must advance generation_seq")
	}
}
