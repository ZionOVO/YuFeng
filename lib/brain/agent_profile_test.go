package brain

import (
	"testing"

	"connectrpc.com/connect"

	agentv1 "yufeng/proto/gen/agentv1"
	auditv1 "yufeng/proto/gen/auditv1"
	grantv1 "yufeng/proto/gen/grantv1"
)

func TestAgentProfileLifecycleAndBatchScope(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	h := newOnboardHarness(t, st)
	if err := writeAdminSystemGrant(ctx, st.Pool(), h.adminID, h.local); err != nil {
		t.Fatal(err)
	}
	if err := SeedOnboardingState(ctx, st.Pool(), OnboardingStateCompleted, h.local); err != nil {
		t.Fatal(err)
	}
	server := NewAgentProfileServer(st.Pool())
	bindings := []*grantv1.BindingRef{{Kind: "asset", Id: h.local}}

	create := func(name string) *agentv1.AgentProfile {
		t.Helper()
		got, err := server.CreateAgentProfile(ctx, bearerReq(h.adminTok, &agentv1.CreateAgentProfileRequest{
			DisplayName: name,
			Tools:       []string{"case.get", "case.request_evidence", "run.create"},
			Bindings:    bindings,
		}))
		if err != nil {
			t.Fatal(err)
		}
		return got.Msg.GetProfile()
	}
	first := create("边缘流量审查一组")
	second := create("边缘流量审查二组")
	if first.GetAgentId() == "" || first.GetKind() != agentv1.AgentProfileKind_AGENT_PROFILE_KIND_TRAFFIC_REVIEW {
		t.Fatalf("unexpected created profile: %v", first)
	}

	listed, err := server.ListAgentProfiles(ctx, bearerReq(h.adminTok, &agentv1.ListAgentProfilesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Msg.GetProfiles()) < 2 {
		t.Fatalf("profiles=%d want at least 2", len(listed.Msg.GetProfiles()))
	}

	updated, err := server.UpdateAgentProfile(ctx, bearerReq(h.adminTok, &agentv1.UpdateAgentProfileRequest{
		AgentId:     first.GetAgentId(),
		DisplayName: "夜间审查组",
		State:       agentv1.AgentProfileState_AGENT_PROFILE_STATE_DISABLED,
		Tools:       []string{"case.get", "case.request_evidence", "run.create"},
		Bindings:    bindings,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if updated.Msg.GetProfile().GetState() != agentv1.AgentProfileState_AGENT_PROFILE_STATE_DISABLED {
		t.Fatalf("state=%s want disabled", updated.Msg.GetProfile().GetState())
	}

	batched, err := server.BatchUpdateAgentProfiles(ctx, bearerReq(h.adminTok, &agentv1.BatchUpdateAgentProfilesRequest{
		AgentIds: []string{first.GetAgentId(), second.GetAgentId()},
		Tools:    []string{"case.get", "case.request_evidence", "run.create", "case.complete"},
		Bindings: bindings,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(batched.Msg.GetProfiles()) != 2 {
		t.Fatalf("batch profiles=%d want 2", len(batched.Msg.GetProfiles()))
	}
	for _, profile := range batched.Msg.GetProfiles() {
		if len(profile.GetTools()) != 4 || profile.GetTools()[0] != "case.complete" || profile.GetTools()[1] != "case.get" ||
			profile.GetTools()[2] != "case.request_evidence" || profile.GetTools()[3] != "run.create" {
			t.Fatalf("batch tools=%v", profile.GetTools())
		}
	}

	if _, err := server.DeleteAgentProfile(ctx, bearerReq(h.adminTok, &agentv1.DeleteAgentProfileRequest{AgentId: first.GetAgentId()})); err != nil {
		t.Fatal(err)
	}
	var state string
	var tombstoned bool
	if err := st.Pool().QueryRow(ctx, `SELECT state,tombstoned_at IS NOT NULL FROM managed_agent_profiles WHERE agent_id=$1`,
		first.GetAgentId()).Scan(&state, &tombstoned); err != nil {
		t.Fatal(err)
	}
	if state != "tombstoned" || !tombstoned {
		t.Fatalf("deleted profile state=%q tombstoned=%v", state, tombstoned)
	}
	auditResp, err := NewAuditServer(st.Pool()).ListAuditEntries(ctx, bearerReq(h.adminTok, &auditv1.ListAuditEntriesRequest{
		ObjectType: "agent_profile",
		ObjectId:   first.GetAgentId(),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(auditResp.Msg.GetEntries()) == 0 {
		t.Fatal("deleted agent profile audit must remain visible through its frozen asset ids")
	}
}

func TestAgentProfileRejectsUnsafeToolsAndJarvisMutation(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	h := newOnboardHarness(t, st)
	if err := writeAdminSystemGrant(ctx, st.Pool(), h.adminID, h.local); err != nil {
		t.Fatal(err)
	}
	if err := SeedOnboardingState(ctx, st.Pool(), OnboardingStateCompleted, h.local); err != nil {
		t.Fatal(err)
	}
	server := NewAgentProfileServer(st.Pool())
	bindings := []*grantv1.BindingRef{{Kind: "asset", Id: h.local}}

	_, err := server.CreateAgentProfile(ctx, bearerReq(h.adminTok, &agentv1.CreateAgentProfileRequest{
		DisplayName: "危险岗位",
		Tools:       []string{"govern.promote_enforce"},
		Bindings:    bindings,
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("unsafe tool want invalid_argument, got %v", err)
	}
	_, err = server.CreateAgentProfile(ctx, bearerReq(h.adminTok, &agentv1.CreateAgentProfileRequest{
		DisplayName: "不完整岗位",
		Tools:       []string{"case.get"},
		Bindings:    bindings,
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("incomplete toolset want invalid_argument, got %v", err)
	}
	_, err = server.DeleteAgentProfile(ctx, bearerReq(h.adminTok, &agentv1.DeleteAgentProfileRequest{AgentId: "jarvis"}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("jarvis delete want invalid_argument, got %v", err)
	}
}

func TestListAgentProfilesProjectsManageabilityBeforeBindingRedaction(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	h := newOnboardHarness(t, st)
	if err := writeAdminSystemGrant(ctx, st.Pool(), h.adminID, h.local); err != nil {
		t.Fatal(err)
	}
	if err := SeedOnboardingState(ctx, st.Pool(), OnboardingStateCompleted, h.local); err != nil {
		t.Fatal(err)
	}
	otherAsset := "agent-profile-hidden-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO assets(asset_id, display_name) VALUES($1,$1)`, otherAsset); err != nil {
		t.Fatal(err)
	}
	localProfile := "profile-local-" + newTestSuffix()
	partialProfile := "profile-partial-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO managed_agent_profiles(agent_id,display_name,tools,bindings,created_by)
		VALUES($1,'完整范围 Agent','["case.get","case.request_evidence","run.create"]',
			jsonb_build_array(jsonb_build_object('kind','asset','id',$3::text)),$5),
		($2,'相交范围 Agent','["case.get","case.request_evidence","run.create"]',
			jsonb_build_array(jsonb_build_object('kind','asset','id',$3::text),jsonb_build_object('kind','asset','id',$4::text)),$5)`,
		localProfile, partialProfile, h.local, otherAsset, h.adminID); err != nil {
		t.Fatal(err)
	}

	server := NewAgentProfileServer(st.Pool())
	listed, err := server.ListAgentProfiles(ctx, bearerReq(h.adminTok, &agentv1.ListAgentProfilesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	profiles := make(map[string]*agentv1.AgentProfile, len(listed.Msg.GetProfiles()))
	for _, profile := range listed.Msg.GetProfiles() {
		profiles[profile.GetAgentId()] = profile
	}
	if profile := profiles[localProfile]; profile == nil || !profile.GetCanManage() {
		t.Fatalf("fully covered profile can_manage = %v, want true", profile)
	}
	partial := profiles[partialProfile]
	if partial == nil {
		t.Fatal("profile with an intersecting asset must remain list-visible")
	}
	if partial.GetCanManage() {
		t.Fatal("partially covered profile must not be projected as manageable")
	}
	if got := partial.GetBindings(); len(got) != 1 || got[0].GetId() != h.local {
		t.Fatalf("partially covered profile bindings = %v, want only visible asset", got)
	}

	_, err = server.UpdateAgentProfile(ctx, bearerReq(h.adminTok, &agentv1.UpdateAgentProfileRequest{
		AgentId: partialProfile, DisplayName: "越权更新", State: agentv1.AgentProfileState_AGENT_PROFILE_STATE_ENABLED,
		Tools:    []string{"case.get", "case.request_evidence", "run.create"},
		Bindings: []*grantv1.BindingRef{{Kind: "asset", Id: h.local}},
	}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("partially covered profile update error = %v, want permission_denied", err)
	}
}
