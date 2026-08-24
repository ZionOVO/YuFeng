package brain

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"yufeng/lib/eventbus"
	"yufeng/lib/kernel"
	artifactv1 "yufeng/proto/gen/artifactv1"
	commonv1 "yufeng/proto/gen/commonv1"
	eventv1 "yufeng/proto/gen/eventv1"
	modelsidev1 "yufeng/proto/gen/modelsidev1"
	"yufeng/proto/gen/modelsidev1/modelsidev1connect"
)

type modelResultRejection struct {
	code string
}

type modelSidePrincipal struct {
	id                    string
	unitID                string
	assetID               string
	clientCertificateHash string
	certificateBound      bool
}

func (e modelResultRejection) Error() string { return e.code }

// ModelResultServer 接收 Edge 邻近 ModelSide 的无原文结果，并在 Brain 内完成幂等入账。
type ModelResultServer struct {
	pool     *pgxpool.Pool
	pub      ed25519.PublicKey
	token    string
	agents   *AgentServer
	jarvisID string
}

// NewModelResultServer 构造模型结果服务；token 是与单元令牌分离的 ModelSide 凭据。
func NewModelResultServer(pool *pgxpool.Pool, pub ed25519.PublicKey, token string, agents *AgentServer, jarvisID string) *ModelResultServer {
	if strings.TrimSpace(jarvisID) == "" {
		jarvisID = defaultJarvisAgentID
	}
	return &ModelResultServer{pool: pool, pub: pub, token: strings.TrimSpace(token), agents: agents, jarvisID: jarvisID}
}

// Handler 返回 ModelResultService 处理器。
func (s *ModelResultServer) Handler() (string, http.Handler) {
	return modelsidev1connect.NewModelResultServiceHandler(s, handlerOptions()...)
}

// UploadResults 逐项执行独立事务，使一个坏结果不会回滚同批已接受结果。
func (s *ModelResultServer) UploadResults(ctx context.Context, req *connect.Request[modelsidev1.UploadResultsRequest]) (*connect.Response[modelsidev1.UploadResultsResponse], error) {
	principal, err := s.authenticate(ctx, req)
	if err != nil {
		return nil, err
	}
	if len(req.Msg.GetResults()) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("results are required"))
	}
	if len(req.Msg.GetResults()) > kernel.ModelSideUploadBatchMax {
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("model result batch exceeds limit"))
	}
	response := &modelsidev1.UploadResultsResponse{}
	for _, result := range req.Msg.GetResults() {
		outcome, err := s.ingestResult(ctx, principal, result)
		if err != nil {
			var rejection modelResultRejection
			if errors.As(err, &rejection) {
				response.Rejected = append(response.Rejected, &modelsidev1.RejectedResult{ResultId: modelResultID(result), Code: rejection.code})
				continue
			}
			return nil, err
		}
		if outcome == "deduped" {
			response.Deduped++
		} else {
			response.Accepted++
		}
	}
	return connect.NewResponse(response), nil
}

