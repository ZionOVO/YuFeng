package brain

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"yufeng/lib/edgecore"
	"yufeng/lib/kernel"
	artifactv1 "yufeng/proto/gen/artifactv1"
	commonv1 "yufeng/proto/gen/commonv1"
	onboardingv1 "yufeng/proto/gen/onboardingv1"
)

// deploymentSpec 是管理员提交后由 Brain 规范化的人工 Edge 部署合同。
type deploymentSpec struct {
	Request       *onboardingv1.PutDeploymentSpecificationRequest
	Digest        string
	ListenAddress string
	UpstreamURL   string
	ModelProfile  *artifactv1.ModelProfile
}

func normalizeDeploymentSpec(msg *onboardingv1.PutDeploymentSpecificationRequest) (deploymentSpec, error) {
	if msg == nil {
		return deploymentSpec{}, errors.New("deployment specification is required")
	}
	unitID := strings.TrimSpace(msg.GetUnitId())
	assetID := strings.TrimSpace(msg.GetAssetId())
	trafficKey := strings.TrimSpace(msg.GetTrafficKey())
	if unitID == "" || len(unitID) > 64 {
		return deploymentSpec{}, errors.New("unit_id must be 1-64 characters")
	}
	if assetID == "" || len(assetID) > 128 || assetID == "bootstrap" {
		return deploymentSpec{}, errors.New("asset_id is invalid")
	}
	if trafficKey == "" || len(trafficKey) > 256 {
		return deploymentSpec{}, errors.New("traffic_key is invalid")
	}
	trustedProxyCIDRs, err := kernel.NormalizeTrustedProxyCIDRs(msg.GetTrustedProxyCidrs())
	if err != nil {
		return deploymentSpec{}, err
	}
	profile, err := modelProfileFromSpecification(msg.GetModelProfile())
	if err != nil {
		return deploymentSpec{}, err
	}
	window, err := kernel.ModelIngressWindowOrDefault(msg.GetModelIngressWindow())
	if err != nil {
		return deploymentSpec{}, err
	}
	normalized := &onboardingv1.PutDeploymentSpecificationRequest{
		UnitId: unitID, AssetId: assetID, Posture: msg.GetPosture(), TrafficKey: trafficKey,
		TrustedProxyCidrs: trustedProxyCIDRs, ModelProfile: modelProfileSpecification(profile), ModelIngressWindow: window,
	}
	var listenAddress, upstreamURL string
	switch msg.GetPosture() {
	case commonv1.IngressPosture_INGRESS_POSTURE_REVERSE_PROXY:
		target := msg.GetReverseProxy()
		if target == nil || msg.GetExtAuthz() != nil {
			return deploymentSpec{}, errors.New("reverse proxy posture requires reverse_proxy target")
		}
		listenAddress = strings.TrimSpace(target.GetListenAddress())
		upstreamURL = strings.TrimSpace(target.GetUpstreamUrl())
		normalized.Target = &onboardingv1.PutDeploymentSpecificationRequest_ReverseProxy{ReverseProxy: &onboardingv1.ReverseProxyTarget{
			ListenAddress: listenAddress, UpstreamUrl: upstreamURL,
		}}
	case commonv1.IngressPosture_INGRESS_POSTURE_EXT_AUTHZ:
		target := msg.GetExtAuthz()
		if target == nil || msg.GetReverseProxy() != nil {
			return deploymentSpec{}, errors.New("ext_authz posture requires ext_authz target")
		}
		listenAddress = strings.TrimSpace(target.GetListenAddress())
		normalized.Target = &onboardingv1.PutDeploymentSpecificationRequest_ExtAuthz{ExtAuthz: &onboardingv1.ExtAuthzTarget{ListenAddress: listenAddress}}
	default:
		return deploymentSpec{}, errors.New("posture must be reverse proxy or ext_authz")
	}
	plan := &artifactv1.UnitListenPlan{
		UnitId: unitID, Posture: msg.GetPosture(), TrafficKey: trafficKey, Version: 1,
		ListenAddress: listenAddress, UpstreamUrl: upstreamURL, ClientSource: clientSourcePolicy(trustedProxyCIDRs),
		ModelIngressWindow: window,
	}
	if err := edgecore.ValidateUnitListenPlan(plan); err != nil {
		return deploymentSpec{}, err
	}
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(normalized)
	if err != nil {
		return deploymentSpec{}, err
	}
	sum := sha256.Sum256(raw)
	return deploymentSpec{
		Request: normalized, Digest: "sha256:" + hex.EncodeToString(sum[:]), ListenAddress: listenAddress,
		UpstreamURL: upstreamURL, ModelProfile: profile,
	}, nil
}

