package brain

import (
	"context"
	"errors"
	"strings"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	"yufeng/lib/edgecore"
	"yufeng/lib/kernel"
	artifactv1 "yufeng/proto/gen/artifactv1"
	assetv1 "yufeng/proto/gen/assetv1"
	unitv1 "yufeng/proto/gen/unitv1"
)

// GetModelIngressWindow 返回绑定 Edge 的中央期望、实际应用和降级状态。
func (s *AssetServer) GetModelIngressWindow(ctx context.Context, req *connect.Request[assetv1.GetModelIngressWindowRequest]) (*connect.Response[assetv1.GetModelIngressWindowResponse], error) {
	user, err := s.caller(ctx, req)
	if err != nil {
		return nil, err
	}
	assetID := strings.TrimSpace(req.Msg.GetAssetId())
	unitID := strings.TrimSpace(req.Msg.GetUnitId())
	if assetID == "" || unitID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("asset_id and unit_id are required"))
	}
	if err := requireUserGrant(ctx, s.pool, user.GetUserId(), "console.read", "asset", assetID); err != nil {
		return nil, err
	}
	status, err := loadModelIngressWindowStatus(ctx, s.pool, assetID, unitID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&assetv1.GetModelIngressWindowResponse{Status: status}), nil
}

// UpdateModelIngressWindow 克隆最新监听计划、替换窗口并签发下一单调版本。
func (s *AssetServer) UpdateModelIngressWindow(ctx context.Context, req *connect.Request[assetv1.UpdateModelIngressWindowRequest]) (*connect.Response[assetv1.UpdateModelIngressWindowResponse], error) {
	user, err := s.caller(ctx, req)
	if err != nil {
		return nil, err
	}
	if err := requireAssetAdmin(user); err != nil {
		return nil, err
	}
	assetID := strings.TrimSpace(req.Msg.GetAssetId())
	unitID := strings.TrimSpace(req.Msg.GetUnitId())
	if assetID == "" || unitID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("asset_id and unit_id are required"))
	}
	desired, err := kernel.NormalizeModelIngressWindow(req.Msg.GetDesired())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := authorizeWrite(ctx, s.pool, user, "asset.update", "asset", assetID, false); err != nil {
		return nil, err
	}
	response := &assetv1.UpdateModelIngressWindowResponse{}
	err = idempotentProto(ctx, s.pool, "asset.model_ingress_window:"+user.GetUserId(), idempotencyKey(req.Header()), req.Msg, response, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "listen-plan:"+unitID); err != nil {
			return err
		}
		capabilities, _, _, err := loadBoundModelIngressUnit(ctx, tx, assetID, unitID)
		if err != nil {
			return err
		}
		if !capabilities.GetLocalAsyncBypass() || capabilities.GetModelIngressHardLimit() == nil {
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("edge does not advertise model ingress window capability"))
		}
		current, err := loadLatestUnitListenPlan(ctx, tx, unitID)
		if err != nil {
			return err
		}
		if expected := req.Msg.GetExpectedListenPlanVersion(); expected != 0 && expected != current.GetVersion() {
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("listen_plan_version_mismatch"))
		}
		if kernel.EqualModelIngressWindow(current.GetModelIngressWindow(), desired) {
			if err := syncEdgeEnrollmentListenPlan(ctx, tx, assetID, unitID, current.GetVersion(), desired); err != nil {
				return err
			}
			response.Status, err = loadModelIngressWindowStatus(ctx, tx, assetID, unitID)
			return err
		}
		next := proto.Clone(current).(*artifactv1.UnitListenPlan)
		next.Version++
		next.Signature = nil
		next.ModelIngressWindow = desired
		if err := edgecore.ValidateUnitListenPlan(next); err != nil {
			return connect.NewError(connect.CodeInvalidArgument, err)
		}
		if err := signListenPlan(next, s.signingKey, s.artifactSigner); err != nil {
			return err
		}
		raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(next)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO unit_listen_plans(unit_id,version,envelope,signed) VALUES($1,$2,$3::jsonb,true)`,
			unitID, next.GetVersion(), raw); err != nil {
			return err
		}
		if err := syncEdgeEnrollmentListenPlan(ctx, tx, assetID, unitID, next.GetVersion(), desired); err != nil {
			return err
		}
		if err := appendAuditTx(ctx, tx, "user", user.GetUserId(), "asset.model_ingress_window.update", "asset", assetID,
			map[string]any{"unit_id": unitID, "from_version": current.GetVersion(), "to_version": next.GetVersion(),
				"max_items": desired.GetMaxItems(), "max_retained_bytes": desired.GetMaxRetainedBytes(), "max_queue_age_ms": desired.GetMaxQueueAge().AsDuration().Milliseconds()}); err != nil {
			return auditFailedError(err)
		}
		response.Status, err = loadModelIngressWindowStatus(ctx, tx, assetID, unitID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(response), nil
}

func loadModelIngressWindowStatus(ctx context.Context, db dbTX, assetID, unitID string) (*assetv1.ModelIngressWindowStatus, error) {
	capabilities, health, applied, err := loadBoundModelIngressUnit(ctx, db, assetID, unitID)
	if err != nil {
		return nil, err
	}
	plan, err := loadLatestUnitListenPlan(ctx, db, unitID)
	if err != nil {
		return nil, err
	}
	status := &assetv1.ModelIngressWindowStatus{
		AssetId: assetID, UnitId: unitID, Desired: proto.Clone(plan.GetModelIngressWindow()).(*artifactv1.ModelIngressWindow),
		DesiredListenPlanVersion: plan.GetVersion(), AppliedListenPlanVersion: applied,
	}
	if !capabilities.GetLocalAsyncBypass() {
		status.State = unitv1.ModelIngressWindowState_MODEL_INGRESS_WINDOW_STATE_DISABLED
		return status, nil
	}
	if health.GetEffectiveModelIngressWindow() != nil {
		status.Effective = proto.Clone(health.GetEffectiveModelIngressWindow()).(*artifactv1.ModelIngressWindow)
		status.State = health.GetModelIngressWindowState()
		status.DegradationReasons = append([]unitv1.ModelIngressDegradationReason(nil), health.GetModelIngressDegradationReasons()...)
	} else {
		status.Effective, status.DegradationReasons = clampModelIngressWindow(status.Desired, capabilities.GetModelIngressHardLimit())
		if len(status.DegradationReasons) > 0 {
			status.State = unitv1.ModelIngressWindowState_MODEL_INGRESS_WINDOW_STATE_DEGRADED
		} else {
			status.State = unitv1.ModelIngressWindowState_MODEL_INGRESS_WINDOW_STATE_APPLIED
		}
	}
	if status.GetAppliedListenPlanVersion() < status.GetDesiredListenPlanVersion() {
		status.State = unitv1.ModelIngressWindowState_MODEL_INGRESS_WINDOW_STATE_CONVERGING
	}
	return status, nil
}

func loadBoundModelIngressUnit(ctx context.Context, db dbTX, assetID, unitID string) (*unitv1.ProducerCapabilities, *unitv1.ProducerHealth, uint64, error) {
	var capabilitiesRaw, healthRaw []byte
	var applied int64
	err := db.QueryRow(ctx, `SELECT u.producer_capabilities,u.producer_health,u.current_listen_plan_version
		FROM units u JOIN unit_assets ua ON ua.unit_id=u.unit_id
		WHERE ua.asset_id=$1 AND u.unit_id=$2 AND u.kind='edge'`, assetID, unitID).Scan(&capabilitiesRaw, &healthRaw, &applied)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, 0, connect.NewError(connect.CodeNotFound, errors.New("bound edge unit not found"))
	}
	if err != nil {
		return nil, nil, 0, err
	}
	capabilities := &unitv1.ProducerCapabilities{}
	if err := protojson.Unmarshal(capabilitiesRaw, capabilities); err != nil {
		return nil, nil, 0, err
	}
	health := &unitv1.ProducerHealth{}
	if err := protojson.Unmarshal(healthRaw, health); err != nil {
		return nil, nil, 0, err
	}
	if applied < 0 {
		return nil, nil, 0, errors.New("stored listen plan version is invalid")
	}
	return capabilities, health, uint64(applied), nil
}

func loadLatestUnitListenPlan(ctx context.Context, db dbTX, unitID string) (*artifactv1.UnitListenPlan, error) {
	var raw []byte
	err := db.QueryRow(ctx, `SELECT envelope FROM unit_listen_plans WHERE unit_id=$1 AND signed ORDER BY version DESC LIMIT 1`, unitID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("signed unit listen plan not found"))
	}
	if err != nil {
		return nil, err
	}
	plan := &artifactv1.UnitListenPlan{}
	if err := protojson.Unmarshal(raw, plan); err != nil {
		return nil, err
	}
	return plan, nil
}

func clampModelIngressWindow(desired, hard *artifactv1.ModelIngressWindow) (*artifactv1.ModelIngressWindow, []unitv1.ModelIngressDegradationReason) {
	if desired == nil || hard == nil {
		return nil, nil
	}
	effective := &artifactv1.ModelIngressWindow{
		MaxItems:         min(desired.GetMaxItems(), hard.GetMaxItems()),
		MaxRetainedBytes: min(desired.GetMaxRetainedBytes(), hard.GetMaxRetainedBytes()),
		MaxQueueAge:      durationpb.New(min(desired.GetMaxQueueAge().AsDuration(), hard.GetMaxQueueAge().AsDuration())),
	}
	var reasons []unitv1.ModelIngressDegradationReason
	if desired.GetMaxItems() > hard.GetMaxItems() {
		reasons = append(reasons, unitv1.ModelIngressDegradationReason_MODEL_INGRESS_DEGRADATION_REASON_MAX_ITEMS)
	}
	if desired.GetMaxRetainedBytes() > hard.GetMaxRetainedBytes() {
		reasons = append(reasons, unitv1.ModelIngressDegradationReason_MODEL_INGRESS_DEGRADATION_REASON_MAX_RETAINED_BYTES)
	}
	if desired.GetMaxQueueAge().AsDuration() > hard.GetMaxQueueAge().AsDuration() {
		reasons = append(reasons, unitv1.ModelIngressDegradationReason_MODEL_INGRESS_DEGRADATION_REASON_MAX_QUEUE_AGE)
	}
	return effective, reasons
}
