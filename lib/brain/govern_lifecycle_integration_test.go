package brain

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"

	"yufeng/lib/edgecore"

	artifactv1 "yufeng/proto/gen/artifactv1"
	authv1 "yufeng/proto/gen/authv1"
	commonv1 "yufeng/proto/gen/commonv1"
	governv1 "yufeng/proto/gen/governv1"
	userv1 "yufeng/proto/gen/userv1"
)

// 治理状态机全链路：Propose → Gate → Shadow → Canary → Enforce → Retire。
// 门禁阈值全零（演示配置），重点验证状态推进与 timeline 的 from_state 真实性。
func TestGovernLifecycleStateMachine(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	if err := EnsureBootstrapAdmin(ctx, st.Pool(), "itlifecycle", "Admin12345"); err != nil {
		t.Fatal(err)
	}
	auth := NewAuthServer(st.Pool(), time.Hour, false, MinPasswordLength)
	login, err := auth.Login(ctx, connect.NewRequest(&authv1.LoginRequest{Username: "itlifecycle", Password: "Admin12345"}))
	if err != nil {
		t.Fatal(err)
	}
	bearer := "Bearer " + login.Msg.Token
	users := NewUserServer(st.Pool(), 8)
	createOfficer := connect.NewRequest(&userv1.CreateUserRequest{
		Username: "itlifecycle-officer", Password: "Officer123", Role: commonv1.UserRole_USER_ROLE_OPERATOR,
	})
	createOfficer.Header().Set("Authorization", bearer)
	setTestIdempotency(createOfficer)
	officer, err := users.CreateUser(ctx, createOfficer)
	if err != nil {
		t.Fatal(err)
	}
	officerLogin, err := auth.Login(ctx, connect.NewRequest(&authv1.LoginRequest{Username: "itlifecycle-officer", Password: "Officer123"}))
	if err != nil {
		t.Fatal(err)
	}
	officerBearer := "Bearer " + officerLogin.Msg.Token

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	govern := NewGovernServer(st.Pool(), priv, 0, 0, 0, 0)
	govern.demoTriage = true
	if _, err := st.Pool().Exec(ctx, `INSERT INTO assets(asset_id, display_name, max_auto_tier)
		VALUES('itlifecycle-asset','itlifecycle-asset','L1') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	if err := SeedOnboardingState(ctx, st.Pool(), OnboardingStateCompleted, "itlifecycle-asset"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		uid := "itlifecycle-unit-" + itoa(i)
		if _, err := st.Pool().Exec(ctx, `INSERT INTO units(unit_id, kind, token_hash) VALUES($1,'edge',$2) ON CONFLICT DO NOTHING`, uid, "h-"+uid); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Pool().Exec(ctx, `INSERT INTO unit_assets(unit_id, asset_id) VALUES($1,'itlifecycle-asset') ON CONFLICT DO NOTHING`, uid); err != nil {
			t.Fatal(err)
		}
	}
	if err := insertUserGrant(ctx, st.Pool(), login.Msg.User.UserId, []string{
		"govern.propose", "govern.gate", "govern.start_shadow",
	}, "itlifecycle-asset"); err != nil {
		t.Fatal(err)
	}
	if err := insertUserGrant(ctx, st.Pool(), officer.Msg.User.UserId, []string{
		"govern.promote_canary", "govern.promote_enforce", "govern.retire",
	}, "itlifecycle-asset"); err != nil {
		t.Fatal(err)
	}

	payload, err := edgecore.MarshalRules([]edgecore.Rule{
		{ID: "sql-union", Pattern: `(?i)union\s+select`},
		{ID: "xss-script", Pattern: `(?i)<script`},
		{ID: "path-traversal", Pattern: `\.\./`},
	})
	if err != nil {
		t.Fatal(err)
	}
	propReq := connect.NewRequest(&governv1.ProposeArtifactRequest{
		Kind: artifactv1.Kind_KIND_RULE, Payload: payload, PayloadSchema: edgecore.RulePayloadSchema,
		Scope: &artifactv1.Scope{AssetIds: []string{"itlifecycle-asset"}},
		Ttl:   durationpb.New(time.Hour),
	})
	propReq.Header().Set("Authorization", bearer)
	propReq.Header().Set("Idempotency-Key", "lifecycle-propose")
	proposed, err := govern.ProposeArtifact(ctx, propReq)
	if err != nil {
		t.Fatal(err)
	}
	releaseID := proposed.Msg.ReleaseId
	if proposed.Msg.State != commonv1.ReleaseState_RELEASE_STATE_DRAFT {
		t.Fatalf("提案后状态 %s", proposed.Msg.State)
	}

	gateReq := connect.NewRequest(&governv1.GateArtifactRequest{ReleaseId: releaseID})
	gateReq.Header().Set("Authorization", bearer)
	gateReq.Header().Set("Idempotency-Key", "lifecycle-gate")
	gated, err := govern.GateArtifact(ctx, gateReq)
	if err != nil {
		t.Fatal(err)
	}
	if gated.Msg.State != commonv1.ReleaseState_RELEASE_STATE_SIGNED {
		t.Fatalf("门禁未通过: state=%s report=%v", gated.Msg.State, gated.Msg.ReplayReport)
	}
	shadowReq := connect.NewRequest(&governv1.StartShadowRequest{ReleaseId: releaseID})
	shadowReq.Header().Set("Authorization", bearer)
	shadowReq.Header().Set("Idempotency-Key", "lifecycle-shadow")
	if _, err := govern.StartShadow(ctx, shadowReq); err != nil {
		t.Fatal(err)
	}
	canaryReq := connect.NewRequest(&governv1.PromoteCanaryRequest{ReleaseId: releaseID, CanaryPercent: 25})
	canaryReq.Header().Set("Authorization", officerBearer)
	canaryReq.Header().Set("Idempotency-Key", "lifecycle-canary")
	if _, err := govern.PromoteCanary(ctx, canaryReq); err != nil {
		t.Fatal(err)
	}
	enforceReq := connect.NewRequest(&governv1.PromoteEnforceRequest{ReleaseId: releaseID})
	enforceReq.Header().Set("Authorization", officerBearer)
	enforceReq.Header().Set("Idempotency-Key", "lifecycle-enforce")
	if _, err := govern.PromoteEnforce(ctx, enforceReq); err != nil {
		t.Fatal(err)
	}
	retireReq := connect.NewRequest(&governv1.RetireReleaseRequest{ReleaseId: releaseID})
	retireReq.Header().Set("Authorization", officerBearer)
	retireReq.Header().Set("Idempotency-Key", "lifecycle-retire")
	retired, err := govern.RetireRelease(ctx, retireReq)
	if err != nil {
		t.Fatal(err)
	}
	if retired.Msg.Release.State != commonv1.ReleaseState_RELEASE_STATE_RETIRED {
		t.Fatalf("退休后状态 %s", retired.Msg.Release.State)
	}

	// timeline 的 from_state 必须是真实前态（回归：曾恒写 'draft'）。
	rows, err := st.Pool().Query(ctx, `SELECT from_state, to_state FROM release_timeline WHERE release_id=$1 ORDER BY sequence`, releaseID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var transitions [][2]string
	for rows.Next() {
		var from, to string
		if err := rows.Scan(&from, &to); err != nil {
			t.Fatal(err)
		}
		transitions = append(transitions, [2]string{from, to})
	}
	if rows.Err() != nil {
		t.Fatal(rows.Err())
	}
	want := [][2]string{
		{"draft", "signed"}, {"signed", "shadow"}, {"shadow", "canary"},
		{"canary", "enforce"}, {"enforce", "retired"},
	}
	if len(transitions) != len(want) {
		t.Fatalf("timeline 转换数 %d（want %d）: %v", len(transitions), len(want), transitions)
	}
	for i, w := range want {
		if transitions[i] != w {
			t.Fatalf("timeline[%d] = %v, want %v", i, transitions[i], w)
		}
	}

	// 终态之后再退休必须失败（状态机守卫）。
	reRetireReq := connect.NewRequest(&governv1.RetireReleaseRequest{ReleaseId: releaseID})
	reRetireReq.Header().Set("Authorization", bearer)
	if _, err := govern.RetireRelease(ctx, reRetireReq); err == nil {
		t.Fatal("已退休的发布再次退休应报状态冲突")
	}
}
