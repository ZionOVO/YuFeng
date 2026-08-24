package brain

import (
	"crypto/ed25519"
	"crypto/rand"
	"strconv"
	"testing"
	"time"

	"connectrpc.com/connect"

	auditv1 "yufeng/proto/gen/auditv1"
	authv1 "yufeng/proto/gen/authv1"
	governv1 "yufeng/proto/gen/governv1"
	sessionv1 "yufeng/proto/gen/sessionv1"
)

func TestPageOffsetTokenIsOpaqueAndRejectsInvalidInput(t *testing.T) {
	token := encodePageOffset(42)
	if token == "" || token == "42" {
		t.Fatalf("page token must be opaque base64, got %q", token)
	}
	offset, err := decodePageOffset(token)
	if err != nil || offset != 42 {
		t.Fatalf("page token round trip got offset=%d err=%v", offset, err)
	}
	if _, err := decodePageOffset("not-a-valid-token"); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("invalid page token want invalid_argument got %v", err)
	}
}

func TestReleaseTimelineEnforcesScopeAndPaginates(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	username := "timeline-page-" + newTestSuffix()
	if err := EnsureBootstrapAdmin(ctx, st.Pool(), username, "Admin12345"); err != nil {
		t.Fatal(err)
	}
	login, err := NewAuthServer(st.Pool(), time.Hour, false, 8).Login(ctx, connect.NewRequest(&authv1.LoginRequest{
		Username: username, Password: "Admin12345",
	}))
	if err != nil {
		t.Fatal(err)
	}
	visible := "asset-timeline-visible-" + newTestSuffix()
	hidden := "asset-timeline-hidden-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO assets(asset_id, display_name, max_auto_tier) VALUES($1,$1,'L1'),($2,$2,'L1')`, visible, hidden); err != nil {
		t.Fatal(err)
	}
	if err := writeAdminSystemGrant(ctx, st.Pool(), login.Msg.User.UserId, visible); err != nil {
		t.Fatal(err)
	}
	if err := SeedOnboardingState(ctx, st.Pool(), OnboardingStateCompleted, visible); err != nil {
		t.Fatal(err)
	}
	visibleRelease := "rel-timeline-visible-" + newTestSuffix()
	hiddenRelease := "rel-timeline-hidden-" + newTestSuffix()
	for _, pair := range [][2]string{{visibleRelease, visible}, {hiddenRelease, hidden}} {
		if _, err := st.Pool().Exec(ctx, `INSERT INTO releases(release_id, state, artifact, ttl_seconds) VALUES($1,'draft','{}',86400)`, pair[0]); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Pool().Exec(ctx, `INSERT INTO release_assets(release_id, asset_id) VALUES($1,$2)`, pair[0], pair[1]); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if _, err := st.Pool().Exec(ctx, `INSERT INTO release_timeline(release_id, from_state, to_state, actor, reason) VALUES($1,'draft','signed','tester',$2)`, visibleRelease, strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	server := NewGovernServer(st.Pool(), priv, 0, 0, 0, 0)
	hiddenReq := connect.NewRequest(&governv1.GetReleaseTimelineRequest{ReleaseId: hiddenRelease})
	hiddenReq.Header().Set("Authorization", "Bearer "+login.Msg.Token)
	if _, err := server.GetReleaseTimeline(ctx, hiddenReq); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("hidden timeline want permission_denied got %v", err)
	}
	firstReq := connect.NewRequest(&governv1.GetReleaseTimelineRequest{ReleaseId: visibleRelease, PageSize: 1})
	firstReq.Header().Set("Authorization", "Bearer "+login.Msg.Token)
	first, err := server.GetReleaseTimeline(ctx, firstReq)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Msg.Entries) != 1 || first.Msg.NextPageToken == "" {
		t.Fatalf("timeline first page: %+v", first.Msg)
	}
	secondReq := connect.NewRequest(&governv1.GetReleaseTimelineRequest{ReleaseId: visibleRelease, PageSize: 1, PageToken: first.Msg.NextPageToken})
	secondReq.Header().Set("Authorization", "Bearer "+login.Msg.Token)
	second, err := server.GetReleaseTimeline(ctx, secondReq)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Msg.Entries) != 1 || second.Msg.NextPageToken != "" {
		t.Fatalf("timeline second page: %+v", second.Msg)
	}
}

