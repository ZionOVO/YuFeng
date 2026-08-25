package brain

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"yufeng/lib/edgecore"
	"yufeng/lib/kernel"
	artifactv1 "yufeng/proto/gen/artifactv1"
	commonv1 "yufeng/proto/gen/commonv1"
)

const (
	detectorManifestSchema = "detector-manifest/v1"
	modelProfileSchema     = "model-profile/v1"
)

type baselineSetting struct {
	key     string
	kind    artifactv1.Kind
	schema  string
	payload proto.Message
}

// signArtifactEnvelope 优先走生产签发器；测试与开发模式才使用进程内私钥。
func signArtifactEnvelope(artifact *artifactv1.Artifact, key ed25519.PrivateKey, signer kernel.Signer) error {
	if signer != nil {
		return kernel.SignArtifactWithSigner(artifact, signer)
	}
	return kernel.SignArtifact(artifact, key)
}

func signGenerationEnvelope(generation *artifactv1.AssetGeneration, key ed25519.PrivateKey, signer kernel.Signer) error {
	if signer != nil {
		return kernel.SignGenerationWithSigner(generation, signer)
	}
	return kernel.SignGeneration(generation, key)
}

func firstNonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

func baselineSettings(profile *artifactv1.ModelProfile) []baselineSetting {
	return []baselineSetting{
		{key: "detector_manifest", kind: artifactv1.Kind_KIND_DETECTOR_MANIFEST, schema: detectorManifestSchema, payload: &artifactv1.DetectorManifest{
			DetectorId: "crs", Version: kernel.CRSVersion, TarballSha256: kernel.CRSTarballSHA256,
			GoModule: kernel.CRSGoModule, Paranoia: kernel.CRSParanoia,
		}},
		{key: "taxonomy_mapper", kind: artifactv1.Kind_KIND_TAXONOMY_MAPPER, schema: edgecore.TaxonomyMapperSchema, payload: edgecore.DefaultTaxonomyMapper()},
		{key: "normalizer_profile", kind: artifactv1.Kind_KIND_NORMALIZER_PROFILE, schema: "http-inspection-profile/v1", payload: edgecore.DefaultInspectionProfile()},
		{key: "evidence_digest", kind: artifactv1.Kind_KIND_EVIDENCE_DIGEST, schema: edgecore.EvidenceDigestSchema, payload: edgecore.DefaultEvidenceDigest()},
		{key: "forward_policy", kind: artifactv1.Kind_KIND_FORWARD_POLICY, schema: edgecore.ForwardPolicySchema, payload: &artifactv1.ForwardPolicy{Kind: commonv1.ForwardPolicyKind_FORWARD_POLICY_KIND_NONE}},
		{key: "model_profile", kind: artifactv1.Kind_KIND_MODEL_PROFILE, schema: modelProfileSchema, payload: profile},
	}
}

// publishBaselineGenerationTx 补齐缺失的基线设置、按内容更新模型档案并签发下一个完整资产世代。
// 已有非模型设置保持原签名制品；Edge 与 ModelSide 不能从本地默认值补齐策略。
func publishBaselineGenerationTx(ctx context.Context, tx pgx.Tx, key ed25519.PrivateKey, signer kernel.Signer, assetID, createdBy string, profile *artifactv1.ModelProfile) (string, int64, error) {
	if strings.TrimSpace(assetID) == "" {
		return "", 0, errors.New("asset_id is required")
	}
	normalized, err := kernel.NormalizeModelProfile(profile)
	if err != nil {
		return "", 0, err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, assetID); err != nil {
		return "", 0, err
	}
	actor := firstNonEmpty(createdBy, "system")
	for _, setting := range baselineSettings(normalized) {
		payload, err := protojson.Marshal(setting.payload)
		if err != nil {
			return "", 0, err
		}
		sum := sha256.Sum256(payload)
		payloadDigest := "sha256:" + hex.EncodeToString(sum[:])
		var storedDigest string
		err = tx.QueryRow(ctx, `SELECT payload_digest FROM asset_generation_settings WHERE asset_id=$1 AND kind=$2`, assetID, setting.key).Scan(&storedDigest)
		if err == nil && (setting.key != "model_profile" || storedDigest == payloadDigest) {
			continue
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return "", 0, err
		}
		artifact := &artifactv1.Artifact{
			Kind: setting.kind, Payload: payload, PayloadSchema: setting.schema,
			CreatedAt: timestamppb.Now(), CreatedBy: actor,
			Scope: &artifactv1.Scope{AssetIds: []string{assetID}},
		}
		if err := signArtifactEnvelope(artifact, key, signer); err != nil {
			return "", 0, err
		}
		raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(artifact)
		if err != nil {
			return "", 0, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO asset_generation_settings(asset_id,kind,payload,payload_digest,updated_by)
			VALUES($1,$2,$3::jsonb,$4,$5)
			ON CONFLICT(asset_id,kind) DO UPDATE SET payload=EXCLUDED.payload,payload_digest=EXCLUDED.payload_digest,
			updated_by=EXCLUDED.updated_by,updated_at=now()`, assetID, setting.key, raw, payloadDigest, actor); err != nil {
			return "", 0, err
		}
	}
	if err := publishAssetGeneration(ctx, tx, assetID, key, signer, false); err != nil {
		return "", 0, err
	}
	var generationID string
	var sequence int64
	if err := tx.QueryRow(ctx, `SELECT generation_id,generation_seq FROM asset_generations
		WHERE asset_id=$1 ORDER BY generation_seq DESC LIMIT 1`, assetID).Scan(&generationID, &sequence); err != nil {
		return "", 0, err
	}
	return generationID, sequence, nil
}

// publishBaselineGeneration 保留库内测试装配入口；正式引导在同一管理员事务中调用事务版本。
func publishBaselineGeneration(ctx context.Context, pool *pgxpool.Pool, key ed25519.PrivateKey, signer kernel.Signer, assetID, createdBy string) (string, error) {
	var generationID string
	err := withTx(ctx, pool, func(tx pgx.Tx) error {
		var err error
		generationID, _, err = publishBaselineGenerationTx(ctx, tx, key, signer, assetID, createdBy, kernel.DefaultModelProfile())
		return err
	})
	return generationID, err
}
