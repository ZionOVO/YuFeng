package brain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"yufeng/lib/edgecore"
	"yufeng/lib/kernel"
	artifactv1 "yufeng/proto/gen/artifactv1"
	assetv1 "yufeng/proto/gen/assetv1"
	commonv1 "yufeng/proto/gen/commonv1"
)

type edgeEnrollmentSpec struct {
	request       *assetv1.PutEdgeEnrollmentRequest
	digest        string
	listenAddress string
	upstreamURL   string
}

func normalizeEdgeEnrollmentSpec(msg *assetv1.PutEdgeEnrollmentRequest) (edgeEnrollmentSpec, error) {
	if msg == nil {
		return edgeEnrollmentSpec{}, errors.New("edge enrollment is required")
	}
	assetID := strings.TrimSpace(msg.GetAssetId())
	unitID := strings.TrimSpace(msg.GetUnitId())
	trafficKey := strings.TrimSpace(msg.GetTrafficKey())
	if assetID == "" || len(assetID) > 128 || assetID == "bootstrap" {
		return edgeEnrollmentSpec{}, errors.New("asset_id is invalid")
	}
	if unitID == "" || len(unitID) > 64 {
		return edgeEnrollmentSpec{}, errors.New("unit_id must be 1-64 characters")
	}
	if trafficKey == "" || len(trafficKey) > 256 {
		return edgeEnrollmentSpec{}, errors.New("traffic_key is invalid")
	}
	trustedProxyCIDRs, err := kernel.NormalizeTrustedProxyCIDRs(msg.GetTrustedProxyCidrs())
	if err != nil {
		return edgeEnrollmentSpec{}, err
	}
	profile, err := kernel.NormalizeModelProfile(msg.GetModelProfile())
	if err != nil {
		return edgeEnrollmentSpec{}, err
	}
	window, err := kernel.ModelIngressWindowOrDefault(msg.GetModelIngressWindow())
	if err != nil {
		return edgeEnrollmentSpec{}, err
	}
	listenAddress := strings.TrimSpace(msg.GetListenAddress())
	upstreamURL := strings.TrimSpace(msg.GetUpstreamUrl())
	switch msg.GetPosture() {
	case commonv1.IngressPosture_INGRESS_POSTURE_REVERSE_PROXY:
	case commonv1.IngressPosture_INGRESS_POSTURE_EXT_AUTHZ:
		if upstreamURL != "" {
			return edgeEnrollmentSpec{}, errors.New("upstream_url is only valid for reverse proxy")
		}
	default:
		return edgeEnrollmentSpec{}, errors.New("posture must be reverse proxy or ext_authz")
	}
	normalized := &assetv1.PutEdgeEnrollmentRequest{
		AssetId: assetID, UnitId: unitID, Posture: msg.GetPosture(), ListenAddress: listenAddress,
		UpstreamUrl: upstreamURL, TrafficKey: trafficKey, TrustedProxyCidrs: trustedProxyCIDRs,
		ModelProfile: profile, ModelIngressWindow: window,
	}
	plan := &artifactv1.UnitListenPlan{
		UnitId: unitID, Posture: msg.GetPosture(), TrafficKey: trafficKey, Version: 1,
		ListenAddress: listenAddress, UpstreamUrl: upstreamURL,
		ClientSource: clientSourcePolicy(trustedProxyCIDRs), ModelIngressWindow: window,
	}
	if err := edgecore.ValidateUnitListenPlan(plan); err != nil {
		return edgeEnrollmentSpec{}, err
	}
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(normalized)
	if err != nil {
		return edgeEnrollmentSpec{}, err
	}
	sum := sha256.Sum256(raw)
	return edgeEnrollmentSpec{
		request: normalized, digest: "sha256:" + hex.EncodeToString(sum[:]),
		listenAddress: listenAddress, upstreamURL: upstreamURL,
	}, nil
}

