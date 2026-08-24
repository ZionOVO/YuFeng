package brain

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	authv1 "yufeng/proto/gen/authv1"
	commonv1 "yufeng/proto/gen/commonv1"
	sessionv1 "yufeng/proto/gen/sessionv1"
	userv1 "yufeng/proto/gen/userv1"
)

func TestSessionCrossUserDeniedAndSenderFromAuth(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	if err := EnsureBootstrapAdmin(ctx, st.Pool(), "sess-admin-"+newTestSuffix(), "Admin12345"); err != nil {
		t.Fatal(err)
	}
	auth := NewAuthServer(st.Pool(), time.Hour, false, 8)
	users := NewUserServer(st.Pool(), 8)
	adminName := ""
	if err := st.Pool().QueryRow(ctx, `SELECT username FROM users WHERE role='admin' ORDER BY created_at DESC LIMIT 1`).Scan(&adminName); err != nil {
		t.Fatal(err)
	}
	login, err := auth.Login(ctx, connect.NewRequest(&authv1.LoginRequest{Username: adminName, Password: "Admin12345"}))
	if err != nil {
		t.Fatal(err)
	}
	adminTok := login.Msg.Token
	mkUser := func(name string) string {
		t.Helper()
		req := connect.NewRequest(&userv1.CreateUserRequest{Username: name, Password: "Operator123", Role: commonv1.UserRole_USER_ROLE_OPERATOR})
		req.Header().Set("Authorization", "Bearer "+adminTok)
		setTestIdempotency(req)
		if _, err := users.CreateUser(ctx, req); err != nil {
			t.Fatal(err)
		}
		lr, err := auth.Login(ctx, connect.NewRequest(&authv1.LoginRequest{Username: name, Password: "Operator123"}))
		if err != nil {
			t.Fatal(err)
		}
		return lr.Msg.Token
	}
	tokA := mkUser("sess-a-" + newTestSuffix())
	tokB := mkUser("sess-b-" + newTestSuffix())
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	agents := NewAgentServer(st.Pool(), "unused", priv)
	sess := NewSessionServer(st.Pool(), agents, "jarvis-none")
	cr := connect.NewRequest(&sessionv1.CreateSessionRequest{Title: "t"})
	cr.Header().Set("Authorization", "Bearer "+tokA)
	created, err := sess.CreateSession(ctx, cr)
	if err != nil {
		t.Fatal(err)
	}
	send := connect.NewRequest(&sessionv1.SendMessageRequest{
		SessionId: created.Msg.SessionId, Content: "hello Bearer supersecrettokenvalue123",
	})
	send.Header().Set("Authorization", "Bearer "+tokA)
	if _, err := sess.SendMessage(ctx, send); err != nil {
		t.Fatal(err)
	}
	pollB := connect.NewRequest(&sessionv1.PollMessagesRequest{SessionId: created.Msg.SessionId})
	pollB.Header().Set("Authorization", "Bearer "+tokB)
	if _, err := sess.PollMessages(ctx, pollB); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("cross user want permission_denied got %v", err)
	}
	list := connect.NewRequest(&sessionv1.ListMessagesRequest{SessionId: created.Msg.SessionId})
	list.Header().Set("Authorization", "Bearer "+tokA)
	got, err := sess.ListMessages(ctx, list)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Msg.Messages) == 0 {
		t.Fatal("owner should read own messages")
	}
	body := got.Msg.Messages[0].Content
	if strings.Contains(body, "supersecrettokenvalue123") {
		t.Fatalf("audit/session stored plaintext token: %s", body)
	}
	if !strings.Contains(body, "[redacted]") {
		t.Fatalf("want redacted, got %s", body)
	}
}
