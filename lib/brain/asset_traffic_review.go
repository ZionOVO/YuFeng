package brain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	"yufeng/lib/edgecore"
	artifactv1 "yufeng/proto/gen/artifactv1"
	assetv1 "yufeng/proto/gen/assetv1"
	unitv1 "yufeng/proto/gen/unitv1"
)

const trafficReviewSettingKind = "traffic_review_policy"

// GetTrafficReviewPolicy 返回资产当前签名世代中的流量审查策略状态。
func (s *AssetServer) GetTrafficReviewPolicy(ctx context.Context, req *connect.Request[assetv1.GetTrafficReviewPolicyRequest]) (*connect.Response[assetv1.GetTrafficReviewPolicyResponse], error) {
	user, err := s.caller(ctx, req)
	if err != nil {
		return nil, err
	}
	assetID := strings.TrimSpace(req.Msg.GetAssetId())
	if assetID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("asset_id is required"))
	}
	if err := requireUserGrant(ctx, s.pool, user.GetUserId(), "console.read", "asset", assetID); err != nil {
		return nil, err
	}
	status, err := loadTrafficReviewPolicyStatus(ctx, s.pool, assetID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&assetv1.GetTrafficReviewPolicyResponse{Status: status}), nil
}

// UpdateTrafficReviewPolicy 把固定上限的策略写入资产设置并发布新的签名世代。
func (s *AssetServer) UpdateTrafficReviewPolicy(ctx context.Context, req *connect.Request[assetv1.UpdateTrafficReviewPolicyRequest]) (*connect.Response[assetv1.UpdateTrafficReviewPolicyResponse], error) {
	user, err := s.caller(ctx, req)
	if err != nil {
		return nil, err
	}
	assetID := strings.TrimSpace(req.Msg.GetAssetId())
	if assetID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("asset_id is required"))
	}
	if err := authorizeWrite(ctx, s.pool, user, "asset.update", "asset", assetID, false); err != nil {
		return nil, err
	}
	target := req.Msg.GetMode()
	if target < artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_OFF || target > artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_SHADOW_CANDIDATES {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("traffic review mode is invalid"))
	}
	resp := &assetv1.UpdateTrafficReviewPolicyResponse{}
	err = idempotentProto(ctx, s.pool, "asset.traffic_review_policy:"+user.GetUserId(), idempotencyKey(req.Header()), req.Msg, resp, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, assetID); err != nil {
			return err
		}
		current, err := loadTrafficReviewPolicyStatus(ctx, tx, assetID)
		if err != nil {
			return err
		}
		if expected := strings.TrimSpace(req.Msg.GetExpectedGenerationId()); expected != "" && expected != current.GetGenerationId() {
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("generation_mismatch"))
		}
		currentMode := current.GetPolicy().GetMode()
		if target > currentMode+1 {
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("traffic review mode must be enabled one level at a time"))
		}
		if target == currentMode {
			resp.Status = current
			return nil
		}
		policy := edgecore.DefaultTrafficReviewPolicy()
		policy.Mode = target
		if err := edgecore.ValidateTrafficReviewPolicy(policy); err != nil {
			return connect.NewError(connect.CodeInvalidArgument, err)
		}
		payload, err := protojson.Marshal(policy)
		if err != nil {
			return err
		}
		artifact := &artifactv1.Artifact{
			Kind: artifactv1.Kind_KIND_TRAFFIC_REVIEW_POLICY, Payload: payload,
			PayloadSchema: edgecore.TrafficReviewPolicySchema, Scope: &artifactv1.Scope{AssetIds: []string{assetID}},
			CreatedAt: timestamppb.Now(), CreatedBy: user.GetUserId(),
		}
		if err := signArtifactEnvelope(artifact, s.signingKey, s.artifactSigner); err != nil {
			return err
		}
		artifactRaw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(artifact)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(payload)
		if _, err := tx.Exec(ctx, `INSERT INTO asset_generation_settings(asset_id,kind,payload,payload_digest,updated_by)
			VALUES($1,$2,$3::jsonb,$4,$5)
			ON CONFLICT(asset_id,kind) DO UPDATE SET payload=EXCLUDED.payload,payload_digest=EXCLUDED.payload_digest,
			updated_by=EXCLUDED.updated_by,updated_at=now()`, assetID, trafficReviewSettingKind, artifactRaw, hex.EncodeToString(digest[:]), user.GetUserId()); err != nil {
			return err
		}
		if err := publishAssetGeneration(ctx, tx, assetID, s.signingKey, s.artifactSigner, false); err != nil {
			return err
		}
		if err := appendAuditTx(ctx, tx, "user", user.GetUserId(), "asset.traffic_review_policy.update", "asset", assetID,
			map[string]any{"from": currentMode.String(), "to": target.String(), "artifact_id": artifact.GetId()}); err != nil {
			return auditFailedError(err)
		}
		status, err := loadTrafficReviewPolicyStatus(ctx, tx, assetID)
		if err != nil {
			return err
		}
		resp.Status = status
		return nil
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func loadTrafficReviewPolicyStatus(ctx context.Context, db dbTX, assetID string) (*assetv1.TrafficReviewPolicyStatus, error) {
	status := &assetv1.TrafficReviewPolicyStatus{Policy: edgecore.DefaultTrafficReviewPolicy()}
	var artifactRaw []byte
	err := db.QueryRow(ctx, `SELECT payload FROM asset_generation_settings WHERE asset_id=$1 AND kind=$2`, assetID, trafficReviewSettingKind).Scan(&artifactRaw)
	if err == nil {
		var artifact artifactv1.Artifact
		if err := protojson.Unmarshal(artifactRaw, &artifact); err != nil {
			return nil, err
		}
		if err := protojson.Unmarshal(artifact.GetPayload(), status.Policy); err != nil {
			return nil, err
		}
		status.PolicyDigest = artifact.GetId()
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	err = db.QueryRow(ctx, `SELECT generation_id,generation_seq FROM asset_generations WHERE asset_id=$1 AND signed ORDER BY generation_seq DESC LIMIT 1`, assetID).
		Scan(&status.GenerationId, &status.GenerationSeq)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	rows, err := db.Query(ctx, `SELECT u.producer_capabilities,u.last_heartbeat_at FROM units u JOIN unit_assets ua ON ua.unit_id=u.unit_id
		WHERE ua.asset_id=$1 AND u.kind='edge'`, assetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	edgeCount := 0
	allEdgesSupported := true
	freshnessCutoff := time.Now().Add(-moduleCapabilityFreshness)
	for rows.Next() {
		var raw []byte
		var lastHeartbeat *time.Time
		if err := rows.Scan(&raw, &lastHeartbeat); err != nil {
			return nil, err
		}
		edgeCount++
		var capabilities unitv1.ProducerCapabilities
		if lastHeartbeat == nil || lastHeartbeat.Before(freshnessCutoff) ||
			protojson.Unmarshal(raw, &capabilities) != nil ||
			!producerCapabilitiesCover(capabilities.GetModuleCapabilities(), []string{"traffic-window/v1", "traffic-review-candidate/v1"}) {
			allEdgesSupported = false
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	status.EdgeSupported = edgeCount > 0 && allEdgesSupported
	return status, nil
}