func modelProfileFromSpecification(spec *onboardingv1.ModelProfileSpecification) (*artifactv1.ModelProfile, error) {
	if spec == nil {
		return nil, errors.New("model_profile is required")
	}
	return kernel.NormalizeModelProfile(&artifactv1.ModelProfile{
		ProfileId: spec.GetProfileId(), ModelGroup: spec.GetModelGroup(), ModelType: spec.GetModelType(),
		ModelVersion: spec.GetModelVersion(), AlertThreshold: spec.GetAlertThreshold(), ReviewFloor: spec.GetReviewFloor(),
		ReviewWindowSeconds: spec.GetReviewWindowSeconds(), MaxReviewPerUnit: spec.GetMaxReviewPerUnit(),
		MaxReviewPerRoute: spec.GetMaxReviewPerRoute(), DedupeRule: artifactv1.ModelDedupeRule(spec.GetDedupeRule()),
		AllowedHeaders: spec.GetAllowedHeaders(), MaxBodyBytes: spec.GetMaxBodyBytes(), ReviewNewRoutes: spec.GetReviewNewRoutes(),
		ReviewInsufficientCoverage: spec.GetReviewInsufficientCoverage(),
	})
}

func modelProfileSpecification(profile *artifactv1.ModelProfile) *onboardingv1.ModelProfileSpecification {
	return &onboardingv1.ModelProfileSpecification{
		ProfileId: profile.GetProfileId(), ModelGroup: profile.GetModelGroup(), ModelType: profile.GetModelType(),
		ModelVersion: profile.GetModelVersion(), AlertThreshold: profile.GetAlertThreshold(), ReviewFloor: profile.GetReviewFloor(),
		ReviewWindowSeconds: profile.GetReviewWindowSeconds(), MaxReviewPerUnit: profile.GetMaxReviewPerUnit(),
		MaxReviewPerRoute: profile.GetMaxReviewPerRoute(), DedupeRule: onboardingv1.ModelDedupeRule(profile.GetDedupeRule()),
		AllowedHeaders: append([]string(nil), profile.GetAllowedHeaders()...), MaxBodyBytes: profile.GetMaxBodyBytes(),
		ReviewNewRoutes: profile.GetReviewNewRoutes(), ReviewInsufficientCoverage: profile.GetReviewInsufficientCoverage(),
	}
}

func clientSourcePolicy(cidrs []string) *artifactv1.ClientSourcePolicy {
	if len(cidrs) == 0 {
		return nil
	}
	return &artifactv1.ClientSourcePolicy{TrustedProxyCidrs: append([]string(nil), cidrs...)}
}

