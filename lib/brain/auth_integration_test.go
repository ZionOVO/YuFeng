package brain

import (
	"context"
	"os"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	"yufeng/lib/store"

	authv1 "yufeng/proto/gen/authv1"
	commonv1 "yufeng/proto/gen/commonv1"
	userv1 "yufeng/proto/gen/userv1"
)

func TestAuthAndUserFlow(t *testing.T) {
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
	if err := EnsureBootstrapAdmin(ctx, st.Pool(), "itadmin", "Admin12345"); err != nil {
		t.Fatal(err)
	}

	auth := NewAuthServer(st.Pool(), time.Hour, false, 8)
	loginReq := connect.NewRequest(&authv1.LoginRequest{Username: "itadmin", Password: "Admin12345"})
	loginResp, err := auth.Login(ctx, loginReq)
	if err != nil {
		t.Fatal(err)
	}
	adminToken := loginResp.Msg.Token
	if adminToken == "" {
		t.Fatal("登录未返回令牌")
	}

	getMeReq := connect.NewRequest(&authv1.GetMeRequest{})
	getMeReq.Header().Set("Authorization", "Bearer "+adminToken)
	me, err := auth.GetMe(ctx, getMeReq)
	if err != nil {
		t.Fatal(err)
	}
	if me.Msg.User.Role != commonv1.UserRole_USER_ROLE_ADMIN {
		t.Fatalf("角色 = %s", me.Msg.User.Role)
	}

	users := NewUserServer(st.Pool(), 8)
	createReq := connect.NewRequest(&userv1.CreateUserRequest{
		Username: "itoperator-" + time.Now().Format("150405"), Password: "Operator123", Role: commonv1.UserRole_USER_ROLE_OPERATOR,
	})
	createReq.Header().Set("Authorization", "Bearer "+adminToken)
	setTestIdempotency(createReq)
	created, err := users.CreateUser(ctx, createReq)
	if err != nil {
		t.Fatal(err)
	}

	updateReq := connect.NewRequest(&userv1.UpdateUserRequest{
		UserId:     created.Msg.User.UserId,
		User:       &authv1.User{State: commonv1.UserState_USER_STATE_DISABLED},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"state"}},
	})
	updateReq.Header().Set("Authorization", "Bearer "+adminToken)
	setTestIdempotency(updateReq)
	updated, err := users.UpdateUser(ctx, updateReq)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Msg.User.State != commonv1.UserState_USER_STATE_DISABLED {
		t.Fatalf("状态 = %s, want disabled", updated.Msg.User.State)
	}
}