func (s *ModelResultServer) authenticate(ctx context.Context, req *connect.Request[modelsidev1.UploadResultsRequest]) (modelSidePrincipal, error) {
	if err := requireAgentClientCert(ctx); err != nil {
		return modelSidePrincipal{}, err
	}
	if s.token == "" {
		return modelSidePrincipal{}, connect.NewError(connect.CodeUnavailable, errors.New("modelside authentication is not configured"))
	}
	raw := bearerToken(req.Header().Get("Authorization"))
	want := sha256.Sum256([]byte(s.token))
	got := sha256.Sum256([]byte(raw))
	if raw == "" || subtle.ConstantTimeCompare(want[:], got[:]) != 1 {
		return modelSidePrincipal{}, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid modelside credential"))
	}
	modelsideID := strings.TrimSpace(req.Msg.GetModelsideId())
	if modelsideID == "" || len(modelsideID) > 128 {
		return modelSidePrincipal{}, connect.NewError(connect.CodeInvalidArgument, errors.New("modelside_id is invalid"))
	}
	var principal modelSidePrincipal
	principal.id = modelsideID
	var storedCertificateHash string
	if err := s.pool.QueryRow(ctx, `SELECT unit_id,asset_id,client_cert_sha256
		FROM modelside_identities WHERE modelside_id=$1`, modelsideID).
		Scan(&principal.unitID, &principal.assetID, &storedCertificateHash); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return modelSidePrincipal{}, connect.NewError(connect.CodeUnauthenticated, errors.New("modelside identity is not declared"))
		}
		return modelSidePrincipal{}, err
	}
	principal.clientCertificateHash = clientCertHash(ctx)
	if principal.clientCertificateHash == "" {
		principal.clientCertificateHash = "dev-insecure"
	}
	if storedCertificateHash != "" {
		if storedCertificateHash != principal.clientCertificateHash {
			return modelSidePrincipal{}, connect.NewError(connect.CodeUnauthenticated, errors.New("modelside client certificate does not match identity"))
		}
		principal.certificateBound = true
	}
	return principal, nil
}