// PutEdgeEnrollment 为既有资产签发或复用人工 Edge 接入制品。
func (s *AssetServer) PutEdgeEnrollment(ctx context.Context, req *connect.Request[assetv1.PutEdgeEnrollmentRequest]) (*connect.Response[assetv1.PutEdgeEnrollmentResponse], error) {
	user, err := s.caller(ctx, req)
	if err != nil {
		return nil, err
	}
	if err := requireAssetAdmin(user); err != nil {
		return nil, err
	}
	spec, err := normalizeEdgeEnrollmentSpec(req.Msg)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := authorizeWrite(ctx, s.pool, user, "asset.update", "asset", spec.request.GetAssetId(), false); err != nil {
		return nil, err
	}
	resp := &assetv1.PutEdgeEnrollmentResponse{}
	err = idempotentProto(ctx, s.pool, "asset.put_edge_enrollment:"+spec.request.GetAssetId(), idempotencyKey(req.Header()), spec.request, resp, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "edge-enrollment:"+spec.request.GetUnitId()); err != nil {
			return err
		}
		if err := prepareEdgeEnrollmentBinding(ctx, tx, spec.request.GetUnitId(), spec.request.GetAssetId()); err != nil {
			return err
		}
		var storedAssetID, storedDigest string
		err := tx.QueryRow(ctx, `SELECT asset_id,specification_digest FROM edge_enrollments WHERE unit_id=$1`, spec.request.GetUnitId()).Scan(&storedAssetID, &storedDigest)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if storedAssetID != "" && storedAssetID != spec.request.GetAssetId() {
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("edge unit already binds another asset"))
		}
		if storedDigest == spec.digest {
			enrollment, err := loadEdgeEnrollment(ctx, tx, spec.request.GetAssetId(), spec.request.GetUnitId())
			if err != nil {
				return err
			}
			resp.Enrollment = enrollment
			return nil
		}
		listenVersion, generationID, generationSeq, err := publishEdgeEnrollmentArtifacts(ctx, tx,
			spec.request.GetUnitId(), spec.request.GetAssetId(), spec.request.GetPosture(), spec.request.GetTrafficKey(),
			spec.request.GetTrustedProxyCidrs(), spec.listenAddress, spec.upstreamURL,
			spec.request.GetModelIngressWindow(), spec.request.GetModelProfile(), s.signingKey, s.artifactSigner, user.GetUserId())
		if err != nil {
			return err
		}
		trustedRaw, err := json.Marshal(spec.request.GetTrustedProxyCidrs())
		if err != nil {
			return err
		}
		profileRaw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(spec.request.GetModelProfile())
		if err != nil {
			return err
		}
		windowRaw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(spec.request.GetModelIngressWindow())
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO edge_enrollments(
			unit_id,asset_id,posture,listen_address,upstream_url,traffic_key,trusted_proxy_cidrs,
			model_profile,model_ingress_window,modelside_id,specification_digest,expected_listen_plan_version,
			expected_generation_id,expected_generation_seq)
			VALUES($1,$2,$3,$4,$5,$6,$7::jsonb,$8::jsonb,$9::jsonb,$10,$11,$12,$13,$14)
			ON CONFLICT(unit_id) DO UPDATE SET posture=EXCLUDED.posture,listen_address=EXCLUDED.listen_address,
			upstream_url=EXCLUDED.upstream_url,traffic_key=EXCLUDED.traffic_key,
			trusted_proxy_cidrs=EXCLUDED.trusted_proxy_cidrs,model_profile=EXCLUDED.model_profile,
			model_ingress_window=EXCLUDED.model_ingress_window,specification_digest=EXCLUDED.specification_digest,
			expected_listen_plan_version=EXCLUDED.expected_listen_plan_version,
			expected_generation_id=EXCLUDED.expected_generation_id,expected_generation_seq=EXCLUDED.expected_generation_seq,
			updated_at=now()`, spec.request.GetUnitId(), spec.request.GetAssetId(), spec.request.GetPosture().String(),
			spec.listenAddress, spec.upstreamURL, spec.request.GetTrafficKey(), trustedRaw, profileRaw, windowRaw,
			modelSideIDForUnit(spec.request.GetUnitId()), spec.digest, listenVersion, generationID, generationSeq); err != nil {
			return err
		}
		if err := appendAuditTx(ctx, tx, "user", user.GetUserId(), "asset.edge_enrollment.put", "asset", spec.request.GetAssetId(), map[string]any{
			"unit_id": spec.request.GetUnitId(), "specification_digest": spec.digest,
			"listen_plan_version": listenVersion, "generation_id": generationID, "generation_seq": generationSeq,
		}); err != nil {
			return err
		}
		enrollment, err := loadEdgeEnrollment(ctx, tx, spec.request.GetAssetId(), spec.request.GetUnitId())
		if err != nil {
			return err
		}
		resp.Enrollment = enrollment
		return nil
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

// GetEdgeEnrollment 返回调用者资产范围内的单项人工 Edge 接入投影。
func (s *AssetServer) GetEdgeEnrollment(ctx context.Context, req *connect.Request[assetv1.GetEdgeEnrollmentRequest]) (*connect.Response[assetv1.GetEdgeEnrollmentResponse], error) {
	user, err := s.caller(ctx, req)
	if err != nil {
		return nil, err
	}
	access, err := loadEffectiveAccess(ctx, s.pool, user)
	if err != nil {
		return nil, err
	}
	scope := scopeFromAccess(access)
	if !scope.hasTool("console.read") || !scope.coversAsset(req.Msg.GetAssetId()) {
		return nil, objectDenied()
	}
	enrollment, err := loadEdgeEnrollment(ctx, s.pool, strings.TrimSpace(req.Msg.GetAssetId()), strings.TrimSpace(req.Msg.GetUnitId()))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, objectDenied()
	}
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&assetv1.GetEdgeEnrollmentResponse{Enrollment: enrollment}), nil
}

func prepareEdgeEnrollmentBinding(ctx context.Context, tx pgx.Tx, unitID, assetID string) error {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM assets WHERE asset_id=$1)`, assetID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("asset must be created before edge enrollment"))
	}
	var enrolledAssetID string
	err := tx.QueryRow(ctx, `SELECT asset_id FROM edge_enrollments WHERE unit_id=$1`, unitID).Scan(&enrolledAssetID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if enrolledAssetID != "" && enrolledAssetID != assetID {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("edge unit already binds another asset"))
	}
	var kind string
	err = tx.QueryRow(ctx, `SELECT kind FROM units WHERE unit_id=$1`, unitID).Scan(&kind)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if kind != "" && kind != "edge" {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("unit is not an edge"))
	}
	var primaryAssetID string
	err = tx.QueryRow(ctx, `SELECT asset_id FROM unit_assets WHERE unit_id=$1 AND is_primary ORDER BY created_at LIMIT 1`, unitID).Scan(&primaryAssetID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if primaryAssetID != "" && primaryAssetID != assetID {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("edge unit already has another primary asset"))
	}
	if _, err := tx.Exec(ctx, `INSERT INTO units(unit_id,kind,version,contract_version)
		VALUES($1,'edge','','v1') ON CONFLICT(unit_id) DO NOTHING`, unitID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO unit_assets(unit_id,asset_id,relation,is_primary)
		VALUES($1,$2,'protects',true) ON CONFLICT(unit_id,asset_id) DO UPDATE SET is_primary=true`, unitID, assetID)
	return err
}

func syncEdgeEnrollmentListenPlan(ctx context.Context, tx pgx.Tx, assetID, unitID string, version uint64, window *artifactv1.ModelIngressWindow) error {
	var posture, listenAddress, upstreamURL, trafficKey string
	var trustedRaw, profileRaw []byte
	err := tx.QueryRow(ctx, `SELECT posture,listen_address,upstream_url,traffic_key,trusted_proxy_cidrs,model_profile
		FROM edge_enrollments WHERE asset_id=$1 AND unit_id=$2`, assetID, unitID).
		Scan(&posture, &listenAddress, &upstreamURL, &trafficKey, &trustedRaw, &profileRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var trustedProxyCIDRs []string
	if err := json.Unmarshal(trustedRaw, &trustedProxyCIDRs); err != nil {
		return err
	}
	var profile artifactv1.ModelProfile
	if err := protojson.Unmarshal(profileRaw, &profile); err != nil {
		return err
	}
	spec, err := normalizeEdgeEnrollmentSpec(&assetv1.PutEdgeEnrollmentRequest{
		AssetId: assetID, UnitId: unitID, Posture: ingressPostureEnum(posture), ListenAddress: listenAddress,
		UpstreamUrl: upstreamURL, TrafficKey: trafficKey, TrustedProxyCidrs: trustedProxyCIDRs,
		ModelProfile: &profile, ModelIngressWindow: window,
	})
	if err != nil {
		return err
	}
	windowRaw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(spec.request.GetModelIngressWindow())
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE edge_enrollments SET model_ingress_window=$3::jsonb,
		specification_digest=$4,expected_listen_plan_version=$5,updated_at=now()
		WHERE asset_id=$1 AND unit_id=$2`, assetID, unitID, windowRaw, spec.digest, version)
	return err
}

