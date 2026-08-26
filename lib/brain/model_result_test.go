package brain

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"yufeng/lib/eventbus"
	"yufeng/lib/kernel"
	artifactv1 "yufeng/proto/gen/artifactv1"
	commonv1 "yufeng/proto/gen/commonv1"
	modelsidev1 "yufeng/proto/gen/modelsidev1"
)

func TestValidateResultAgainstSignedProfile(t *testing.T) {
	profile := kernel.DefaultModelProfile()
	partial := []*commonv1.InspectionCoverage{{Target: commonv1.InspectionSurface_INSPECTION_SURFACE_BODY, Status: commonv1.CoverageStatus_COVERAGE_STATUS_PARTIAL}}
	tests := []struct {
		name   string
		result *modelsidev1.ModelResult
		code   string
	}{
		{name: "alert", result: modelResultForProfile(profile, modelsidev1.ModelResultKind_MODEL_RESULT_KIND_MODEL_ALERT, 0.95)},
		{name: "alert with review reason", result: func() *modelsidev1.ModelResult {
			result := modelResultForProfile(profile, modelsidev1.ModelResultKind_MODEL_RESULT_KIND_MODEL_ALERT, 0.95)
			result.ReviewReasons = []modelsidev1.ReviewReason{modelsidev1.ReviewReason_REVIEW_REASON_SCORE_FLOOR}
			return result
		}(), code: "review_reason_invalid"},
		{name: "review floor", result: modelResultForProfile(profile, modelsidev1.ModelResultKind_MODEL_RESULT_KIND_REVIEW_SAMPLE, 0.5)},
		{name: "below floor", result: modelResultForProfile(profile, modelsidev1.ModelResultKind_MODEL_RESULT_KIND_REVIEW_SAMPLE, 0.49), code: "below_review_policy"},
		{name: "signed insufficient coverage", result: func() *modelsidev1.ModelResult {
			result := modelResultForProfile(profile, modelsidev1.ModelResultKind_MODEL_RESULT_KIND_REVIEW_SAMPLE, 0.1)
			result.ReviewReasons = []modelsidev1.ReviewReason{modelsidev1.ReviewReason_REVIEW_REASON_INSUFFICIENT_COVERAGE}
			result.Coverage = partial
			return result
		}()},
		{name: "unsupported coverage claim", result: func() *modelsidev1.ModelResult {
			result := modelResultForProfile(profile, modelsidev1.ModelResultKind_MODEL_RESULT_KIND_REVIEW_SAMPLE, 0.1)
			result.ReviewReasons = []modelsidev1.ReviewReason{modelsidev1.ReviewReason_REVIEW_REASON_INSUFFICIENT_COVERAGE}
			return result
		}(), code: "review_reason_not_signed"},
		{name: "alert below threshold", result: modelResultForProfile(profile, modelsidev1.ModelResultKind_MODEL_RESULT_KIND_MODEL_ALERT, 0.8), code: "result_kind_mismatch"},
		{name: "coordinate mismatch", result: func() *modelsidev1.ModelResult {
			result := modelResultForProfile(profile, modelsidev1.ModelResultKind_MODEL_RESULT_KIND_MODEL_ALERT, 0.95)
			result.ModelVersion = "other"
			return result
		}(), code: "model_coordinates_mismatch"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateResultAgainstProfile(test.result, profile)
			if test.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var rejection modelResultRejection
			if err == nil || !strings.Contains(err.Error(), test.code) || !errors.As(err, &rejection) {
				t.Fatalf("want rejection %q, got %v", test.code, err)
			}
		})
	}
}