func (s *ModelResultServer) ingestResult(ctx context.Context, principal modelSidePrincipal, result *modelsidev1.ModelResult) (string, error) {
	if err := validateModelResultShape(result); err != nil {
		return "", err
	}
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(result)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	payloadDigest := "sha256:" + hex.EncodeToString(sum[:])
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "modelside-identity:"+principal.id); err != nil {
		return "", err
	}
	if err := bindModelSidePrincipal(ctx, tx, principal, result); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "model-result:"+result.GetResultId()); err != nil {
		return "", err
	}
	var storedDigest string
	err = tx.QueryRow(ctx, `SELECT payload_digest FROM model_result_receipts WHERE result_id=$1`, result.GetResultId()).Scan(&storedDigest)
	if err == nil {
		if storedDigest != payloadDigest {
			return "", modelResultRejection{code: "result_id_conflict"}
		}
		if err := tx.Commit(ctx); err != nil {
			return "", err
		}
		return "deduped", nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	profile, err := loadResultModelProfile(ctx, tx, result, s.pub)
	if err != nil {
		return "", err
	}
	if err := validateResultAgainstProfile(result, profile); err != nil {
		return "", err
	}
	bound, err := unitBindsAsset(ctx, tx, result.GetUnitId(), result.GetAssetId())
	if err != nil {
		return "", err
	}
	if !bound {
		return "", modelResultRejection{code: "unit_asset_mismatch"}
	}
	if err := validateNewRouteReview(ctx, tx, result); err != nil {
		return "", err
	}
	windowStart, replaceRepresentative, err := reserveReviewWindow(ctx, tx, result, profile)
	if err != nil {
		return "", err
	}
	event, eventDigest, err := buildModelResultEvent(result)
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO event_receipts(event_id,payload_digest,occurred_at)
		VALUES($1,$2,$3)`, event.GetId(), eventDigest, event.GetOccurredAt().AsTime()); err != nil {
		return "", err
	}
	eventRaw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(event)
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO events(event_id,unit_id,asset_id,request_id,occurred_at,source,kind,verdict,
		payload,release_traces,generation_seq,payload_digest)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb,'[]'::jsonb,$10,$11)`,
		event.GetId(), result.GetUnitId(), result.GetAssetId(), result.GetRequestId(), event.GetOccurredAt().AsTime(),
		event.GetSource(), eventKindString(event.GetKind()), eventVerdictString(event.GetVerdict()), eventRaw,
		result.GetGenerationSeq(), eventDigest); err != nil {
		return "", err
	}
	if result.GetKind() == modelsidev1.ModelResultKind_MODEL_RESULT_KIND_MODEL_ALERT {
		frozen, err := freezeCheckTicket(ctx, tx, result.GetUnitId(), event)
		if err != nil {
			return "", err
		}
		if frozen.Status != checkTicketReady {
			return "", fmt.Errorf("freeze model alert ticket: %s", frozen.QuarantineReason)
		}
	}
	inferenceID, err := newID("inf")
	if err != nil {
		return "", err
	}
	threshold := profile.GetReviewFloor()
	if result.GetKind() == modelsidev1.ModelResultKind_MODEL_RESULT_KIND_MODEL_ALERT {
		threshold = profile.GetAlertThreshold()
	}
	if _, err := tx.Exec(ctx, `INSERT INTO model_inferences(inference_id,event_id,model_group,model_type,model_version,
		threshold,score,model_profile_digest,request_id,result_kind)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, inferenceID, event.GetId(), result.GetModelGroup(),
		result.GetModelType(), result.GetModelVersion(), threshold, result.GetScore(), result.GetModelProfileDigest(),
		result.GetRequestId(), modelResultKindName(result.GetKind())); err != nil {
		return "", err
	}
	caseID, err := attachModelResultToCase(ctx, tx, result)
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO model_result_receipts(result_id,payload_digest,modelside_id,request_id,
		unit_id,asset_id,generation_id,generation_seq,model_profile_digest,result_kind,score,method,route,window_start,
		event_id,case_id,occurred_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
		result.GetResultId(), payloadDigest, principal.id, result.GetRequestId(), result.GetUnitId(), result.GetAssetId(),
		result.GetGenerationId(), result.GetGenerationSeq(), result.GetModelProfileDigest(), modelResultKindName(result.GetKind()),
		result.GetScore(), strings.ToUpper(result.GetMethod()), result.GetRoute(), windowStart, event.GetId(), caseID,
		result.GetOccurredAt().AsTime()); err != nil {
		return "", err
	}
	if result.GetKind() == modelsidev1.ModelResultKind_MODEL_RESULT_KIND_REVIEW_SAMPLE {
		if err := storeReviewRepresentative(ctx, tx, result, windowStart, caseID, replaceRepresentative); err != nil {
			return "", err
		}
	}
	if err := writeOutbox(ctx, tx, eventbus.SubjectModelResults, result.GetResultId(), map[string]any{
		"result_id": result.GetResultId(), "event_id": event.GetId(), "case_id": caseID,
		"kind": modelResultKindName(result.GetKind()), "asset_id": result.GetAssetId(),
	}); err != nil {
		return "", err
	}
	if result.GetKind() == modelsidev1.ModelResultKind_MODEL_RESULT_KIND_MODEL_ALERT && s.agents != nil {
		triage := &TelemetryServer{pool: s.pool, agents: s.agents, jarvisID: s.jarvisID}
		if err := triage.maybeEnqueueTriage(ctx, tx, event); err != nil {
			return "", err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return "accepted", nil
}

func bindModelSidePrincipal(ctx context.Context, tx pgx.Tx, principal modelSidePrincipal, result *modelsidev1.ModelResult) error {
	if result.GetUnitId() != principal.unitID || result.GetAssetId() != principal.assetID {
		return modelResultRejection{code: "modelside_binding_mismatch"}
	}
	if principal.certificateBound {
		return nil
	}
	command, err := tx.Exec(ctx, `UPDATE modelside_identities
		SET client_cert_sha256=$2,certificate_bound_at=now()
		WHERE modelside_id=$1 AND client_cert_sha256=''`, principal.id, principal.clientCertificateHash)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 1 {
		return nil
	}
	var storedCertificateHash string
	if err := tx.QueryRow(ctx, `SELECT client_cert_sha256 FROM modelside_identities WHERE modelside_id=$1`, principal.id).
		Scan(&storedCertificateHash); err != nil {
		return err
	}
	if storedCertificateHash != principal.clientCertificateHash {
		return modelResultRejection{code: "modelside_certificate_mismatch"}
	}
	return nil
}

func validateModelResultShape(result *modelsidev1.ModelResult) error {
	if result == nil {
		return modelResultRejection{code: "invalid_result"}
	}
	if strings.TrimSpace(result.GetResultId()) == "" || len(result.GetResultId()) > 128 ||
		strings.TrimSpace(result.GetRequestId()) == "" || strings.TrimSpace(result.GetUnitId()) == "" ||
		strings.TrimSpace(result.GetAssetId()) == "" || strings.TrimSpace(result.GetGenerationId()) == "" ||
		result.GetGenerationSeq() <= 0 || strings.TrimSpace(result.GetModelProfileDigest()) == "" ||
		strings.TrimSpace(result.GetMethod()) == "" || strings.TrimSpace(result.GetRoute()) == "" {
		return modelResultRejection{code: "invalid_result"}
	}
	if math.IsNaN(result.GetScore()) || math.IsInf(result.GetScore(), 0) || result.GetScore() < 0 || result.GetScore() > 1 {
		return modelResultRejection{code: "invalid_score"}
	}
	if result.GetOccurredAt() == nil || result.GetOccurredAt().CheckValid() != nil {
		return modelResultRejection{code: "invalid_occurred_at"}
	}
	return nil
}

func loadResultModelProfile(ctx context.Context, tx pgx.Tx, result *modelsidev1.ModelResult, pub ed25519.PublicKey) (*artifactv1.ModelProfile, error) {
	var assetID string
	var sequence int64
	var raw []byte
	var signed bool
	if err := tx.QueryRow(ctx, `SELECT asset_id,generation_seq,envelope,signed FROM asset_generations
		WHERE generation_id=$1`, result.GetGenerationId()).Scan(&assetID, &sequence, &raw, &signed); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, modelResultRejection{code: "generation_not_found"}
		}
		return nil, err
	}
	if !signed || assetID != result.GetAssetId() || sequence != result.GetGenerationSeq() {
		return nil, modelResultRejection{code: "generation_mismatch"}
	}
	var generation artifactv1.AssetGeneration
	if err := protojson.Unmarshal(raw, &generation); err != nil || kernel.VerifyGeneration(&generation, pub) != nil {
		return nil, modelResultRejection{code: "generation_signature_invalid"}
	}
	for _, member := range generation.GetMembers() {
		artifact := member.GetArtifact()
		if artifact == nil || artifact.GetKind() != artifactv1.Kind_KIND_MODEL_PROFILE || artifact.GetId() != result.GetModelProfileDigest() {
			continue
		}
		if artifact.GetPayloadSchema() != modelProfileSchema || kernel.VerifyArtifact(artifact, pub) != nil {
			return nil, modelResultRejection{code: "model_profile_signature_invalid"}
		}
		var profile artifactv1.ModelProfile
		if err := protojson.Unmarshal(artifact.GetPayload(), &profile); err != nil {
			return nil, modelResultRejection{code: "model_profile_invalid"}
		}
		normalized, err := kernel.NormalizeModelProfile(&profile)
		if err != nil {
			return nil, modelResultRejection{code: "model_profile_invalid"}
		}
		return normalized, nil
	}
	return nil, modelResultRejection{code: "model_profile_mismatch"}
}

func validateResultAgainstProfile(result *modelsidev1.ModelResult, profile *artifactv1.ModelProfile) error {
	if result.GetModelProfileId() != profile.GetProfileId() || result.GetModelGroup() != profile.GetModelGroup() ||
		result.GetModelType() != profile.GetModelType() || result.GetModelVersion() != profile.GetModelVersion() {
		return modelResultRejection{code: "model_coordinates_mismatch"}
	}
	if result.GetScore() >= profile.GetAlertThreshold() {
		if result.GetKind() != modelsidev1.ModelResultKind_MODEL_RESULT_KIND_MODEL_ALERT {
			return modelResultRejection{code: "result_kind_mismatch"}
		}
		if len(result.GetReviewReasons()) != 0 {
			return modelResultRejection{code: "review_reason_invalid"}
		}
		return nil
	}
	if result.GetKind() != modelsidev1.ModelResultKind_MODEL_RESULT_KIND_REVIEW_SAMPLE {
		return modelResultRejection{code: "result_kind_mismatch"}
	}
	eligible := result.GetScore() >= profile.GetReviewFloor()
	for _, reason := range result.GetReviewReasons() {
		switch reason {
		case modelsidev1.ReviewReason_REVIEW_REASON_SCORE_FLOOR:
			if result.GetScore() >= profile.GetReviewFloor() {
				eligible = true
			}
		case modelsidev1.ReviewReason_REVIEW_REASON_NEW_ROUTE:
			if !profile.GetReviewNewRoutes() {
				return modelResultRejection{code: "review_reason_not_signed"}
			}
			eligible = true
		case modelsidev1.ReviewReason_REVIEW_REASON_INSUFFICIENT_COVERAGE:
			if !profile.GetReviewInsufficientCoverage() || !hasInsufficientCoverage(result.GetCoverage()) {
				return modelResultRejection{code: "review_reason_not_signed"}
			}
			eligible = true
		default:
			return modelResultRejection{code: "review_reason_invalid"}
		}
	}
	if !eligible {
		return modelResultRejection{code: "below_review_policy"}
	}
	return nil
}

func validateNewRouteReview(ctx context.Context, tx pgx.Tx, result *modelsidev1.ModelResult) error {
	if result.GetKind() != modelsidev1.ModelResultKind_MODEL_RESULT_KIND_REVIEW_SAMPLE ||
		!hasReviewReason(result.GetReviewReasons(), modelsidev1.ReviewReason_REVIEW_REASON_NEW_ROUTE) {
		return nil
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM model_result_receipts
		WHERE unit_id=$1 AND model_profile_digest=$2 AND method=$3 AND route=$4
	)`, result.GetUnitId(), result.GetModelProfileDigest(), strings.ToUpper(strings.TrimSpace(result.GetMethod())), result.GetRoute()).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return modelResultRejection{code: "review_route_not_new"}
	}
	return nil
}