func TestSessionHistoryPaginatesWithOpaqueToken(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	username := "session-page-" + newTestSuffix()
	if err := EnsureBootstrapAdmin(ctx, st.Pool(), username, "Admin12345"); err != nil {
		t.Fatal(err)
	}
	login, err := NewAuthServer(st.Pool(), time.Hour, false, 8).Login(ctx, connect.NewRequest(&authv1.LoginRequest{
		Username: username, Password: "Admin12345",
	}))
	if err != nil {
		t.Fatal(err)
	}
	server := NewSessionServer(st.Pool(), nil, "jarvis-1")
	create := connect.NewRequest(&sessionv1.CreateSessionRequest{Title: "page"})
	create.Header().Set("Authorization", "Bearer "+login.Msg.Token)
	created, err := server.CreateSession(ctx, create)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO session_messages(session_id, sender, content) VALUES($1,$2,'one'),($1,$2,'two')`, created.Msg.SessionId, login.Msg.User.UserId); err != nil {
		t.Fatal(err)
	}
	firstReq := connect.NewRequest(&sessionv1.ListMessagesRequest{SessionId: created.Msg.SessionId, PageSize: 1})
	firstReq.Header().Set("Authorization", "Bearer "+login.Msg.Token)
	first, err := server.ListMessages(ctx, firstReq)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Msg.Messages) != 1 || first.Msg.NextPageToken == "" {
		t.Fatalf("session first page: %+v", first.Msg)
	}
	secondReq := connect.NewRequest(&sessionv1.ListMessagesRequest{SessionId: created.Msg.SessionId, PageSize: 1, PageToken: first.Msg.NextPageToken})
	secondReq.Header().Set("Authorization", "Bearer "+login.Msg.Token)
	second, err := server.ListMessages(ctx, secondReq)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Msg.Messages) != 1 || second.Msg.NextPageToken != "" || second.Msg.Messages[0].Sequence == first.Msg.Messages[0].Sequence {
		t.Fatalf("session second page: %+v", second.Msg)
	}
}

func TestAuditPaginationAppliesVisibilityBeforePageBoundary(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	username := "audit-page-" + newTestSuffix()
	if err := EnsureBootstrapAdmin(ctx, st.Pool(), username, "Admin12345"); err != nil {
		t.Fatal(err)
	}
	login, err := NewAuthServer(st.Pool(), time.Hour, false, 8).Login(ctx, connect.NewRequest(&authv1.LoginRequest{
		Username: username, Password: "Admin12345",
	}))
	if err != nil {
		t.Fatal(err)
	}
	visible := "asset-visible-" + newTestSuffix()
	hidden := "asset-hidden-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO assets(asset_id, display_name, max_auto_tier) VALUES($1,$1,'L1'),($2,$2,'L1')`, visible, hidden); err != nil {
		t.Fatal(err)
	}
	if err := writeAdminSystemGrant(ctx, st.Pool(), login.Msg.User.UserId, visible); err != nil {
		t.Fatal(err)
	}
	if err := SeedOnboardingState(ctx, st.Pool(), OnboardingStateCompleted, visible); err != nil {
		t.Fatal(err)
	}
	if err := appendAudit(ctx, st.Pool(), "user", "actor", "visible.old", "asset", visible, nil); err != nil {
		t.Fatal(err)
	}
	if err := appendAudit(ctx, st.Pool(), "user", "actor", "hidden", "asset", hidden, nil); err != nil {
		t.Fatal(err)
	}
	if err := appendAudit(ctx, st.Pool(), "user", "actor", "visible.new", "asset", visible, nil); err != nil {
		t.Fatal(err)
	}

	server := NewAuditServer(st.Pool())
	firstReq := connect.NewRequest(&auditv1.ListAuditEntriesRequest{PageSize: 1})
	firstReq.Header().Set("Authorization", "Bearer "+login.Msg.Token)
	first, err := server.ListAuditEntries(ctx, firstReq)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Msg.Entries) != 1 || first.Msg.Entries[0].Action != "visible.new" || first.Msg.NextPageToken == "" {
		t.Fatalf("unexpected first visible page: %+v", first.Msg)
	}
	secondReq := connect.NewRequest(&auditv1.ListAuditEntriesRequest{PageSize: 1, PageToken: first.Msg.NextPageToken})
	secondReq.Header().Set("Authorization", "Bearer "+login.Msg.Token)
	second, err := server.ListAuditEntries(ctx, secondReq)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Msg.Entries) != 1 || second.Msg.Entries[0].Action != "visible.old" || second.Msg.NextPageToken != "" {
		t.Fatalf("unexpected second visible page: %+v", second.Msg)
	}
}
