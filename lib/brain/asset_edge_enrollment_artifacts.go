package brain

import (
	"context"
	"crypto/ed25519"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	"yufeng/lib/edgecore"
	"yufeng/lib/kernel"
	artifactv1 "yufeng/proto/gen/artifactv1"
	commonv1 "yufeng/proto/gen/commonv1"
)

func clientSourcePolicy(cidrs []string) *artifactv1.ClientSourcePolicy {
	if len(cidrs) == 0 {
		return nil
	}
	return &artifactv1.ClientSourcePolicy{TrustedProxyCidrs: append([]string(nil), cidrs...)}
}

func signListenPlan(plan *artifactv1.UnitListenPlan, key ed25519.PrivateKey, signer kernel.Signer) error {
	if signer != nil {
		return kernel.SignUnitListenPlanWithSigner(plan, signer)
	}
	return kernel.SignUnitListenPlan(plan, key)
}

// publishEdgeEnrollmentArtifacts 签发单元下一监听计划与保留既有设置的下一资产世代。
// 调用方必须先确认资产存在并固定单元与资产的人工绑定。
func publishEdgeEnrollmentArtifacts(ctx context.Context, tx pgx.Tx, unitID, assetID string,
	posture commonv1.IngressPosture, trafficKey string, trustedProxyCIDRs []string,
	listenAddress, upstreamURL string, window *artifactv1.ModelIngressWindow, profile *artifactv1.ModelProfile,
	key ed25519.PrivateKey, signer kernel.Signer, createdBy string,
) (uint64, string, int64, error) {
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
		UnitId: unitID, Posture: posture, TrafficKey: trafficKey, Version: version,
		ListenAddress: listenAddress, UpstreamUrl: upstreamURL,
		ClientSource:       clientSourcePolicy(trustedProxyCIDRs),
		ModelIngressWindow: window,
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
	generationID, generationSeq, err := publishBaselineGenerationTx(ctx, tx, key, signer, assetID, createdBy, profile)
	if err != nil {
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
		return errors.New("modelside identity already binds another edge enrollment")
	}
	return nil
}