func hasReviewReason(reasons []modelsidev1.ReviewReason, want modelsidev1.ReviewReason) bool {
	for _, reason := range reasons {
		if reason == want {
			return true
		}
	}
	return false
}

func hasInsufficientCoverage(items []*commonv1.InspectionCoverage) bool {
	for _, item := range items {
		if item == nil {
			continue
		}
		switch item.GetStatus() {
		case commonv1.CoverageStatus_COVERAGE_STATUS_PARTIAL,
			commonv1.CoverageStatus_COVERAGE_STATUS_UNSUPPORTED,
			commonv1.CoverageStatus_COVERAGE_STATUS_ERROR:
			return true
		}
	}
	return false
}

func reserveReviewWindow(ctx context.Context, tx pgx.Tx, result *modelsidev1.ModelResult, profile *artifactv1.ModelProfile) (*time.Time, bool, error) {
	if result.GetKind() != modelsidev1.ModelResultKind_MODEL_RESULT_KIND_REVIEW_SAMPLE {
		return nil, false, nil
	}
	seconds := int64(profile.GetReviewWindowSeconds())
	occurred := result.GetOccurredAt().AsTime().UTC()
	start := time.Unix(occurred.Unix()/seconds*seconds, 0).UTC()
	lockMaterial := strings.Join([]string{result.GetUnitId(), result.GetModelProfileDigest(), start.Format(time.RFC3339)}, "\x00")
	lockDigest := sha256.Sum256([]byte(lockMaterial))
	lockKey := hex.EncodeToString(lockDigest[:])
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "model-review:"+lockKey); err != nil {
		return nil, false, err
	}
	method := strings.ToUpper(strings.TrimSpace(result.GetMethod()))
	var existingScore float64
	err := tx.QueryRow(ctx, `SELECT score FROM model_review_representatives
		WHERE unit_id=$1 AND model_profile_digest=$2 AND window_start=$3 AND method=$4 AND route=$5`,
		result.GetUnitId(), result.GetModelProfileDigest(), start, method, result.GetRoute()).Scan(&existingScore)
	if err == nil {
		if result.GetScore() <= existingScore {
			return nil, false, modelResultRejection{code: "review_sample_not_representative"}
		}
		return &start, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, err
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM model_review_representatives
		WHERE unit_id=$1 AND model_profile_digest=$2 AND window_start=$3`,
		result.GetUnitId(), result.GetModelProfileDigest(), start).Scan(&count); err != nil {
		return nil, false, err
	}
	if count >= int(profile.GetMaxReviewPerUnit()) {
		return nil, false, modelResultRejection{code: "review_window_full"}
	}
	return &start, false, nil
}

func buildModelResultEvent(result *modelsidev1.ModelResult) (*eventv1.Event, string, error) {
	eventID, err := newID("evt")
	if err != nil {
		return nil, "", err
	}
	kind := eventv1.Kind_KIND_MODEL_REVIEW_SAMPLE
	verdict := eventv1.Verdict_VERDICT_OBSERVE
	triageReason := commonv1.TriageReason_TRIAGE_REASON_UNSPECIFIED
	if result.GetKind() == modelsidev1.ModelResultKind_MODEL_RESULT_KIND_MODEL_ALERT {
		kind = eventv1.Kind_KIND_MODEL_ALERT
		verdict = eventv1.Verdict_VERDICT_ESCALATE
		triageReason = commonv1.TriageReason_TRIAGE_REASON_SUSPECTED_MISS
	}
	event := &eventv1.Event{
		Id: eventID, OccurredAt: result.GetOccurredAt(), AssetId: result.GetAssetId(), UnitId: result.GetUnitId(),
		RequestId: result.GetRequestId(), Source: "yufeng-modelside", Kind: kind, Verdict: verdict,
		Traffic:  &eventv1.Event_Http{Http: &eventv1.Http{Method: strings.ToUpper(strings.TrimSpace(result.GetMethod())), Path: result.GetRoute()}},
		Coverage: cloneCoverage(result.GetCoverage()), GenerationId: result.GetGenerationId(), GenerationSeq: result.GetGenerationSeq(),
		TriageReason: triageReason, Labels: map[string]string{
			"model_profile_id": result.GetModelProfileId(), "model_profile_digest": result.GetModelProfileDigest(),
			"model_version": result.GetModelVersion(), "model_result_kind": modelResultKindName(result.GetKind()),
		},
	}
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(event)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(raw)
	return event, "sha256:" + hex.EncodeToString(sum[:]), nil
}

func attachModelResultToCase(ctx context.Context, tx pgx.Tx, result *modelsidev1.ModelResult) (string, error) {
	clusterID := modelResultClusterID(result)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1),34)`, "model-case:"+clusterID); err != nil {
		return "", err
	}
	var caseID, state string
	var representatives []byte
	err := tx.QueryRow(ctx, `SELECT case_id,state,representatives FROM investigation_cases
		WHERE asset_id=$1 AND module_id='traffic-interception' AND cluster_id=$2
		  AND state NOT IN ('resolved','failed','evidence_expired')
		ORDER BY created_at LIMIT 1 FOR UPDATE`, result.GetAssetId(), clusterID).Scan(&caseID, &state, &representatives)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	resultRaw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(result)
	if err != nil {
		return "", err
	}
	priority := int(math.Round(result.GetScore() * 100))
	if priority < 1 {
		priority = 1
	}
	if caseID == "" {
		caseID, err = newID("case")
		if err != nil {
			return "", err
		}
		representativeJSON, err := json.Marshal([]json.RawMessage{resultRaw})
		if err != nil {
			return "", err
		}
		title := fmt.Sprintf("%s %s 模型旁路复核", strings.ToUpper(result.GetMethod()), result.GetRoute())
		if _, err := tx.Exec(ctx, `INSERT INTO investigation_cases(
			case_id,module_id,asset_id,cluster_id,state,priority,title,summary,representatives)
			VALUES($1,'traffic-interception',$2,$3,'open',$4,$5,$6,$7::jsonb)`,
			caseID, result.GetAssetId(), clusterID, priority, title, "Edge 邻近模型的无原文异步结果", representativeJSON); err != nil {
			return "", err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO case_activities(case_id,kind,ref_id,summary)
			VALUES($1,'created',$2,'案件由模型旁路结果聚合创建')`, caseID, result.GetResultId()); err != nil {
			return "", err
		}
		return caseID, nil
	}
	if state != "open" {
		return caseID, nil
	}
	var current []json.RawMessage
	if len(representatives) > 0 {
		if err := json.Unmarshal(representatives, &current); err != nil {
			return "", fmt.Errorf("decode model case representatives: %w", err)
		}
	}
	replace := len(current) == 0
	if len(current) > 0 {
		var previous modelsidev1.ModelResult
		if protojson.Unmarshal(current[0], &previous) != nil || result.GetScore() > previous.GetScore() {
			replace = true
		}
	}
	if replace {
		next, err := json.Marshal([]json.RawMessage{resultRaw})
		if err != nil {
			return "", err
		}
		if _, err := tx.Exec(ctx, `UPDATE investigation_cases SET priority=GREATEST(priority,$2),
			representatives=$3::jsonb,updated_at=now() WHERE case_id=$1`, caseID, priority, next); err != nil {
			return "", err
		}
	}
	return caseID, nil
}

func storeReviewRepresentative(ctx context.Context, tx pgx.Tx, result *modelsidev1.ModelResult, windowStart *time.Time, caseID string, replace bool) error {
	if windowStart == nil {
		return errors.New("review window is missing")
	}
	method := strings.ToUpper(strings.TrimSpace(result.GetMethod()))
	if replace {
		_, err := tx.Exec(ctx, `UPDATE model_review_representatives SET result_id=$6,score=$7,case_id=$8,updated_at=now()
			WHERE unit_id=$1 AND model_profile_digest=$2 AND window_start=$3 AND method=$4 AND route=$5`,
			result.GetUnitId(), result.GetModelProfileDigest(), *windowStart, method, result.GetRoute(),
			result.GetResultId(), result.GetScore(), caseID)
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO model_review_representatives(
		unit_id,model_profile_digest,window_start,method,route,result_id,score,case_id)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, result.GetUnitId(), result.GetModelProfileDigest(), *windowStart,
		method, result.GetRoute(), result.GetResultId(), result.GetScore(), caseID)
	return err
}

func modelResultClusterID(result *modelsidev1.ModelResult) string {
	raw := strings.Join([]string{result.GetAssetId(), strings.ToUpper(result.GetMethod()), result.GetRoute(), result.GetModelProfileId()}, "\x00")
	sum := sha256.Sum256([]byte(raw))
	return "model:" + hex.EncodeToString(sum[:16])
}

func modelResultKindName(kind modelsidev1.ModelResultKind) string {
	switch kind {
	case modelsidev1.ModelResultKind_MODEL_RESULT_KIND_MODEL_ALERT:
		return "MODEL_ALERT"
	case modelsidev1.ModelResultKind_MODEL_RESULT_KIND_REVIEW_SAMPLE:
		return "REVIEW_SAMPLE"
	default:
		return ""
	}
}

func modelResultID(result *modelsidev1.ModelResult) string {
	if result == nil {
		return ""
	}
	return result.GetResultId()
}
