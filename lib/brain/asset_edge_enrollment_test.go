package brain

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"yufeng/lib/edgecore"
	"yufeng/lib/kernel"
	artifactv1 "yufeng/proto/gen/artifactv1"
	assetv1 "yufeng/proto/gen/assetv1"
	authv1 "yufeng/proto/gen/authv1"
	commonv1 "yufeng/proto/gen/commonv1"
	registryv1 "yufeng/proto/gen/registryv1"
)

func TestAdminCreatesFirstAssetAfterZeroAssetCompletion(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	username := "zero-asset-admin-" + newTestSuffix()
	if err := EnsureBootstrapAdmin(ctx, st.Pool(), username, "Admin12345"); err != nil {
		t.Fatal(err)
	}
	auth := NewAuthServer(st.Pool(), time.Hour, false, 8)
	login, err := auth.Login(ctx, connect.NewRequest(&authv1.LoginRequest{Username: username, Password: "Admin12345"}))
	if err != nil {
		t.Fatal(err)
	}
	jarvisID := "zero-asset-jarvis-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO agents(agent_id,role,last_heartbeat_at) VALUES($1,'orchestrator',now())`, jarvisID); err != nil {
		t.Fatal(err)
	}
	if err := SeedOnboardingState(ctx, st.Pool(), OnboardingStateModelLive, ""); err != nil {
		t.Fatal(err)
	}
	if missing, err := completeOnboarding(ctx, st.Pool(), completeCheck{
		AdminUserID: login.Msg.GetUser().GetUserId(), JarvisAgentID: jarvisID, ModelLive: true,
	}); err != nil || len(missing) != 0 {
		t.Fatalf("zero-asset completion missing=%v err=%v", missing, err)
	}
	created, err := NewAssetServer(st.Pool()).CreateAsset(ctx, bearerReq(login.Msg.GetToken(), &assetv1.CreateAssetRequest{
		Asset: &assetv1.Asset{DisplayName: "first protected asset"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	me, err := auth.GetMe(ctx, bearerReq(login.Msg.GetToken(), &authv1.GetMeRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if !bindingHas(me.Msg.GetAccess().GetBindings(), "asset", created.Msg.GetAsset().GetId()) {
		t.Fatalf("new asset was not added to creator scope: %v", me.Msg.GetAccess().GetBindings())
	}
}

func TestNormalizeEdgeEnrollmentSupportsReverseProxyAndExternalAuthorization(t *testing.T) {
	tests := []struct {
		name     string
		posture  commonv1.IngressPosture
		upstream string
	}{
		{name: "reverse proxy", posture: commonv1.IngressPosture_INGRESS_POSTURE_REVERSE_PROXY, upstream: "http://app:8080"},
		{name: "external authorization", posture: commonv1.IngressPosture_INGRESS_POSTURE_EXT_AUTHZ},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec, err := normalizeEdgeEnrollmentSpec(&assetv1.PutEdgeEnrollmentRequest{
				AssetId: "asset-test", UnitId: "edge-test", Posture: test.posture,
				ListenAddress: ":18080", UpstreamUrl: test.upstream, TrafficKey: "test-http",
				ModelProfile: kernel.DefaultModelProfile(),
			})
			if err != nil {
				t.Fatal(err)
			}
			if spec.request.GetPosture() != test.posture || spec.upstreamURL != test.upstream || spec.digest == "" {
				t.Fatalf("normalized enrollment=%v upstream=%q digest=%q", spec.request, spec.upstreamURL, spec.digest)
			}
		})
	}
	if _, err := normalizeEdgeEnrollmentSpec(&assetv1.PutEdgeEnrollmentRequest{
		AssetId: "asset-test", UnitId: "edge-test", Posture: commonv1.IngressPosture_INGRESS_POSTURE_MIRROR_OBSERVE,
		ListenAddress: ":18080", TrafficKey: "test-http", ModelProfile: kernel.DefaultModelProfile(),
	}); err == nil {
		t.Fatal("mirror posture was accepted by manual Edge enrollment")
	}
}

func TestPutEdgeEnrollmentIsIdempotentAndPreservesAssetSettings(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	h := newOnboardHarness(t, st)
	if err := writeAdminSystemGrant(ctx, st.Pool(), h.adminID, h.local); err != nil {
		t.Fatal(err)
	}
	if err := SeedOnboardingState(ctx, st.Pool(), OnboardingStateCompleted, h.local); err != nil {
		t.Fatal(err)
	}
	_, signingKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	server := NewAssetServer(st.Pool())
	server.signingKey = signingKey
	unitID := "manual-edge-" + newTestSuffix()
	request := &assetv1.PutEdgeEnrollmentRequest{
		AssetId: h.local, UnitId: unitID,
		Posture:       commonv1.IngressPosture_INGRESS_POSTURE_REVERSE_PROXY,
		ListenAddress: ":18080", UpstreamUrl: "http://app:8080", TrafficKey: "manual-edge",
		TrustedProxyCidrs: []string{"127.0.0.0/8"}, ModelProfile: kernel.DefaultModelProfile(),
	}
	first, err := server.PutEdgeEnrollment(ctx, bearerReq(h.adminTok, request))
	if err != nil {
		t.Fatal(err)
	}
	second, err := server.PutEdgeEnrollment(ctx, bearerReq(h.adminTok, request))
	if err != nil {
		t.Fatal(err)
	}
	if first.Msg.GetEnrollment().GetExpectedGenerationId() == "" ||
		first.Msg.GetEnrollment().GetExpectedGenerationId() != second.Msg.GetEnrollment().GetExpectedGenerationId() ||
		first.Msg.GetEnrollment().GetExpectedListenPlanVersion() != second.Msg.GetEnrollment().GetExpectedListenPlanVersion() {
		t.Fatalf("same normalized enrollment must reuse coordinates: first=%v second=%v", first.Msg, second.Msg)
	}
	if first.Msg.GetEnrollment().GetStatus() != assetv1.EdgeEnrollmentStatus_EDGE_ENROLLMENT_STATUS_WAITING_FOR_REGISTRATION {
		t.Fatalf("new enrollment status=%s", first.Msg.GetEnrollment().GetStatus())
	}
	if first.Msg.GetEnrollment().GetModelsideId() != modelSideIDForUnit(unitID) || first.Msg.GetEnrollment().GetModelProfileDigest() == "" {
		t.Fatalf("modelside projection=%v", first.Msg.GetEnrollment())
	}
	var firstGenerationRaw []byte
	if err := st.Pool().QueryRow(ctx, `SELECT envelope FROM asset_generations WHERE generation_id=$1`, first.Msg.GetEnrollment().GetExpectedGenerationId()).Scan(&firstGenerationRaw); err != nil {
		t.Fatal(err)
	}
	var firstGeneration artifactv1.AssetGeneration
	if err := protojson.Unmarshal(firstGenerationRaw, &firstGeneration); err != nil {
		t.Fatal(err)
	}
	firstDetectorID := generationArtifactID(&firstGeneration, artifactv1.Kind_KIND_DETECTOR_MANIFEST)
	firstModelProfileID := generationArtifactID(&firstGeneration, artifactv1.Kind_KIND_MODEL_PROFILE)
	if _, err := server.DeleteAsset(ctx, bearerReq(h.adminTok, &assetv1.DeleteAssetRequest{AssetId: h.local})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("asset with Edge enrollment delete want failed_precondition, got %v", err)
	}

	policy, err := server.UpdateTrafficReviewPolicy(ctx, bearerReq(h.adminTok, &assetv1.UpdateTrafficReviewPolicyRequest{
		AssetId: h.local, Mode: artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_STATISTICS_ONLY,
		ExpectedGenerationId: first.Msg.GetEnrollment().GetExpectedGenerationId(),
	}))
	if err != nil {
		t.Fatal(err)
	}
	policyEnrollment, err := server.GetEdgeEnrollment(ctx, bearerReq(h.adminTok, &assetv1.GetEdgeEnrollmentRequest{AssetId: h.local, UnitId: unitID}))
	if err != nil {
		t.Fatal(err)
	}
	if policyEnrollment.Msg.GetEnrollment().GetExpectedGenerationId() != policy.Msg.GetStatus().GetGenerationId() ||
		policyEnrollment.Msg.GetEnrollment().GetExpectedGenerationSeq() != policy.Msg.GetStatus().GetGenerationSeq() {
		t.Fatalf("policy generation was not projected into Edge enrollment: policy=%v enrollment=%v", policy.Msg.GetStatus(), policyEnrollment.Msg.GetEnrollment())
	}
	updatedRequest := proto.Clone(request).(*assetv1.PutEdgeEnrollmentRequest)
	updatedRequest.UpstreamUrl = "http://app-v2:8080"
	updated, err := server.PutEdgeEnrollment(ctx, bearerReq(h.adminTok, updatedRequest))
	if err != nil {
		t.Fatal(err)
	}
	if updated.Msg.GetEnrollment().GetExpectedListenPlanVersion() != first.Msg.GetEnrollment().GetExpectedListenPlanVersion()+1 {
		t.Fatalf("listen plan did not advance: before=%d after=%d", first.Msg.GetEnrollment().GetExpectedListenPlanVersion(), updated.Msg.GetEnrollment().GetExpectedListenPlanVersion())
	}
	if updated.Msg.GetEnrollment().GetExpectedGenerationSeq() <= policy.Msg.GetStatus().GetGenerationSeq() {
		t.Fatalf("generation did not advance after policy generation: policy=%d edge=%d", policy.Msg.GetStatus().GetGenerationSeq(), updated.Msg.GetEnrollment().GetExpectedGenerationSeq())
	}
	var generationRaw []byte
	if err := st.Pool().QueryRow(ctx, `SELECT envelope FROM asset_generations WHERE generation_id=$1`, updated.Msg.GetEnrollment().GetExpectedGenerationId()).Scan(&generationRaw); err != nil {
		t.Fatal(err)
	}
	var generation artifactv1.AssetGeneration
	if err := protojson.Unmarshal(generationRaw, &generation); err != nil {
		t.Fatal(err)
	}
	if !generationHasArtifactKind(&generation, artifactv1.Kind_KIND_TRAFFIC_REVIEW_POLICY) {
		t.Fatal("edge enrollment update discarded the existing traffic review policy")
	}
	if generationArtifactID(&generation, artifactv1.Kind_KIND_TRAFFIC_REVIEW_POLICY) != policy.Msg.GetStatus().GetPolicyDigest() {
		t.Fatal("edge enrollment update replaced the existing traffic review policy")
	}
	if generationArtifactID(&generation, artifactv1.Kind_KIND_DETECTOR_MANIFEST) != firstDetectorID ||
		generationArtifactID(&generation, artifactv1.Kind_KIND_MODEL_PROFILE) != firstModelProfileID {
		t.Fatal("edge enrollment update re-signed unchanged baseline artifacts")
	}

	if _, err := st.Pool().Exec(ctx, `UPDATE units SET token_hash='registered',last_heartbeat_at=now(),
		current_listen_plan_version=$2,current_generation_id=$3,current_generation_seq=$4 WHERE unit_id=$1`,
		unitID, updated.Msg.GetEnrollment().GetExpectedListenPlanVersion(), updated.Msg.GetEnrollment().GetExpectedGenerationId(),
		updated.Msg.GetEnrollment().GetExpectedGenerationSeq()); err != nil {
		t.Fatal(err)
	}
	online, err := server.GetEdgeEnrollment(ctx, bearerReq(h.adminTok, &assetv1.GetEdgeEnrollmentRequest{AssetId: h.local, UnitId: unitID}))
	if err != nil {
		t.Fatal(err)
	}
	if online.Msg.GetEnrollment().GetStatus() != assetv1.EdgeEnrollmentStatus_EDGE_ENROLLMENT_STATUS_ONLINE {
		t.Fatalf("converged status=%s", online.Msg.GetEnrollment().GetStatus())
	}
	capabilitiesRaw, err := protojson.Marshal(edgecore.ProducerCapabilities())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE units SET producer_capabilities=$2::jsonb WHERE unit_id=$1`, unitID, capabilitiesRaw); err != nil {
		t.Fatal(err)
	}
	desiredWindow := kernel.DefaultModelIngressWindow()
	desiredWindow.MaxItems /= 2
	windowResponse, err := server.UpdateModelIngressWindow(ctx, bearerReq(h.adminTok, &assetv1.UpdateModelIngressWindowRequest{
		AssetId: h.local, UnitId: unitID, Desired: desiredWindow,
		ExpectedListenPlanVersion: online.Msg.GetEnrollment().GetExpectedListenPlanVersion(),
	}))
	if err != nil {
		t.Fatal(err)
	}
	afterWindow, err := server.GetEdgeEnrollment(ctx, bearerReq(h.adminTok, &assetv1.GetEdgeEnrollmentRequest{AssetId: h.local, UnitId: unitID}))
	if err != nil {
		t.Fatal(err)
	}
	if afterWindow.Msg.GetEnrollment().GetExpectedListenPlanVersion() != windowResponse.Msg.GetStatus().GetDesiredListenPlanVersion() ||
		!kernel.EqualModelIngressWindow(afterWindow.Msg.GetEnrollment().GetModelIngressWindow(), desiredWindow) ||
		afterWindow.Msg.GetEnrollment().GetSpecificationDigest() == online.Msg.GetEnrollment().GetSpecificationDigest() ||
		afterWindow.Msg.GetEnrollment().GetStatus() != assetv1.EdgeEnrollmentStatus_EDGE_ENROLLMENT_STATUS_OUT_OF_SYNC {
		t.Fatalf("model window was not projected into Edge enrollment: window=%v enrollment=%v", windowResponse.Msg.GetStatus(), afterWindow.Msg.GetEnrollment())
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE units SET current_listen_plan_version=$2 WHERE unit_id=$1`, unitID, afterWindow.Msg.GetEnrollment().GetExpectedListenPlanVersion()); err != nil {
		t.Fatal(err)
	}
	afterWindowConverged, err := server.GetEdgeEnrollment(ctx, bearerReq(h.adminTok, &assetv1.GetEdgeEnrollmentRequest{AssetId: h.local, UnitId: unitID}))
	if err != nil {
		t.Fatal(err)
	}
	if afterWindowConverged.Msg.GetEnrollment().GetStatus() != assetv1.EdgeEnrollmentStatus_EDGE_ENROLLMENT_STATUS_ONLINE {
		t.Fatalf("model window convergence status=%s", afterWindowConverged.Msg.GetEnrollment().GetStatus())
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE units SET current_listen_plan_version=1 WHERE unit_id=$1`, unitID); err != nil {
		t.Fatal(err)
	}
	outOfSync, err := server.GetEdgeEnrollment(ctx, bearerReq(h.adminTok, &assetv1.GetEdgeEnrollmentRequest{AssetId: h.local, UnitId: unitID}))
	if err != nil {
		t.Fatal(err)
	}
	if outOfSync.Msg.GetEnrollment().GetStatus() != assetv1.EdgeEnrollmentStatus_EDGE_ENROLLMENT_STATUS_OUT_OF_SYNC {
		t.Fatalf("out-of-sync status=%s", outOfSync.Msg.GetEnrollment().GetStatus())
	}

	otherAsset, err := server.CreateAsset(ctx, bearerReq(h.adminTok, &assetv1.CreateAssetRequest{Asset: &assetv1.Asset{DisplayName: "other"}}))
	if err != nil {
		t.Fatal(err)
	}
	conflict := proto.Clone(request).(*assetv1.PutEdgeEnrollmentRequest)
	conflict.AssetId = otherAsset.Msg.GetAsset().GetId()
	if _, err := server.PutEdgeEnrollment(ctx, bearerReq(h.adminTok, conflict)); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("predeclared edge rebind want failed_precondition, got %v", err)
	}
}

func TestEdgeEnrollmentStatusTransitions(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name       string
		registered bool
		heartbeat  *time.Time
		listen     uint64
		generation string
		sequence   int64
		want       assetv1.EdgeEnrollmentStatus
	}{
		{name: "waiting", want: assetv1.EdgeEnrollmentStatus_EDGE_ENROLLMENT_STATUS_WAITING_FOR_REGISTRATION},
		{name: "offline", registered: true, heartbeat: timePointer(now.Add(-kernel.EdgeOnlineWindow - time.Second)), want: assetv1.EdgeEnrollmentStatus_EDGE_ENROLLMENT_STATUS_OFFLINE},
		{name: "out of sync", registered: true, heartbeat: timePointer(now), listen: 1, generation: "old", sequence: 1, want: assetv1.EdgeEnrollmentStatus_EDGE_ENROLLMENT_STATUS_OUT_OF_SYNC},
		{name: "online", registered: true, heartbeat: timePointer(now), listen: 2, generation: "gen", sequence: 3, want: assetv1.EdgeEnrollmentStatus_EDGE_ENROLLMENT_STATUS_ONLINE},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := edgeEnrollmentStatus(test.registered, test.heartbeat, now, 2, test.listen, "gen", test.generation, 3, test.sequence)
			if got != test.want {
				t.Fatalf("status=%s want %s", got, test.want)
			}
		})
	}
}

func TestManualEdgeRegistrationPreservesAssetMetadataAndRejectsHostKind(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	h := newOnboardHarness(t, st)
	if err := writeAdminSystemGrant(ctx, st.Pool(), h.adminID, h.local); err != nil {
		t.Fatal(err)
	}
	if err := SeedOnboardingState(ctx, st.Pool(), OnboardingStateCompleted, h.local); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE assets SET display_name='Protected API',access_mode='remote',
		criticality='p0',max_auto_tier='L2',labels='{"owner":"security"}'::jsonb WHERE asset_id=$1`, h.local); err != nil {
		t.Fatal(err)
	}
	publicKey, signingKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	assets := NewAssetServer(st.Pool())
	assets.signingKey = signingKey
	request := func(unitID string) *assetv1.PutEdgeEnrollmentRequest {
		return &assetv1.PutEdgeEnrollmentRequest{
			AssetId: h.local, UnitId: unitID,
			Posture:       commonv1.IngressPosture_INGRESS_POSTURE_REVERSE_PROXY,
			ListenAddress: ":18080", UpstreamUrl: "http://app:8080", TrafficKey: "registration-" + unitID,
			ModelProfile: kernel.DefaultModelProfile(),
		}
	}
	unitID := "metadata-edge-" + newTestSuffix()
	if _, err := assets.PutEdgeEnrollment(ctx, bearerReq(h.adminTok, request(unitID))); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistryServer(st.Pool(), publicKey, "manual-edge-bootstrap")
	registration := connect.NewRequest(&registryv1.RegisterRequest{
		UnitId: unitID, Kind: registryv1.UnitKind_UNIT_KIND_EDGE, Version: "test", ContractVersion: "v1",
		PubkeyHint: kernel.KeyID(publicKey), Capabilities: testEdgeCapabilities(),
		Asset: &assetv1.Asset{
			Id: h.local, DisplayName: "Edge supplied name", AccessMode: commonv1.AccessMode_ACCESS_MODE_EMBEDDED,
			Criticality: assetv1.Criticality_CRITICALITY_P2, MaxAutoTier: commonv1.Tier_TIER_L0_REPORT,
			Labels: map[string]string{"owner": "edge"},
		},
	})
	registration.Header().Set("Authorization", "Bearer manual-edge-bootstrap")
	if _, err := registry.Register(ctx, registration); err != nil {
		t.Fatal(err)
	}
	var displayName, accessMode, criticality, maxTier string
	var labels []byte
	if err := st.Pool().QueryRow(ctx, `SELECT display_name,access_mode,criticality,max_auto_tier,labels
		FROM assets WHERE asset_id=$1`, h.local).Scan(&displayName, &accessMode, &criticality, &maxTier, &labels); err != nil {
		t.Fatal(err)
	}
	if displayName != "Protected API" || accessMode != "remote" || criticality != "p0" || maxTier != "L2" || string(labels) != `{"owner": "security"}` {
		t.Fatalf("registration changed manual metadata name=%q mode=%q criticality=%q tier=%q labels=%s",
			displayName, accessMode, criticality, maxTier, labels)
	}

	hostUnitID := "host-conflict-" + newTestSuffix()
	if _, err := assets.PutEdgeEnrollment(ctx, bearerReq(h.adminTok, request(hostUnitID))); err != nil {
		t.Fatal(err)
	}
	hostRegistration := connect.NewRequest(&registryv1.RegisterRequest{
		UnitId: hostUnitID, Kind: registryv1.UnitKind_UNIT_KIND_HOST, Version: "test", ContractVersion: "v1",
		Asset: &assetv1.Asset{Id: h.local},
	})
	hostRegistration.Header().Set("Authorization", "Bearer manual-edge-bootstrap")
	if _, err := registry.Register(ctx, hostRegistration); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("edge enrollment registered as host: %v", err)
	}
}

func TestModelSideEnrollmentStatusTransitions(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name       string
		registered bool
		lastResult *time.Time
		generation string
		profile    string
		want       assetv1.EdgeEnrollmentStatus
	}{
		{name: "waiting", want: assetv1.EdgeEnrollmentStatus_EDGE_ENROLLMENT_STATUS_WAITING_FOR_REGISTRATION},
		{name: "offline without result", registered: true, want: assetv1.EdgeEnrollmentStatus_EDGE_ENROLLMENT_STATUS_OFFLINE},
		{name: "offline stale result", registered: true, lastResult: timePointer(now.Add(-kernel.EdgeOnlineWindow - time.Second)), generation: "gen", profile: "profile", want: assetv1.EdgeEnrollmentStatus_EDGE_ENROLLMENT_STATUS_OFFLINE},
		{name: "out of sync", registered: true, lastResult: timePointer(now), generation: "old", profile: "profile", want: assetv1.EdgeEnrollmentStatus_EDGE_ENROLLMENT_STATUS_OUT_OF_SYNC},
		{name: "online", registered: true, lastResult: timePointer(now), generation: "gen", profile: "profile", want: assetv1.EdgeEnrollmentStatus_EDGE_ENROLLMENT_STATUS_ONLINE},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := modelSideEnrollmentStatus(test.registered, test.lastResult, now, "gen", test.generation, "profile", test.profile)
			if got != test.want {
				t.Fatalf("status=%s want %s", got, test.want)
			}
		})
	}
}

func generationHasArtifactKind(generation *artifactv1.AssetGeneration, kind artifactv1.Kind) bool {
	for _, member := range generation.GetMembers() {
		if member.GetArtifact().GetKind() == kind {
			return true
		}
	}
	return false
}

func generationArtifactID(generation *artifactv1.AssetGeneration, kind artifactv1.Kind) string {
	for _, member := range generation.GetMembers() {
		if member.GetArtifact().GetKind() == kind {
			return member.GetArtifact().GetId()
		}
	}
	return ""
}

func timePointer(value time.Time) *time.Time { return &value }