type enrollmentScanner interface {
	Scan(...any) error
}

const edgeEnrollmentSelect = `SELECT e.asset_id,e.unit_id,e.posture,e.listen_address,e.upstream_url,e.traffic_key,
	e.trusted_proxy_cidrs,e.model_profile,e.model_ingress_window,e.modelside_id,e.specification_digest,
	e.expected_listen_plan_version,e.expected_generation_id,e.expected_generation_seq,
	COALESCE(u.token_hash,''),u.last_heartbeat_at,u.current_listen_plan_version,u.current_generation_id,u.current_generation_seq,
	mi.certificate_bound_at,mr.created_at,COALESCE(mr.generation_id,''),COALESCE(mr.model_profile_digest,''),g.envelope
	FROM edge_enrollments e
	JOIN units u ON u.unit_id=e.unit_id
	JOIN asset_generations g ON g.generation_id=e.expected_generation_id
	LEFT JOIN modelside_identities mi ON mi.modelside_id=e.modelside_id
	LEFT JOIN LATERAL (
		SELECT created_at,generation_id,model_profile_digest FROM model_result_receipts
		WHERE modelside_id=e.modelside_id ORDER BY created_at DESC LIMIT 1
	) mr ON true`

func loadEdgeEnrollment(ctx context.Context, db dbTX, assetID, unitID string) (*assetv1.EdgeEnrollment, error) {
	row := db.QueryRow(ctx, edgeEnrollmentSelect+` WHERE e.asset_id=$1 AND e.unit_id=$2`, assetID, unitID)
	return scanEdgeEnrollment(row, time.Now())
}

