package brain

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"connectrpc.com/connect"

	"yufeng/lib/kernel"
	agentv1 "yufeng/proto/gen/agentv1"
)

func TestSharedBootstrapSecondAgentDenied(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tok := "shared-" + newTestSuffix()
	s := NewAgentServer(st.Pool(), tok, priv)
	first := "agent-r2a-" + newTestSuffix()
	second := "agent-r2b-" + newTestSuffix()
	if _, err := s.RegisterAgent(ctx, connect.NewRequest(&agentv1.RegisterAgentRequest{
		AgentId: first, BootstrapToken: tok, AgentPublicKey: "k",
	})); err != nil {
		t.Fatal(err)
	}
	_, err = s.RegisterAgent(ctx, connect.NewRequest(&agentv1.RegisterAgentRequest{
		AgentId: second, BootstrapToken: tok, AgentPublicKey: "k",
	}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("same token second agent_id want permission_denied, got %v", err)
	}
}

func TestProductionRejectsUnboundSharedBootstrap(t *testing.T) {
	if err := kernel.ValidateProductionAgentBootstrap(false, "compose-agent-bootstrap", ""); err == nil {
		t.Fatal("production unbound shared token must refuse start")
	}
	if err := kernel.ValidateProductionAgentBootstrap(false, "compose-agent-bootstrap", "jarvis-1"); err != nil {
		t.Fatal(err)
	}
	if err := kernel.ValidateProductionAgentBootstrap(true, "dev-agent-bootstrap-token", ""); err != nil {
		t.Fatal(err)
	}

	st, ctx := openTestStore(t)
	defer st.Close()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tok := "prod-shared-" + newTestSuffix()
	s := NewAgentServer(st.Pool(), tok, priv)
	s.allowUnboundShared = false
	_, err = s.RegisterAgent(ctx, connect.NewRequest(&agentv1.RegisterAgentRequest{
		AgentId: "stray-" + newTestSuffix(), BootstrapToken: tok, AgentPublicKey: "k",
	}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("production unbound shared token must not register, got %v", err)
	}
}

func TestProductionAgentTokensRequireAndPinClientCertificate(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	boot := "cert-boot-" + newTestSuffix()
	agentID := "cert-agent-" + newTestSuffix()
	if err := SeedAgentBootstrap(ctx, st.Pool(), agentID, boot); err != nil {
		t.Fatal(err)
	}
	s := NewAgentServer(st.Pool(), boot, priv)
	s.allowUnboundShared = false
	required := context.WithValue(ctx, clientCertRequiredKey{}, true)
	request := connect.NewRequest(&agentv1.RegisterAgentRequest{
		AgentId: agentID, BootstrapToken: boot, AgentPublicKey: "cert-agent-pub",
	})
	if _, err := s.RegisterAgent(required, request); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("registration without client certificate want unauthenticated, got %v", err)
	}
	withCert := context.WithValue(required, clientCertHashKey{}, "cert-one")
	reg, err := s.RegisterAgent(withCert, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := requireAgentToken(required, st.Pool(), reg.Msg.AccessToken); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("access without client certificate want unauthenticated, got %v", err)
	}
	wrongCert := context.WithValue(required, clientCertHashKey{}, "cert-two")
	if _, err := requireAgentToken(wrongCert, st.Pool(), reg.Msg.AccessToken); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("access with another client certificate want unauthenticated, got %v", err)
	}
	if got, err := requireAgentToken(withCert, st.Pool(), reg.Msg.AccessToken); err != nil || got != agentID {
		t.Fatalf("bound access token got=%q err=%v", got, err)
	}
	refresh := connect.NewRequest(&agentv1.RefreshAccessTokenRequest{RefreshToken: reg.Msg.RefreshToken})
	if _, err := s.RefreshAccessToken(wrongCert, refresh); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("refresh with another client certificate want unauthenticated, got %v", err)
	}
}

func TestProductionRejectsLegacyAccessTokenWithoutClientCertificateBinding(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	boot := "legacy-cert-boot-" + newTestSuffix()
	agentID := "legacy-cert-agent-" + newTestSuffix()
	s := NewAgentServer(st.Pool(), boot, priv)
	reg, err := s.RegisterAgent(ctx, connect.NewRequest(&agentv1.RegisterAgentRequest{
		AgentId: agentID, BootstrapToken: boot, AgentPublicKey: "legacy-cert-agent-pub",
	}))
	if err != nil {
		t.Fatal(err)
	}
	required := context.WithValue(ctx, clientCertRequiredKey{}, true)
	withCert := context.WithValue(required, clientCertHashKey{}, "cert-one")
	if _, err := requireAgentToken(withCert, st.Pool(), reg.Msg.AccessToken); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("legacy unbound access token want unauthenticated, got %v", err)
	}
}