func persistDeploymentSpecification(ctx context.Context, tx dbTX, spec deploymentSpec, listenPlanVersion uint64, generationID string, generationSeq int64) error {
	raw, err := protojson.Marshal(spec.Request)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE deployment_onboarding SET deployment_spec=$1::jsonb,
		deployment_spec_digest=$2,local_unit_id=$3,local_asset_id=$4,expected_listen_plan_version=$5,
		expected_generation_id=$6,expected_generation_seq=$7,last_error='',updated_at=now() WHERE id=1`,
		string(raw), spec.Digest, spec.Request.GetUnitId(), spec.Request.GetAssetId(), listenPlanVersion, generationID, generationSeq)
	return err
}

func signListenPlan(plan *artifactv1.UnitListenPlan, key ed25519.PrivateKey, signer kernel.Signer) error {
	if signer != nil {
		return kernel.SignUnitListenPlanWithSigner(plan, signer)
	}
	return kernel.SignUnitListenPlan(plan, key)
}

// publishDeploymentSpecification 在一个管理员事务内预声明单元、签发监听计划和完整基线世代。
// Edge 之后只能凭人工安装时配置的引导凭据主动注册并拉取这些坐标。
func publishDeploymentSpecification(ctx context.Context, tx pgx.Tx, spec deploymentSpec, key ed25519.PrivateKey, signer kernel.Signer, createdBy string) (uint64, string, int64, error) {
	unitID := spec.Request.GetUnitId()
	assetID := spec.Request.GetAssetId()
	if err := ensureDeploymentAssetAndUnit(ctx, tx, unitID, assetID); err != nil {
		return 0, "", 0, err
	}
	if err := ensureModelSideIdentityDeclaration(ctx, tx, unitID, assetID); err != nil {
		return 0, "", 0, err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "listen-plan:"+unitID); err != nil {
		return 0, "", 0, err
	}
	var previous uint64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(max(version),0) FROM unit_listen_plans WHERE unit_id=$1`, unitID).Scan(&previous); err != nil {
		return 0, "", 0, err
	}
	version := previous + 1
	plan := &artifactv1.UnitListenPlan{
		UnitId: unitID, Posture: spec.Request.GetPosture(), TrafficKey: spec.Request.GetTrafficKey(), Version: version,
		ListenAddress: spec.ListenAddress, UpstreamUrl: spec.UpstreamURL,
		ClientSource:       clientSourcePolicy(spec.Request.GetTrustedProxyCidrs()),
		ModelIngressWindow: spec.Request.GetModelIngressWindow(),
	}
	if err := edgecore.ValidateUnitListenPlan(plan); err != nil {
		return 0, "", 0, err
	}
	if err := signListenPlan(plan, key, signer); err != nil {
		return 0, "", 0, err
	}
	raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(plan)
	if err != nil {
		return 0, "", 0, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO unit_listen_plans(unit_id,version,envelope,signed,created_at)
		VALUES($1,$2,$3::jsonb,true,$4)`, unitID, version, raw, timestamppb.Now().AsTime()); err != nil {
		return 0, "", 0, err
	}
	generationID, generationSeq, err := publishBaselineGenerationTx(ctx, tx, key, signer, assetID, createdBy, spec.ModelProfile)
	if err != nil {
		return 0, "", 0, err
	}
	if err := persistDeploymentSpecification(ctx, tx, spec, version, generationID, generationSeq); err != nil {
		return 0, "", 0, err
	}
	return version, generationID, generationSeq, nil
}

func modelSideIDForUnit(unitID string) string {
	return strings.TrimSpace(unitID) + "-modelside"
}

func ensureModelSideIdentityDeclaration(ctx context.Context, tx dbTX, unitID, assetID string) error {
	modelSideID := modelSideIDForUnit(unitID)
	if _, err := tx.Exec(ctx, `INSERT INTO modelside_identities(modelside_id,unit_id,asset_id)
		VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, modelSideID, unitID, assetID); err != nil {
		return err
	}
	var storedUnitID, storedAssetID string
	if err := tx.QueryRow(ctx, `SELECT unit_id,asset_id FROM modelside_identities WHERE modelside_id=$1`, modelSideID).
		Scan(&storedUnitID, &storedAssetID); err != nil {
		return err
	}
	if storedUnitID != unitID || storedAssetID != assetID {
		return errors.New("modelside identity already binds another deployment")
	}
	return nil
}
