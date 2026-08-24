package brain

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"testing"
	"time"

	"connectrpc.com/connect"

	"yufeng/lib/store"

	authv1 "yufeng/proto/gen/authv1"
	governv1 "yufeng/proto/gen/governv1"
)

func TestGovernListReleasesScanColumns(t *testing.T) {
	dsn := os.Getenv("YUFENG_TEST_DSN")
	if dsn == "" {
		t.Skip("YUFENG_TEST_DSN 未设置，跳过 PostgreSQL 集成测试")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	st, err := store.Open(ctx, store.Config{DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	resetOnboardingForTest(ctx, st.Pool())
	if err := EnsureBootstrapAdmin(ctx, st.Pool(), "itgovadmin", "Admin12345"); err != nil {
		t.Fatal(err)
	}
	auth := NewAuthServer(st.Pool(), time.Hour, false, 8)
	loginReq := connect.NewRequest(&authv1.LoginRequest{Username: "itgovadmin", Password: "Admin12345"})
	loginResp, err := auth.Login(ctx, loginReq)
	if err != nil {
		t.Fatal(err)
	}
	assetID := "itgov-asset-1"
	if _, err := st.Pool().Exec(ctx, `INSERT INTO assets(asset_id, display_name, max_auto_tier) VALUES($1,$1,'L1') ON CONFLICT DO NOTHING`, assetID); err != nil {
		t.Fatal(err)
	}
	if err := writeAdminSystemGrant(ctx, st.Pool(), loginResp.Msg.User.UserId, assetID); err != nil {
		t.Fatal(err)
	}
	if err := SeedOnboardingState(ctx, st.Pool(), OnboardingStateCompleted, assetID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO releases(release_id, state, artifact, ttl_seconds)
	VALUES('itgov-rel-1','draft','{}'::jsonb,86400) ON CONFLICT(release_id) DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO release_assets(release_id, asset_id) VALUES('itgov-rel-1',$1) ON CONFLICT DO NOTHING`, assetID); err != nil {
		t.Fatal(err)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	govern := NewGovernServer(st.Pool(), priv, 0, 0, 0, 0)
	govern.demoTriage = true
	listReq := connect.NewRequest(&governv1.ListReleasesRequest{})
	listReq.Header().Set("Authorization", "Bearer "+loginResp.Msg.Token)
	if _, err := govern.ListReleases(ctx, listReq); err != nil {
		t.Fatalf("ListReleases 查询/扫描列数不一致: %v", err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO release_timeline(release_id, from_state, to_state, actor)
	VALUES('itgov-rel-1','draft','signed','test') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	timelineReq := connect.NewRequest(&governv1.GetReleaseTimelineRequest{ReleaseId: "itgov-rel-1"})
	timelineReq.Header().Set("Authorization", "Bearer "+loginResp.Msg.Token)
	if _, err := govern.GetReleaseTimeline(ctx, timelineReq); err != nil {
		t.Fatalf("GetReleaseTimeline 查询/扫描列数不一致: %v", err)
	}
}
