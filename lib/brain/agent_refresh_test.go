package brain

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"connectrpc.com/connect"
	"yufeng/lib/kernel"

	agentv1 "yufeng/proto/gen/agentv1"
)

func TestRefreshAccessTokenRotatesAndRejectsReuse(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	boot := "boot-ref-" + newTestSuffix()
	id := "agent-ref-" + newTestSuffix()
	s := NewAgentServer(st.Pool(), boot, priv)
	reg, err := s.RegisterAgent(ctx, connect.NewRequest(&agentv1.RegisterAgentRequest{
		AgentId: id, BootstrapToken: boot, AgentPublicKey: "pub-ref",
	}))
	if err != nil {
		t.Fatal(err)
	}
	var storedHash string
	if err := st.Pool().QueryRow(ctx, `SELECT pubkey_hash FROM agent_tokens WHERE agent_id=$1 AND kind='access'`, id).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	if storedHash == "" || storedHash != hashToken("pub-ref") {
		t.Fatalf("access token must bind registered pubkey, hash=%q", storedHash)
	}

	first, err := s.RefreshAccessToken(ctx, connect.NewRequest(&agentv1.RefreshAccessTokenRequest{RefreshToken: reg.Msg.RefreshToken}))
	if err != nil {
		t.Fatal(err)
	}
	if first.Msg.RefreshToken == "" || first.Msg.RefreshToken == reg.Msg.RefreshToken {
		t.Fatal("refresh must rotate")
	}
	if _, err := s.RefreshAccessToken(ctx, connect.NewRequest(&agentv1.RefreshAccessTokenRequest{RefreshToken: reg.Msg.RefreshToken})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("old refresh want unauthenticated, got %v", err)
	}

	if _, err := st.Pool().Exec(ctx, `UPDATE agents SET refresh_expires_at=now()-interval '1 second' WHERE agent_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RefreshAccessToken(ctx, connect.NewRequest(&agentv1.RefreshAccessTokenRequest{RefreshToken: first.Msg.RefreshToken})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("expired refresh want unauthenticated, got %v", err)
	}
}

func TestRefreshAccessTokenRejectsRevokedAgent(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	boot := "boot-rev-" + newTestSuffix()
	id := "agent-rev-" + newTestSuffix()
	s := NewAgentServer(st.Pool(), boot, priv)
	reg, err := s.RegisterAgent(ctx, connect.NewRequest(&agentv1.RegisterAgentRequest{
		AgentId: id, BootstrapToken: boot, AgentPublicKey: "pub-rev",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE agents SET revoked_at=now() WHERE agent_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RefreshAccessToken(ctx, connect.NewRequest(&agentv1.RefreshAccessTokenRequest{RefreshToken: reg.Msg.RefreshToken})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("revoked agent want unauthenticated, got %v", err)
	}
	hb := connect.NewRequest(&agentv1.HeartbeatRequest{AgentId: id})
	hb.Header().Set("Authorization", "Bearer "+reg.Msg.AccessToken)
	if _, err := s.Heartbeat(ctx, hb); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("revoked access want unauthenticated, got %v", err)
	}
}

func TestReleaseRevokesPreviousCapability(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	boot := "boot-lease-" + newTestSuffix()
	id := "agent-lease-" + newTestSuffix()
	s := NewAgentServer(st.Pool(), boot, priv)
	reg, err := s.RegisterAgent(ctx, connect.NewRequest(&agentv1.RegisterAgentRequest{
		AgentId: id, BootstrapToken: boot, AgentPublicKey: "pub-lease",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnqueueInstruction(ctx, id, instructionTriage, "cluster-1", demoTriageInstructionTools, nil); err != nil {
		t.Fatal(err)
	}
	poll := connect.NewRequest(&agentv1.PollInstructionsRequest{AgentId: id, LongPollSeconds: 1})
	poll.Header().Set("Authorization", "Bearer "+reg.Msg.AccessToken)
	first, err := s.PollInstructions(ctx, poll)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Msg.Instructions) != 1 {
		t.Fatalf("want 1 instruction, got %d", len(first.Msg.Instructions))
	}
	old := first.Msg.Instructions[0]
	oldTok := old.CapabilityToken
	if _, err := st.Pool().Exec(ctx, `UPDATE agent_instructions SET lease_expires_at=now()-interval '1 second' WHERE instruction_id=$1`, first.Msg.Instructions[0].InstructionId); err != nil {
		t.Fatal(err)
	}
	second, err := s.PollInstructions(ctx, poll)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Msg.Instructions) != 1 {
		t.Fatalf("re-lease want 1, got %d", len(second.Msg.Instructions))
	}
	if second.Msg.Instructions[0].CapabilityToken == oldTok {
		t.Fatal("re-lease must issue a new capability token")
	}
	if second.Msg.Instructions[0].BudgetId != old.BudgetId || second.Msg.Instructions[0].LeaseEpoch <= old.LeaseEpoch {
		t.Fatal("re-lease must preserve budget_id and increase lease_epoch")
	}
	oldClaims, err := kernel.VerifyCapabilityToken(oldTok, priv.Public().(ed25519.PublicKey), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var revoked bool
	if err := st.Pool().QueryRow(ctx, `SELECT revoked FROM capability_token_instances WHERE jti=$1`, oldClaims.TokenID).Scan(&revoked); err != nil {
		t.Fatal(err)
	}
	if !revoked {
		t.Fatal("expired lease must revoke previous token instance")
	}
}

func TestExtendInstructionLeaseRequiresDualTokens(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	boot := "boot-extend-" + newTestSuffix()
	id := "agent-extend-" + newTestSuffix()
	s := NewAgentServer(st.Pool(), boot, priv)
	reg, err := s.RegisterAgent(ctx, connect.NewRequest(&agentv1.RegisterAgentRequest{
		AgentId: id, BootstrapToken: boot, AgentPublicKey: "pub-extend",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnqueueInstruction(ctx, id, instructionSession, "ses-1", sessionInstructionTools, []string{"ses-1"}); err != nil {
		t.Fatal(err)
	}
	poll := connect.NewRequest(&agentv1.PollInstructionsRequest{AgentId: id, LongPollSeconds: 1})
	poll.Header().Set("Authorization", "Bearer "+reg.Msg.AccessToken)
	leased, err := s.PollInstructions(ctx, poll)
	if err != nil || len(leased.Msg.Instructions) != 1 {
		t.Fatalf("poll: %v", err)
	}
	item := leased.Msg.Instructions[0]
	missing := connect.NewRequest(&agentv1.ExtendInstructionLeaseRequest{
		InstructionId: item.InstructionId, LeaseId: item.LeaseId, LeaseEpoch: item.LeaseEpoch,
	})
	missing.Header().Set("Authorization", "Bearer "+reg.Msg.AccessToken)
	if _, err := s.ExtendInstructionLease(ctx, missing); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("missing capability want unauthenticated, got %v", err)
	}
	extend := connect.NewRequest(&agentv1.ExtendInstructionLeaseRequest{
		InstructionId: item.InstructionId, LeaseId: item.LeaseId, LeaseEpoch: item.LeaseEpoch,
	})
	extend.Header().Set("Authorization", "Bearer "+reg.Msg.AccessToken)
	extend.Header().Set(CapabilityHeader, "Bearer "+item.CapabilityToken)
	resp, err := s.ExtendInstructionLease(ctx, extend)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.CapabilityToken == "" || resp.Msg.CapabilityToken == item.CapabilityToken {
		t.Fatal("extend must rotate the capability token")
	}
	if resp.Msg.LeaseId != item.LeaseId || resp.Msg.LeaseEpoch != item.LeaseEpoch || resp.Msg.BudgetId != item.BudgetId {
		t.Fatalf("extension changed ownership: %+v", resp.Msg)
	}
}
