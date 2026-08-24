package brain

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentv1 "yufeng/proto/gen/agentv1"
)

func TestEnsureBootstrapJarvisSeedsPubkeyOnce(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	id := "jarvis-boot-" + newTestSuffix()
	if err := EnsureBootstrapJarvis(ctx, st.Pool(), id); err != nil {
		t.Fatal(err)
	}
	if err := EnsureBootstrapJarvis(ctx, st.Pool(), id); err != nil {
		t.Fatal(err)
	}
	var pub string
	if err := st.Pool().QueryRow(ctx, `SELECT public_key FROM agents WHERE agent_id=$1`, id).Scan(&pub); err != nil {
		t.Fatal(err)
	}
	if pub != "bootstrap" {
		t.Fatalf("first seed public_key=%q", pub)
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE agents SET public_key='real-pub' WHERE agent_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if err := EnsureBootstrapJarvis(ctx, st.Pool(), id); err != nil {
		t.Fatal(err)
	}
	if err := st.Pool().QueryRow(ctx, `SELECT public_key FROM agents WHERE agent_id=$1`, id).Scan(&pub); err != nil {
		t.Fatal(err)
	}
	if pub != "real-pub" {
		t.Fatalf("must not hijack existing pubkey, got %q", pub)
	}
}

func TestEnsureBootstrapJarvisRejectsEmpty(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	if err := EnsureBootstrapJarvis(ctx, st.Pool(), "  "); err == nil {
		t.Fatal("empty id must fail")
	}
}

func TestRegisterAgentClaimsBootstrapPlaceholder(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := "jarvis-claim-" + newTestSuffix()
	tok := "boot-claim-" + newTestSuffix()
	if err := EnsureBootstrapJarvis(ctx, st.Pool(), id); err != nil {
		t.Fatal(err)
	}
	if err := SeedAgentBootstrap(ctx, st.Pool(), id, tok); err != nil {
		t.Fatal(err)
	}
	s := NewAgentServer(st.Pool(), tok, priv)
	s.allowUnboundShared = false
	reg, err := s.RegisterAgent(ctx, connect.NewRequest(&agentv1.RegisterAgentRequest{
		AgentId: id, BootstrapToken: tok, AgentPublicKey: "real-pub",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if reg.Msg.RefreshToken == "" || reg.Msg.AccessToken == "" {
		t.Fatal("claim must return tokens")
	}
	var pub, refreshHash string
	if err := st.Pool().QueryRow(ctx, `SELECT public_key, refresh_token_hash FROM agents WHERE agent_id=$1`, id).Scan(&pub, &refreshHash); err != nil {
		t.Fatal(err)
	}
	if pub != "real-pub" {
		t.Fatalf("public_key=%q", pub)
	}
	if refreshHash == bootstrapJarvisPlaceholder {
		t.Fatal("placeholder refresh hash must be replaced")
	}
	ref, err := s.RefreshAccessToken(ctx, connect.NewRequest(&agentv1.RefreshAccessTokenRequest{RefreshToken: reg.Msg.RefreshToken}))
	if err != nil {
		t.Fatal(err)
	}
	if ref.Msg.AccessToken == "" || ref.Msg.RefreshToken == "" {
		t.Fatal("refresh after claim must rotate")
	}
	_, err = s.RegisterAgent(ctx, connect.NewRequest(&agentv1.RegisterAgentRequest{
		AgentId: id, BootstrapToken: tok, AgentPublicKey: "hijack",
	}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("reuse after claim want unauthenticated, got %v", err)
	}
}

func TestRegisterAgentClaimsPlaceholderAfterConsumedBootstrap(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := "jarvis-used-" + newTestSuffix()
	tok := "boot-used-" + newTestSuffix()
	if err := EnsureBootstrapJarvis(ctx, st.Pool(), id); err != nil {
		t.Fatal(err)
	}
	if err := SeedAgentBootstrap(ctx, st.Pool(), id, tok); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE agent_bootstrap SET used_at=now() WHERE agent_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	s := NewAgentServer(st.Pool(), tok, priv)
	s.allowUnboundShared = false
	reg, err := s.RegisterAgent(ctx, connect.NewRequest(&agentv1.RegisterAgentRequest{
		AgentId: id, BootstrapToken: tok, AgentPublicKey: "real-pub",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if reg.Msg.RefreshToken == "" {
		t.Fatal("interrupted claim must return refresh")
	}
	_, err = s.RegisterAgent(ctx, connect.NewRequest(&agentv1.RegisterAgentRequest{
		AgentId: id, BootstrapToken: tok, AgentPublicKey: "again",
	}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("second claim want unauthenticated, got %v", err)
	}
}

func TestRegisterAgentDoesNotClaimRealIdentity(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := "jarvis-real-" + newTestSuffix()
	tok := "boot-real-" + newTestSuffix()
	if err := EnsureBootstrapJarvis(ctx, st.Pool(), id); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE agents SET refresh_token_hash='already', public_key='real-pub' WHERE agent_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if err := SeedAgentBootstrap(ctx, st.Pool(), id, tok); err != nil {
		t.Fatal(err)
	}
	s := NewAgentServer(st.Pool(), tok, priv)
	s.allowUnboundShared = false
	_, err = s.RegisterAgent(ctx, connect.NewRequest(&agentv1.RegisterAgentRequest{
		AgentId: id, BootstrapToken: tok, AgentPublicKey: "hijack",
	}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("real identity want permission_denied, got %v", err)
	}
	var pub, hash string
	if err := st.Pool().QueryRow(ctx, `SELECT public_key, refresh_token_hash FROM agents WHERE agent_id=$1`, id).Scan(&pub, &hash); err != nil {
		t.Fatal(err)
	}
	if pub != "real-pub" || hash != "already" {
		t.Fatalf("must not overwrite registered agent pub=%q hash=%q", pub, hash)
	}
	var used *time.Time
	if err := st.Pool().QueryRow(ctx, `SELECT used_at FROM agent_bootstrap WHERE agent_id=$1`, id).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if used != nil {
		t.Fatal("failed claim must not consume bootstrap")
	}
}

func TestEnsureBootstrapAdminWritesNoAssetGrant(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	name := "boot-admin-" + newTestSuffix()
	if err := EnsureBootstrapAdmin(ctx, st.Pool(), name, "Admin12345"); err != nil {
		t.Fatal(err)
	}
	if err := EnsureBootstrapAdmin(ctx, st.Pool(), name, "Admin12345"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM grants g
		JOIN users u ON u.user_id=g.subject_id
		WHERE u.username=$1`, name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("bootstrap must not write grants, rows=%d", n)
	}
}
