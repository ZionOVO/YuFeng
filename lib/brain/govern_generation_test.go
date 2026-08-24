package brain

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	artifactv1 "yufeng/proto/gen/artifactv1"
)

func TestPublishAssetGenerationMarksRollbackAndPublishesEmptyState(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO assets(asset_id, display_name, max_auto_tier) VALUES('asset-generation','asset-generation','L1')`); err != nil {
		t.Fatal(err)
	}
	if err := publishAssetGeneration(ctx, st.Pool(), "asset-generation", key, nil, false); err != nil {
		t.Fatal(err)
	}
	if err := publishAssetGeneration(ctx, st.Pool(), "asset-generation", key, nil, true); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := st.Pool().QueryRow(ctx, `SELECT envelope FROM asset_generations WHERE asset_id='asset-generation' ORDER BY generation_seq DESC LIMIT 1`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var gen artifactv1.AssetGeneration
	if err := protojson.Unmarshal(raw, &gen); err != nil {
		t.Fatal(err)
	}
	if gen.GenerationSeq != 2 || gen.RollbackOf != 1 || gen.ParentGenerationId == "" {
		t.Fatalf("rollback envelope=%+v", &gen)
	}
	if len(gen.Members) != 0 {
		t.Fatalf("empty active state must be published, members=%d", len(gen.Members))
	}
}