func TestModelResultIngestionIsAtomicIdempotentAndBounded(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	jarvisID := "jarvis-model-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO agents(agent_id,refresh_token_hash,role,public_key)
		VALUES($1,'x','orchestrator','test-pub')`, jarvisID); err != nil {
		t.Fatal(err)
	}
	unitID, assetID, _ := seedUnitAsset(t, ctx, st, "model-result")
	if _, err := st.Pool().Exec(ctx, `INSERT INTO modelside_identities(modelside_id,unit_id,asset_id)
		VALUES('modelside-test',$1,$2)`, unitID, assetID); err != nil {
		t.Fatal(err)
	}
	ctx = workerCertContext(ctx, "modelside-cert-one")
	generationID, err := publishBaselineGeneration(ctx, st.Pool(), priv, nil, assetID, "administrator")
	if err != nil {
		t.Fatal(err)
	}
	generationSeq, profileDigest, profile := loadGenerationModelProfile(t, ctx, st.Pool(), generationID)
	agents := NewAgentServer(st.Pool(), "boot", priv)
	server := NewModelResultServer(st.Pool(), pub, "modelside-token", agents, jarvisID)
	now := time.Now().UTC().Truncate(time.Second)

	base := modelResultForProfile(profile, modelsidev1.ModelResultKind_MODEL_RESULT_KIND_MODEL_ALERT, 0.95)
	base.ResultId = "result-alert-" + newTestSuffix()
	base.RequestId = "request-alert-" + newTestSuffix()
	base.UnitId = unitID
	base.AssetId = assetID
	base.GenerationId = generationID
	base.GenerationSeq = generationSeq
	base.ModelProfileDigest = profileDigest
	base.Method = "POST"
	base.Route = "/login"
	base.OccurredAt = timestamppb.New(now)

	response := uploadModelResults(t, ctx, server, base)
	if response.GetAccepted() != 1 || response.GetDeduped() != 0 || len(response.GetRejected()) != 0 {
		t.Fatalf("alert response=%+v", response)
	}
	assertModelAlertLedger(t, ctx, st.Pool(), base, jarvisID)
	var caseRepresentatives int
	if err := st.Pool().QueryRow(ctx, `SELECT jsonb_array_length(c.representatives)
		FROM investigation_cases c JOIN model_result_receipts r USING(case_id) WHERE r.result_id=$1`,
		base.GetResultId()).Scan(&caseRepresentatives); err != nil {
		t.Fatal(err)
	}
	if caseRepresentatives != 0 {
		t.Fatalf("ModelResult must not be stored as ReviewCandidate, representatives=%d", caseRepresentatives)
	}
	var pinnedCertificate string
	if err := st.Pool().QueryRow(ctx, `SELECT client_cert_sha256 FROM modelside_identities WHERE modelside_id='modelside-test'`).Scan(&pinnedCertificate); err != nil {
		t.Fatal(err)
	}
	if pinnedCertificate != "modelside-cert-one" {
		t.Fatalf("pinned ModelSide certificate=%q", pinnedCertificate)
	}

	wrongAsset := proto.Clone(base).(*modelsidev1.ModelResult)
	wrongAsset.ResultId = "result-wrong-binding-" + newTestSuffix()
	wrongAsset.AssetId = "another-asset"
	if got := uploadModelResults(t, ctx, server, wrongAsset); len(got.GetRejected()) != 1 || got.GetRejected()[0].GetCode() != "modelside_binding_mismatch" {
		t.Fatalf("wrong ModelSide binding=%+v", got)
	}
	wrongCertificateContext := context.WithValue(workerCertContext(ctx, "modelside-cert-two"), clientCertRequiredKey{}, true)
	request := connect.NewRequest(&modelsidev1.UploadResultsRequest{ModelsideId: "modelside-test", Results: []*modelsidev1.ModelResult{base}})
	request.Header().Set("Authorization", "Bearer modelside-token")
	if _, err := server.UploadResults(wrongCertificateContext, request); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("wrong ModelSide certificate code=%s err=%v", connect.CodeOf(err), err)
	}

	response = uploadModelResults(t, ctx, server, base)
	if response.GetAccepted() != 0 || response.GetDeduped() != 1 || len(response.GetRejected()) != 0 {
		t.Fatalf("dedupe response=%+v", response)
	}
	assertModelAlertLedger(t, ctx, st.Pool(), base, jarvisID)

	var instructionsBefore int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM agent_instructions WHERE agent_id=$1`, jarvisID).Scan(&instructionsBefore); err != nil {
		t.Fatal(err)
	}
	review := func(suffix, route string, score float64, reasons ...modelsidev1.ReviewReason) *modelsidev1.ModelResult {
		result := proto.Clone(base).(*modelsidev1.ModelResult)
		result.ResultId = "result-review-" + suffix + "-" + newTestSuffix()
		result.RequestId = "request-review-" + suffix + "-" + newTestSuffix()
		result.Kind = modelsidev1.ModelResultKind_MODEL_RESULT_KIND_REVIEW_SAMPLE
		result.Score = score
		result.Route = route
		result.ReviewReasons = reasons
		return result
	}
	first := review("a-low", "/review/a", 0.61, modelsidev1.ReviewReason_REVIEW_REASON_SCORE_FLOOR)
	if got := uploadModelResults(t, ctx, server, first); got.GetAccepted() != 1 {
		t.Fatalf("first review=%+v", got)
	}
	higher := review("a-high", "/review/a", 0.75, modelsidev1.ReviewReason_REVIEW_REASON_SCORE_FLOOR)
	if got := uploadModelResults(t, ctx, server, higher); got.GetAccepted() != 1 {
		t.Fatalf("replacement review=%+v", got)
	}
	notNew := review("a-not-new", "/review/a", 0.8, modelsidev1.ReviewReason_REVIEW_REASON_NEW_ROUTE)
	if got := uploadModelResults(t, ctx, server, notNew); len(got.GetRejected()) != 1 || got.GetRejected()[0].GetCode() != "review_route_not_new" {
		t.Fatalf("existing route claim=%+v", got)
	}
	for _, route := range []string{"/review/b", "/review/c", "/review/d"} {
		if got := uploadModelResults(t, ctx, server, review(strings.TrimPrefix(route, "/review/"), route, 0.6)); got.GetAccepted() != 1 {
			t.Fatalf("review route %s=%+v", route, got)
		}
	}
	full := uploadModelResults(t, ctx, server, review("e", "/review/e", 0.6))
	if len(full.GetRejected()) != 1 || full.GetRejected()[0].GetCode() != "review_window_full" {
		t.Fatalf("full review window=%+v", full)
	}
	var representatives int
	var highest float64
	if err := st.Pool().QueryRow(ctx, `SELECT count(*),max(score) FROM model_review_representatives
		WHERE unit_id=$1 AND model_profile_digest=$2`, unitID, profileDigest).Scan(&representatives, &highest); err != nil {
		t.Fatal(err)
	}
	if representatives != int(profile.GetMaxReviewPerUnit()) || highest != higher.GetScore() {
		t.Fatalf("representatives=%d highest=%v", representatives, highest)
	}
	var instructionsAfter int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM agent_instructions WHERE agent_id=$1`, jarvisID).Scan(&instructionsAfter); err != nil {
		t.Fatal(err)
	}
	if instructionsAfter != instructionsBefore {
		t.Fatalf("review samples woke Jarvis: before=%d after=%d", instructionsBefore, instructionsAfter)
	}
}

func modelResultForProfile(profile *artifactv1.ModelProfile, kind modelsidev1.ModelResultKind, score float64) *modelsidev1.ModelResult {
	return &modelsidev1.ModelResult{
		Kind: kind, Score: score, ModelProfileId: profile.GetProfileId(), ModelGroup: profile.GetModelGroup(),
		ModelType: profile.GetModelType(), ModelVersion: profile.GetModelVersion(),
	}
}

func loadGenerationModelProfile(t *testing.T, ctx context.Context, pool *pgxpool.Pool, generationID string) (int64, string, *artifactv1.ModelProfile) {
	t.Helper()
	var sequence int64
	var raw []byte
	if err := pool.QueryRow(ctx, `SELECT generation_seq,envelope FROM asset_generations WHERE generation_id=$1`, generationID).Scan(&sequence, &raw); err != nil {
		t.Fatal(err)
	}
	var generation artifactv1.AssetGeneration
	if err := protojson.Unmarshal(raw, &generation); err != nil {
		t.Fatal(err)
	}
	for _, member := range generation.GetMembers() {
		artifact := member.GetArtifact()
		if artifact == nil || artifact.GetKind() != artifactv1.Kind_KIND_MODEL_PROFILE {
			continue
		}
		var profile artifactv1.ModelProfile
		if err := protojson.Unmarshal(artifact.GetPayload(), &profile); err != nil {
			t.Fatal(err)
		}
		return sequence, artifact.GetId(), &profile
	}
	t.Fatal("baseline generation has no model profile")
	return 0, "", nil
}

func uploadModelResults(t *testing.T, ctx context.Context, server *ModelResultServer, results ...*modelsidev1.ModelResult) *modelsidev1.UploadResultsResponse {
	t.Helper()
	request := connect.NewRequest(&modelsidev1.UploadResultsRequest{ModelsideId: "modelside-test", Results: results})
	request.Header().Set("Authorization", "Bearer modelside-token")
	response, err := server.UploadResults(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	return response.Msg
}

func assertModelAlertLedger(t *testing.T, ctx context.Context, pool *pgxpool.Pool, result *modelsidev1.ModelResult, jarvisID string) {
	t.Helper()
	var eventID, caseID, resultKind string
	if err := pool.QueryRow(ctx, `SELECT event_id,case_id,result_kind FROM model_result_receipts WHERE result_id=$1`, result.GetResultId()).Scan(&eventID, &caseID, &resultKind); err != nil {
		t.Fatal(err)
	}
	if resultKind != "MODEL_ALERT" || eventID == "" || caseID == "" {
		t.Fatalf("receipt event=%q case=%q kind=%q", eventID, caseID, resultKind)
	}
	var events, inferences, tickets, cases, outboxRows, instructions int
	var payload, attackClass string
	if err := pool.QueryRow(ctx, `SELECT count(*),min(payload::text) FROM events WHERE event_id=$1`, eventID).Scan(&events, &payload); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*),min(attack_class) FROM model_inferences WHERE event_id=$1 AND request_id=$2`, eventID, result.GetRequestId()).Scan(&inferences, &attackClass); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM check_tickets WHERE event_id=$1 AND status='ready'`, eventID).Scan(&tickets); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM investigation_cases WHERE case_id=$1`, caseID).Scan(&cases); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE topic=$1 AND dedupe_key=$2`, eventbus.SubjectModelResults, result.GetResultId()).Scan(&outboxRows); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_instructions WHERE kind=$1 AND agent_id=$2`, instructionTriage, jarvisID).Scan(&instructions); err != nil {
		t.Fatal(err)
	}
	if events != 1 || inferences != 1 || attackClass != commonv1.AttackClass_ATTACK_CLASS_UNMAPPED.String() || tickets != 1 || cases != 1 || outboxRows != 1 || instructions != 1 {
		t.Fatalf("ledger counts events=%d inferences=%d attackClass=%q tickets=%d cases=%d outbox=%d instructions=%d", events, inferences, attackClass, tickets, cases, outboxRows, instructions)
	}
	for _, forbidden := range []string{"headers", "query_parameters", "query_redacted", "body"} {
		if strings.Contains(payload, forbidden) {
			t.Fatalf("event retained forbidden raw traffic field %q: %s", forbidden, payload)
		}
	}
}