func listEdgeEnrollments(ctx context.Context, db dbTX, assetID string) ([]*assetv1.EdgeEnrollment, error) {
	rows, err := db.Query(ctx, edgeEnrollmentSelect+` WHERE e.asset_id=$1 ORDER BY e.unit_id`, assetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*assetv1.EdgeEnrollment{}
	now := time.Now()
	for rows.Next() {
		enrollment, err := scanEdgeEnrollment(rows, now)
		if err != nil {
			return nil, err
		}
		out = append(out, enrollment)
	}
	return out, rows.Err()
}

func scanEdgeEnrollment(row enrollmentScanner, now time.Time) (*assetv1.EdgeEnrollment, error) {
	var assetID, unitID, posture, listenAddress, upstreamURL, trafficKey string
	var trustedRaw, profileRaw, windowRaw, modelSideID, specDigest string
	var expectedListenVersion, expectedGenerationSeq int64
	var expectedGenerationID, tokenHash, currentGenerationID string
	var currentListenVersion, currentGenerationSeq int64
	var lastHeartbeat, modelSideBoundAt, modelSideLastResult *time.Time
	var modelSideGenerationID, modelSideProfileDigest, generationRaw string
	if err := row.Scan(&assetID, &unitID, &posture, &listenAddress, &upstreamURL, &trafficKey,
		&trustedRaw, &profileRaw, &windowRaw, &modelSideID, &specDigest,
		&expectedListenVersion, &expectedGenerationID, &expectedGenerationSeq,
		&tokenHash, &lastHeartbeat, &currentListenVersion, &currentGenerationID, &currentGenerationSeq,
		&modelSideBoundAt, &modelSideLastResult, &modelSideGenerationID, &modelSideProfileDigest, &generationRaw); err != nil {
		return nil, err
	}
	var trusted []string
	if err := json.Unmarshal([]byte(trustedRaw), &trusted); err != nil {
		return nil, err
	}
	var profile artifactv1.ModelProfile
	if err := protojson.Unmarshal([]byte(profileRaw), &profile); err != nil {
		return nil, err
	}
	var window artifactv1.ModelIngressWindow
	if err := protojson.Unmarshal([]byte(windowRaw), &window); err != nil {
		return nil, err
	}
	profileDigest, err := modelProfileDigestFromGeneration([]byte(generationRaw))
	if err != nil {
		return nil, err
	}
	enrollment := &assetv1.EdgeEnrollment{
		AssetId: assetID, UnitId: unitID, Posture: ingressPostureEnum(posture), ListenAddress: listenAddress,
		UpstreamUrl: upstreamURL, TrafficKey: trafficKey, TrustedProxyCidrs: trusted,
		ModelProfile: &profile, ModelIngressWindow: &window, ModelsideId: modelSideID,
		SpecificationDigest: specDigest, ExpectedGenerationId: expectedGenerationID,
		ExpectedGenerationSeq: expectedGenerationSeq, CurrentGenerationId: currentGenerationID,
		CurrentGenerationSeq: currentGenerationSeq, ModelProfileDigest: profileDigest,
	}
	if expectedListenVersion > 0 {
		enrollment.ExpectedListenPlanVersion = uint64(expectedListenVersion)
	}
	if currentListenVersion > 0 {
		enrollment.CurrentListenPlanVersion = uint64(currentListenVersion)
	}
	if lastHeartbeat != nil {
		enrollment.LastHeartbeatAt = timestamppb.New(*lastHeartbeat)
	}
	if modelSideLastResult != nil {
		enrollment.ModelsideLastResultAt = timestamppb.New(*modelSideLastResult)
	}
	enrollment.Status = edgeEnrollmentStatus(tokenHash != "", lastHeartbeat, now,
		enrollment.ExpectedListenPlanVersion, enrollment.CurrentListenPlanVersion,
		expectedGenerationID, currentGenerationID, expectedGenerationSeq, currentGenerationSeq)
	enrollment.ModelsideStatus = modelSideEnrollmentStatus(modelSideBoundAt != nil, modelSideLastResult, now,
		expectedGenerationID, modelSideGenerationID, profileDigest, modelSideProfileDigest)
	return enrollment, nil
}

func edgeEnrollmentStatus(registered bool, heartbeat *time.Time, now time.Time, expectedListen, currentListen uint64,
	expectedGeneration, currentGeneration string, expectedSequence, currentSequence int64,
) assetv1.EdgeEnrollmentStatus {
	if !registered {
		return assetv1.EdgeEnrollmentStatus_EDGE_ENROLLMENT_STATUS_WAITING_FOR_REGISTRATION
	}
	if heartbeat == nil || now.Sub(*heartbeat) > kernel.EdgeOnlineWindow {
		return assetv1.EdgeEnrollmentStatus_EDGE_ENROLLMENT_STATUS_OFFLINE
	}
	if expectedListen != currentListen || expectedGeneration != currentGeneration || expectedSequence != currentSequence {
		return assetv1.EdgeEnrollmentStatus_EDGE_ENROLLMENT_STATUS_OUT_OF_SYNC
	}
	return assetv1.EdgeEnrollmentStatus_EDGE_ENROLLMENT_STATUS_ONLINE
}

func modelSideEnrollmentStatus(registered bool, lastResult *time.Time, now time.Time,
	expectedGeneration, actualGeneration, expectedProfile, actualProfile string,
) assetv1.EdgeEnrollmentStatus {
	if !registered {
		return assetv1.EdgeEnrollmentStatus_EDGE_ENROLLMENT_STATUS_WAITING_FOR_REGISTRATION
	}
	if lastResult == nil || now.Sub(*lastResult) > kernel.EdgeOnlineWindow {
		return assetv1.EdgeEnrollmentStatus_EDGE_ENROLLMENT_STATUS_OFFLINE
	}
	if expectedGeneration != actualGeneration || expectedProfile != actualProfile {
		return assetv1.EdgeEnrollmentStatus_EDGE_ENROLLMENT_STATUS_OUT_OF_SYNC
	}
	return assetv1.EdgeEnrollmentStatus_EDGE_ENROLLMENT_STATUS_ONLINE
}

func modelProfileDigestFromGeneration(raw []byte) (string, error) {
	var generation artifactv1.AssetGeneration
	if err := protojson.Unmarshal(raw, &generation); err != nil {
		return "", err
	}
	for _, member := range generation.GetMembers() {
		artifact := member.GetArtifact()
		if artifact != nil && artifact.GetKind() == artifactv1.Kind_KIND_MODEL_PROFILE {
			return artifact.GetId(), nil
		}
	}
	return "", errors.New("asset generation is missing model profile")
}
