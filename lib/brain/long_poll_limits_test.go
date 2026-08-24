package brain

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"connectrpc.com/connect"

	"yufeng/lib/kernel"
	agentv1 "yufeng/proto/gen/agentv1"
	authv1 "yufeng/proto/gen/authv1"
	sessionv1 "yufeng/proto/gen/sessionv1"
)

func TestPollInstructionsRejectsOverAgentMax(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	boot := "poll-boot-" + newTestSuffix()
	agentID := "poll-agent-" + newTestSuffix()
	s := NewAgentServer(st.Pool(), boot, priv)
	reg, err := s.RegisterAgent(ctx, connect.NewRequest(&agentv1.RegisterAgentRequest{
		AgentId: agentID, BootstrapToken: boot, AgentPublicKey: "k",
	}))
	if err != nil {
		t.Fatal(err)
	}
	over := int32(kernel.AgentLongPollMax.Seconds()) + 1
	req := connect.NewRequest(&agentv1.PollInstructionsRequest{AgentId: agentID, LongPollSeconds: over})
	req.Header().Set("Authorization", "Bearer "+reg.Msg.AccessToken)
	_, err = s.PollInstructions(ctx, req)
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("over AgentLongPollMax want invalid_argument got %v", err)
	}
}

func TestPollMessagesWaitsOrReturnsOnMessage(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	admin := "poll-admin-" + newTestSuffix()
	if err := EnsureBootstrapAdmin(ctx, st.Pool(), admin, "Admin12345"); err != nil {
		t.Fatal(err)
	}
	auth := NewAuthServer(st.Pool(), time.Hour, false, 8)
	login, err := auth.Login(ctx, connect.NewRequest(&authv1.LoginRequest{Username: admin, Password: "Admin12345"}))
	if err != nil {
		t.Fatal(err)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	agents := NewAgentServer(st.Pool(), "unused", priv)
	sess := NewSessionServer(st.Pool(), agents, "jarvis-none")
	cr := connect.NewRequest(&sessionv1.CreateSessionRequest{Title: "poll"})
	cr.Header().Set("Authorization", "Bearer "+login.Msg.Token)
	created, err := sess.CreateSession(ctx, cr)
	if err != nil {
		t.Fatal(err)
	}

	over := connect.NewRequest(&sessionv1.PollMessagesRequest{
		SessionId: created.Msg.SessionId, LongPollSeconds: int32(kernel.SessionLongPollMax.Seconds()) + 1,
	})
	over.Header().Set("Authorization", "Bearer "+login.Msg.Token)
	if _, err := sess.PollMessages(ctx, over); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("over SessionLongPollMax want invalid_argument got %v", err)
	}

	empty := connect.NewRequest(&sessionv1.PollMessagesRequest{SessionId: created.Msg.SessionId, LongPollSeconds: 1})
	empty.Header().Set("Authorization", "Bearer "+login.Msg.Token)
	start := time.Now()
	got, err := sess.PollMessages(ctx, empty)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < 800*time.Millisecond {
		t.Fatalf("empty poll must wait until timeout, elapsed=%s", elapsed)
	}
	if len(got.Msg.Messages) != 0 {
		t.Fatalf("empty session want 0 messages, got %d", len(got.Msg.Messages))
	}

	done := make(chan error, 1)
	go func() {
		time.Sleep(200 * time.Millisecond)
		send := connect.NewRequest(&sessionv1.SendMessageRequest{SessionId: created.Msg.SessionId, Content: "hello-poll"})
		send.Header().Set("Authorization", "Bearer "+login.Msg.Token)
		_, err := sess.SendMessage(ctx, send)
		done <- err
	}()
	wait := connect.NewRequest(&sessionv1.PollMessagesRequest{SessionId: created.Msg.SessionId, LongPollSeconds: 5})
	wait.Header().Set("Authorization", "Bearer "+login.Msg.Token)
	start = time.Now()
	got, err = sess.PollMessages(ctx, wait)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("poll must return when message arrives, elapsed=%s", elapsed)
	}
	if len(got.Msg.Messages) == 0 || got.Msg.Messages[0].Content != "hello-poll" {
		t.Fatalf("want arriving message, got %+v", got.Msg.Messages)
	}
}
