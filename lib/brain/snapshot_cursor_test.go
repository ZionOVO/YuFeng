package brain

import (
	"strings"
	"testing"

	"connectrpc.com/connect"

	artifactv1 "yufeng/proto/gen/artifactv1"
)

func TestListReleasesSnapshotCursorContinues(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	unitID, assetID, tok := seedUnitAsset(t, ctx, st, "snap")
	payload := strings.Repeat("x", 400)
	for i := 0; i < 2; i++ {
		relID := "rel-snap-" + newTestSuffix()
		art := `{"kind":"KIND_RULE","payload":"` + payload + `"}`
		if _, err := st.Pool().Exec(ctx, `INSERT INTO releases(release_id, state, artifact, ttl_seconds, canary_percent)
			VALUES($1,'enforce',$2::jsonb,86400,0)`, relID, art); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Pool().Exec(ctx, `INSERT INTO release_assets(release_id, asset_id) VALUES($1,$2)`, relID, assetID); err != nil {
			t.Fatal(err)
		}
	}
	arts := NewArtifactServer(st.Pool())
	firstReq := connect.NewRequest(&artifactv1.ListReleasesRequest{UnitId: unitID, FullSnapshot: true, MaxBytes: 300})
	firstReq.Header().Set("Authorization", "Bearer "+tok)
	first, err := arts.ListReleases(ctx, firstReq)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Msg.Snapshot || !first.Msg.HasMore || !strings.HasPrefix(first.Msg.NextCursor, "s:") {
		t.Fatalf("first snapshot page=%+v", first.Msg)
	}
	if len(first.Msg.Items) == 0 {
		t.Fatal("first snapshot page empty")
	}

	secondReq := connect.NewRequest(&artifactv1.ListReleasesRequest{UnitId: unitID, FullSnapshot: true, Cursor: first.Msg.NextCursor, MaxBytes: 300})
	secondReq.Header().Set("Authorization", "Bearer "+tok)
	second, err := arts.ListReleases(ctx, secondReq)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Msg.Snapshot {
		t.Fatal("continuation must stay in snapshot mode")
	}
	if len(second.Msg.Items) == 0 {
		t.Fatal("snapshot cursor must not restart at the first item")
	}
	if second.Msg.Items[0].ReleaseId == first.Msg.Items[0].ReleaseId {
		t.Fatal("snapshot cursor must advance")
	}
}
