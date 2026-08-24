package brain

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/durationpb"

	"yufeng/lib/edgecore"
	"yufeng/lib/kernel"
	artifactv1 "yufeng/proto/gen/artifactv1"
	assetv1 "yufeng/proto/gen/assetv1"
	authv1 "yufeng/proto/gen/authv1"
	commonv1 "yufeng/proto/gen/commonv1"
	unitv1 "yufeng/proto/gen/unitv1"
	userv1 "yufeng/proto/gen/userv1"
)

func TestModelIngressWindowPublishesNextSignedListenPlan(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	h := newOnboardHarness(t, st)
	if err := writeAdminSystemGrant(ctx, st.Pool(), h.adminID, h.local); err != nil {
		t.Fatal(err)
	}
	if err := SeedOnboardingState(ctx, st.Pool(), OnboardingStateCompleted, h.local); err != nil {
		t.Fatal(err)
	}
	pub, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	unitID := "model-window-edge-" + newTestSuffix()
	plan := &artifactv1.UnitListenPlan{
		UnitId: unitID, Version: 1, Posture: commonv1.IngressPosture_INGRESS_POSTURE_REVERSE_PROXY,
		TrafficKey: "model-window", ListenAddress: ":18080", UpstreamUrl: "http://app:8080",
		ModelIngressWindow: kernel.DefaultModelIngressWindow(),
	}
	if err := kernel.SignUnitListenPlan(plan, privateKey); err != nil {
		t.Fatal(err)
	}
	planRaw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	capabilitiesRaw, err := protojson.Marshal(edgecore.ProducerCapabilities())
	if err != nil {
		t.Fatal(err)
	}
	healthRaw, err := protojson.Marshal(&unitv1.ProducerHealth{
		HealthyProjectionVersions:   []string{"event/v1"},
		EffectiveModelIngressWindow: kernel.DefaultModelIngressWindow(),
		ModelIngressWindowState:     unitv1.ModelIngressWindowState_MODEL_INGRESS_WINDOW_STATE_APPLIED,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO units(unit_id,kind,producer_capabilities,producer_health,current_listen_plan_version,last_heartbeat_at)
		VALUES($1,'edge',$2::jsonb,$3::jsonb,1,now())`, unitID, capabilitiesRaw, healthRaw); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO unit_assets(unit_id,asset_id) VALUES($1,$2)`, unitID, h.local); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO unit_listen_plans(unit_id,version,envelope,signed) VALUES($1,1,$2::jsonb,true)`, unitID, planRaw); err != nil {
		t.Fatal(err)
	}
	server := NewAssetServer(st.Pool())
	server.signingKey = privateKey

	desired := &artifactv1.ModelIngressWindow{MaxItems: 8192, MaxRetainedBytes: 192 << 20, MaxQueueAge: durationpb.New(3 * time.Second)}
	operatorName := "model-window-operator-" + newTestSuffix()
	createOperator := bearerReq(h.adminTok, &userv1.CreateUserRequest{
		Username: operatorName, Password: "Operator123", Role: commonv1.UserRole_USER_ROLE_OPERATOR,
	})
	setTestIdempotency(createOperator)
	operator, err := NewUserServer(st.Pool(), 8).CreateUser(ctx, createOperator)
	if err != nil {
		t.Fatal(err)
	}
	if err := insertUserGrant(ctx, st.Pool(), operator.Msg.GetUser().GetUserId(), []string{"asset.update"}, h.local); err != nil {
		t.Fatal(err)
	}
	operatorLogin, err := NewAuthServer(st.Pool(), time.Hour, false, 8).Login(ctx, connect.NewRequest(&authv1.LoginRequest{
		Username: operatorName, Password: "Operator123",
	}))
	if err != nil {
		t.Fatal(err)
	}
	operatorRequest := bearerReq(operatorLogin.Msg.GetToken(), &assetv1.UpdateModelIngressWindowRequest{
		AssetId: h.local, UnitId: unitID, Desired: desired, ExpectedListenPlanVersion: 1,
	})
	setTestIdempotency(operatorRequest)
	if _, err := server.UpdateModelIngressWindow(ctx, operatorRequest); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("operator with asset.update grant must be denied: %v", err)
	}

	request := bearerReq(h.adminTok, &assetv1.UpdateModelIngressWindowRequest{
		AssetId: h.local, UnitId: unitID, Desired: desired, ExpectedListenPlanVersion: 1,
	})
	setTestIdempotency(request)
	response, err := server.UpdateModelIngressWindow(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	status := response.Msg.GetStatus()
	if status.GetDesiredListenPlanVersion() != 2 || status.GetAppliedListenPlanVersion() != 1 ||
		status.GetState() != unitv1.ModelIngressWindowState_MODEL_INGRESS_WINDOW_STATE_CONVERGING {
		t.Fatalf("updated status=%v", status)
	}
	var signedRaw []byte
	if err := st.Pool().QueryRow(ctx, `SELECT envelope FROM unit_listen_plans WHERE unit_id=$1 AND version=2`, unitID).Scan(&signedRaw); err != nil {
		t.Fatal(err)
	}
	var signed artifactv1.UnitListenPlan
	if err := protojson.Unmarshal(signedRaw, &signed); err != nil {
		t.Fatal(err)
	}
	if !kernel.EqualModelIngressWindow(signed.GetModelIngressWindow(), desired) {
		t.Fatalf("signed desired=%v", signed.GetModelIngressWindow())
	}
	if err := kernel.VerifyUnitListenPlan(&signed, pub); err != nil {
		t.Fatal(err)
	}

	stale := bearerReq(h.adminTok, &assetv1.UpdateModelIngressWindowRequest{
		AssetId: h.local, UnitId: unitID, Desired: kernel.DefaultModelIngressWindow(), ExpectedListenPlanVersion: 1,
	})
	setTestIdempotency(stale)
	if _, err := server.UpdateModelIngressWindow(ctx, stale); connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "listen_plan_version_mismatch") {
		t.Fatalf("stale update must fail: %v", err)
	}
}

func TestModelIngressWindowStatusProjectsLocalClamp(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	h := newOnboardHarness(t, st)
	if err := writeAdminSystemGrant(ctx, st.Pool(), h.adminID, h.local); err != nil {
		t.Fatal(err)
	}
	if err := SeedOnboardingState(ctx, st.Pool(), OnboardingStateCompleted, h.local); err != nil {
		t.Fatal(err)
	}
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	unitID := "model-window-clamp-" + newTestSuffix()
	desired := &artifactv1.ModelIngressWindow{MaxItems: 4096, MaxRetainedBytes: 128 << 20, MaxQueueAge: durationpb.New(2 * time.Second)}
	plan := &artifactv1.UnitListenPlan{
		UnitId: unitID, Version: 1, Posture: commonv1.IngressPosture_INGRESS_POSTURE_REVERSE_PROXY,
		TrafficKey: "model-window", ListenAddress: ":18080", UpstreamUrl: "http://app:8080", ModelIngressWindow: desired,
	}
	if err := kernel.SignUnitListenPlan(plan, privateKey); err != nil {
		t.Fatal(err)
	}
	planRaw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	hard := &artifactv1.ModelIngressWindow{MaxItems: 2048, MaxRetainedBytes: 64 << 20, MaxQueueAge: durationpb.New(time.Second)}
	capabilitiesRaw, err := protojson.Marshal(edgecore.ProducerCapabilitiesWithModelIngressHardLimit(hard))
	if err != nil {
		t.Fatal(err)
	}
	healthRaw, err := protojson.Marshal(&unitv1.ProducerHealth{
		HealthyProjectionVersions: []string{"event/v1"}, EffectiveModelIngressWindow: hard,
		ModelIngressWindowState: unitv1.ModelIngressWindowState_MODEL_INGRESS_WINDOW_STATE_DEGRADED,
		ModelIngressDegradationReasons: []unitv1.ModelIngressDegradationReason{
			unitv1.ModelIngressDegradationReason_MODEL_INGRESS_DEGRADATION_REASON_MAX_ITEMS,
			unitv1.ModelIngressDegradationReason_MODEL_INGRESS_DEGRADATION_REASON_MAX_RETAINED_BYTES,
			unitv1.ModelIngressDegradationReason_MODEL_INGRESS_DEGRADATION_REASON_MAX_QUEUE_AGE,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO units(unit_id,kind,producer_capabilities,producer_health,current_listen_plan_version,last_heartbeat_at)
		VALUES($1,'edge',$2::jsonb,$3::jsonb,1,now())`, unitID, capabilitiesRaw, healthRaw); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO unit_assets(unit_id,asset_id) VALUES($1,$2)`, unitID, h.local); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO unit_listen_plans(unit_id,version,envelope,signed) VALUES($1,1,$2::jsonb,true)`, unitID, planRaw); err != nil {
		t.Fatal(err)
	}
	server := NewAssetServer(st.Pool())
	response, err := server.GetModelIngressWindow(ctx, bearerReq(h.adminTok, &assetv1.GetModelIngressWindowRequest{AssetId: h.local, UnitId: unitID}))
	if err != nil {
		t.Fatal(err)
	}
	status := response.Msg.GetStatus()
	if status.GetState() != unitv1.ModelIngressWindowState_MODEL_INGRESS_WINDOW_STATE_DEGRADED || !kernel.EqualModelIngressWindow(status.GetEffective(), hard) || len(status.GetDegradationReasons()) != 3 {
		t.Fatalf("clamped status=%v", status)
	}
}
