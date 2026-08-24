package brain

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	authv1 "yufeng/proto/gen/authv1"
	commonv1 "yufeng/proto/gen/commonv1"
	userv1 "yufeng/proto/gen/userv1"
)

func TestUpdateUserCannotDisableLastEffectiveAdministrator(t *testing.T) {
	store, ctx := openTestStore(t)
	defer store.Close()
	username := "last-admin-" + newTestSuffix()
	if err := EnsureBootstrapAdmin(ctx, store.Pool(), username, "Admin12345"); err != nil {
		t.Fatal(err)
	}
	auth := NewAuthServer(store.Pool(), time.Hour, false, MinPasswordLength)
	login, err := auth.Login(ctx, connect.NewRequest(&authv1.LoginRequest{Username: username, Password: "Admin12345"}))
	if err != nil {
		t.Fatal(err)
	}
	req := connect.NewRequest(&userv1.UpdateUserRequest{
		UserId: login.Msg.GetUser().GetUserId(), User: &authv1.User{State: commonv1.UserState_USER_STATE_DISABLED},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"state"}},
	})
	req.Header().Set("Authorization", "Bearer "+login.Msg.GetToken())
	setTestIdempotency(req)
	if _, err := NewUserServer(store.Pool(), MinPasswordLength).UpdateUser(ctx, req); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("last effective administrator disable got %v", err)
	}
}
