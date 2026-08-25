package brain

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	assetv1 "yufeng/proto/gen/assetv1"
	"yufeng/proto/gen/assetv1/assetv1connect"
	authv1 "yufeng/proto/gen/authv1"
	commonv1 "yufeng/proto/gen/commonv1"
	grantv1 "yufeng/proto/gen/grantv1"
	unitv1 "yufeng/proto/gen/unitv1"

	"yufeng/lib/kernel"
)

const assetSelectCols = `asset_id, display_name, access_mode, transports, capabilities, criticality, max_auto_tier, labels, version, updated_at`

// AssetServer 是资产面服务。
type AssetServer struct {
	pool            *pgxpool.Pool
	centralWorkerID string
	signingKey      ed25519.PrivateKey
	artifactSigner  kernel.Signer
}

// NewAssetServer 构造资产服务。
func NewAssetServer(pool *pgxpool.Pool, centralWorkerIDs ...string) *AssetServer {
	server := &AssetServer{pool: pool}
	if len(centralWorkerIDs) > 0 {
		server.centralWorkerID = centralWorkerIDs[0]
	}
	return server
}

// Handler 返回 Connect 服务端处理器。
func (s *AssetServer) Handler() (string, http.Handler) {
	return assetv1connect.NewAssetServiceHandler(s, handlerOptions()...)
}

func (s *AssetServer) caller(ctx context.Context, req interface{ Header() http.Header }) (*authv1.User, error) {
	user, err := requireUser(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	if err := requireCompletedOnboarding(ctx, s.pool); err != nil {
		return nil, err
	}
	return user, nil
}

// CreateAsset 校验管理权限与资产字段后登记一个新的防御资产。
func (s *AssetServer) CreateAsset(ctx context.Context, req *connect.Request[assetv1.CreateAssetRequest]) (*connect.Response[assetv1.CreateAssetResponse], error) {
	user, err := s.caller(ctx, req)
	if err != nil {
		return nil, err
	}
	if err := requireAssetAdmin(user); err != nil {
		return nil, err
	}
	if req.Msg.Asset == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("asset is required"))
	}
	a := proto.Clone(req.Msg.Asset).(*assetv1.Asset)
	if strings.TrimSpace(a.DisplayName) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("display_name is required"))
	}
	if err := authorizeWrite(ctx, s.pool, user, "asset.create", "asset", a.Id, false); err != nil {
		return nil, err
	}
	if a.AccessMode == commonv1.AccessMode_ACCESS_MODE_UNSPECIFIED {
		a.AccessMode = commonv1.AccessMode_ACCESS_MODE_NETWORK
	}
	if a.Criticality == assetv1.Criticality_CRITICALITY_UNSPECIFIED {
		a.Criticality = assetv1.Criticality_CRITICALITY_P2
	}
	if a.MaxAutoTier == commonv1.Tier_TIER_UNSPECIFIED {
		a.MaxAutoTier = commonv1.Tier_TIER_L0_REPORT
	}
	if a.Labels == nil {
		a.Labels = map[string]string{}
	}
	resp := &assetv1.CreateAssetResponse{}
	err = idempotentProto(ctx, s.pool, "asset.create:"+user.UserId, idempotencyKey(req.Header()), req.Msg, resp, func(tx pgx.Tx) error {
		if a.Id == "" {
			id, err := newID("asset")
			if err != nil {
				return err
			}
			a.Id = id
		}
		transports, capabilities, labels, err := assetColumns(a)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO assets(asset_id, display_name, access_mode, transports, capabilities, criticality, max_auto_tier, labels)
	VALUES($1,$2,$3,$4::jsonb,$5::jsonb,$6,$7,$8::jsonb)`,
			a.Id, a.DisplayName, accessModeString(a.AccessMode), transports, capabilities, criticalityString(a.Criticality), tierString(a.MaxAutoTier), labels); err != nil {
			return connect.NewError(connect.CodeAlreadyExists, err)
		}
		if err := appendAssetBinding(ctx, tx, user.UserId, a.Id); err != nil {
			return err
		}
		if err := appendSubjectAssetBinding(ctx, tx, "worker", s.centralWorkerID, a.Id); err != nil {
			return err
		}
		resp.Asset = a
		return nil
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

// UpdateAsset 使用更新时间作并发保护并修改指定防御资产。
func (s *AssetServer) UpdateAsset(ctx context.Context, req *connect.Request[assetv1.UpdateAssetRequest]) (*connect.Response[assetv1.UpdateAssetResponse], error) {
	user, err := s.caller(ctx, req)
	if err != nil {
		return nil, err
	}
	if err := requireAssetAdmin(user); err != nil {
		return nil, err
	}
	if err := authorizeWrite(ctx, s.pool, user, "asset.update", "asset", req.Msg.AssetId, false); err != nil {
		return nil, err
	}
	if req.Msg.Asset == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("asset is required"))
	}
	sets := []string{"updated_at=now()", "version=version+1"}
	args := []any{req.Msg.AssetId}
	argn := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }
	paths := []string{}
	if req.Msg.UpdateMask != nil {
		paths = req.Msg.UpdateMask.Paths
	}
	for _, path := range paths {
		switch path {
		case "display_name", "displayName":
			sets = append(sets, "display_name="+argn(req.Msg.Asset.DisplayName))
		case "access_mode", "accessMode":
			sets = append(sets, "access_mode="+argn(accessModeString(req.Msg.Asset.AccessMode)))
		case "criticality":
			sets = append(sets, "criticality="+argn(criticalityString(req.Msg.Asset.Criticality)))
		case "max_auto_tier", "maxAutoTier":
			sets = append(sets, "max_auto_tier="+argn(tierString(req.Msg.Asset.MaxAutoTier)))
		case "labels":
			raw, err := json.Marshal(req.Msg.Asset.Labels)
			if err != nil {
				return nil, err
			}
			sets = append(sets, "labels="+argn(string(raw))+"::jsonb")
		default:
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unsupported update path %s", path))
		}
	}
	if len(sets) == 2 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("update mask is empty"))
	}
	where := `WHERE asset_id=$1`
	if ts := req.Msg.GetExpectedUpdatedAt(); ts != nil && ts.IsValid() {
		where += ` AND updated_at=` + argn(ts.AsTime())
	}
	query := `UPDATE assets SET ` + strings.Join(sets, ", ") + ` ` + where + ` RETURNING ` + assetSelectCols
	a, err := scanAsset(s.pool.QueryRow(ctx, query, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		if req.Msg.GetExpectedUpdatedAt() != nil && req.Msg.GetExpectedUpdatedAt().IsValid() {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("version_mismatch"))
		}
		return nil, objectDenied()
	}
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&assetv1.UpdateAssetResponse{Asset: a}), nil
}

// DeleteAsset 删除没有活动单元绑定的防御资产。
func (s *AssetServer) DeleteAsset(ctx context.Context, req *connect.Request[assetv1.DeleteAssetRequest]) (*connect.Response[assetv1.DeleteAssetResponse], error) {
	user, err := s.caller(ctx, req)
	if err != nil {
		return nil, err
	}
	if err := requireAssetAdmin(user); err != nil {
		return nil, err
	}
	id := strings.TrimSpace(req.Msg.AssetId)
	if id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("asset_id is required"))
	}
	if err := authorizeWrite(ctx, s.pool, user, "asset.delete", "asset", id, false); err != nil {
		return nil, err
	}
	var enrolled bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM edge_enrollments WHERE asset_id=$1)`, id).Scan(&enrolled); err != nil {
		return nil, err
	}
	if enrolled {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("asset has an edge enrollment"))
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM assets WHERE asset_id=$1`, id)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, objectDenied()
	}
	if err := pruneAssetBinding(ctx, s.pool, id); err != nil {
		return nil, err
	}
	return connect.NewResponse(&assetv1.DeleteAssetResponse{}), nil
}

// ListAssets 按调用者授权范围过滤并分页列出防御资产。
func (s *AssetServer) ListAssets(ctx context.Context, req *connect.Request[assetv1.ListAssetsRequest]) (*connect.Response[assetv1.ListAssetsResponse], error) {
	user, err := s.caller(ctx, req)
	if err != nil {
		return nil, err
	}
	access, err := loadEffectiveAccess(ctx, s.pool, user)
	if err != nil {
		return nil, err
	}
	scope := scopeFromAccess(access)
	ids := scope.assetIDs()
	if !scope.hasTool("console.read") || len(ids) == 0 {
		return connect.NewResponse(&assetv1.ListAssetsResponse{}), nil
	}
	limit := ClampPageSize(req.Msg.GetPageSize())
	offset, err := decodePageOffset(req.Msg.GetPageToken())
	if err != nil {
		return nil, err
	}
	crit := criticalityFilter(req.Msg.GetCriticality())
	rows, err := s.pool.Query(ctx, `SELECT `+assetSelectCols+`
	FROM assets a WHERE a.asset_id = ANY($1) AND ($2='' OR a.display_name ILIKE '%'||$2||'%')
	  AND ($3='' OR a.criticality=$3)
	ORDER BY a.created_at DESC LIMIT $4 OFFSET $5`, ids, req.Msg.Query, crit, limit+1, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	resp := &assetv1.ListAssetsResponse{}
	for rows.Next() {
		a, err := scanAsset(rows)
		if err != nil {
			return nil, err
		}
		detail, err := s.assetDetail(ctx, a)
		if err != nil {
			return nil, err
		}
		resp.Assets = append(resp.Assets, detail)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(resp.Assets) > limit {
		resp.Assets = resp.Assets[:limit]
		resp.NextPageToken = encodePageOffset(offset + limit)
	}
	return connect.NewResponse(resp), nil
}

// GetAsset 返回调用者有权查看的单个防御资产。
func (s *AssetServer) GetAsset(ctx context.Context, req *connect.Request[assetv1.GetAssetRequest]) (*connect.Response[assetv1.GetAssetResponse], error) {
	user, err := s.caller(ctx, req)
	if err != nil {
		return nil, err
	}
	access, err := loadEffectiveAccess(ctx, s.pool, user)
	if err != nil {
		return nil, err
	}
	if !scopeFromAccess(access).coversAsset(req.Msg.AssetId) {
		return nil, objectDenied()
	}
	row := s.pool.QueryRow(ctx, `SELECT `+assetSelectCols+` FROM assets WHERE asset_id=$1`, req.Msg.AssetId)
	a, err := scanAsset(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, objectDenied()
	}
	if err != nil {
		return nil, err
	}
	detail, err := s.assetDetail(ctx, a)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&assetv1.GetAssetResponse{Asset: detail}), nil
}

// AttachUnit 把已登记数据面单元绑定到指定防御资产。
func (s *AssetServer) AttachUnit(ctx context.Context, req *connect.Request[assetv1.AttachUnitRequest]) (*connect.Response[assetv1.AttachUnitResponse], error) {
	user, err := s.caller(ctx, req)
	if err != nil {
		return nil, err
	}
	if err := requireAssetAdmin(user); err != nil {
		return nil, err
	}
	if err := authorizeWrite(ctx, s.pool, user, "asset.attach", "asset", req.Msg.AssetId, false); err != nil {
		return nil, err
	}
	var enrolledAssetID string
	err = s.pool.QueryRow(ctx, `SELECT asset_id FROM edge_enrollments WHERE unit_id=$1`, req.Msg.UnitId).Scan(&enrolledAssetID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if enrolledAssetID != "" && enrolledAssetID != req.Msg.AssetId {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("edge enrollment binds another asset"))
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO unit_assets(unit_id, asset_id, relation, is_primary) VALUES($1,$2,'protects',false)
	ON CONFLICT(unit_id, asset_id) DO NOTHING`, req.Msg.UnitId, req.Msg.AssetId); err != nil {
		return nil, err
	}
	if err := backfillUnitLiveFeed(ctx, s.pool, req.Msg.UnitId, req.Msg.AssetId); err != nil {
		return nil, err
	}
	a, err := scanAsset(s.pool.QueryRow(ctx, `SELECT `+assetSelectCols+` FROM assets WHERE asset_id=$1`, req.Msg.AssetId))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("asset not found"))
	}
	if err != nil {
		return nil, err
	}
	detail, err := s.assetDetail(ctx, a)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&assetv1.AttachUnitResponse{Asset: detail}), nil
}

// DetachUnit 解除数据面单元与指定防御资产的绑定。
func (s *AssetServer) DetachUnit(ctx context.Context, req *connect.Request[assetv1.DetachUnitRequest]) (*connect.Response[assetv1.DetachUnitResponse], error) {
	user, err := s.caller(ctx, req)
	if err != nil {
		return nil, err
	}
	if err := requireAssetAdmin(user); err != nil {
		return nil, err
	}
	if err := authorizeWrite(ctx, s.pool, user, "asset.detach", "asset", req.Msg.AssetId, false); err != nil {
		return nil, err
	}
	var enrolled bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM edge_enrollments WHERE unit_id=$1 AND asset_id=$2)`, req.Msg.UnitId, req.Msg.AssetId).Scan(&enrolled); err != nil {
		return nil, err
	}
	if enrolled {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("edge enrollment must be retired before detach"))
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM unit_assets WHERE unit_id=$1 AND asset_id=$2`, req.Msg.UnitId, req.Msg.AssetId); err != nil {
		return nil, err
	}
	a, err := scanAsset(s.pool.QueryRow(ctx, `SELECT `+assetSelectCols+` FROM assets WHERE asset_id=$1`, req.Msg.AssetId))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("asset not found"))
	}
	if err != nil {
		return nil, err
	}
	detail, err := s.assetDetail(ctx, a)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&assetv1.DetachUnitResponse{Asset: detail}), nil
}

func (s *AssetServer) assetDetail(ctx context.Context, a *assetv1.Asset) (*assetv1.AssetDetail, error) {
	h, err := assetHealthFromUnits(ctx, s.pool, a.Id)
	if err != nil {
		return nil, err
	}
	detail := &assetv1.AssetDetail{Asset: a, Health: h}
	rows, err := s.pool.Query(ctx, `SELECT u.unit_id, u.kind, u.version, u.health, u.producer_capabilities,
		u.producer_health, u.posture, u.traffic_key, u.last_heartbeat_at,u.current_generation_id,u.current_generation_seq,
		u.current_listen_plan_version
		FROM units u JOIN unit_assets ua ON ua.unit_id=u.unit_id WHERE ua.asset_id=$1 ORDER BY u.unit_id`, a.Id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, kind, version, health, capabilitiesRaw, producerHealthRaw, posture, trafficKey, currentGenerationID string
		var currentGenerationSeq, currentListenPlanVersion int64
		var lastHeartbeat *time.Time
		if err := rows.Scan(&id, &kind, &version, &health, &capabilitiesRaw, &producerHealthRaw, &posture, &trafficKey,
			&lastHeartbeat, &currentGenerationID, &currentGenerationSeq, &currentListenPlanVersion); err != nil {
			return nil, err
		}
		detail.UnitIds = append(detail.UnitIds, id)
		unit := &unitv1.UnitProjection{
			UnitId: id, Kind: kind, Version: version, Health: unitHealthEnum(health),
			Posture: ingressPostureEnum(posture), TrafficKey: trafficKey,
			CurrentGenerationId: currentGenerationID, CurrentGenerationSeq: currentGenerationSeq,
		}
		if currentListenPlanVersion > 0 {
			unit.CurrentListenPlanVersion = uint64(currentListenPlanVersion)
		}
		var capabilities unitv1.ProducerCapabilities
		if err := protojson.Unmarshal([]byte(capabilitiesRaw), &capabilities); err != nil {
			return nil, err
		}
		unit.Capabilities = &capabilities
		var producerHealth unitv1.ProducerHealth
		if err := protojson.Unmarshal([]byte(producerHealthRaw), &producerHealth); err != nil {
			return nil, err
		}
		unit.ProducerHealth = &producerHealth
		if lastHeartbeat != nil {
			unit.LastHeartbeatAt = timestamppb.New(*lastHeartbeat)
		}
		detail.Units = append(detail.Units, unit)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	detail.EdgeEnrollments, err = listEdgeEnrollments(ctx, s.pool, a.Id)
	if err != nil {
		return nil, err
	}
	err = s.pool.QueryRow(ctx, `SELECT count(*) FROM releases r JOIN release_assets ra ON ra.release_id=r.release_id
WHERE ra.asset_id=$1 AND r.state IN ('shadow','canary','enforce')`, a.Id).Scan(&detail.ActiveReleaseCount)
	return detail, err
}

func unitHealthEnum(value string) commonv1.UnitHealth {
	switch strings.TrimSpace(value) {
	case "degraded", commonv1.UnitHealth_UNIT_HEALTH_DEGRADED.String():
		return commonv1.UnitHealth_UNIT_HEALTH_DEGRADED
	case "tap_silent", commonv1.UnitHealth_UNIT_HEALTH_TAP_SILENT.String():
		return commonv1.UnitHealth_UNIT_HEALTH_TAP_SILENT
	case "tap_skew", commonv1.UnitHealth_UNIT_HEALTH_TAP_SKEW.String():
		return commonv1.UnitHealth_UNIT_HEALTH_TAP_SKEW
	default:
		return commonv1.UnitHealth_UNIT_HEALTH_HEALTHY
	}
}

func ingressPostureEnum(value string) commonv1.IngressPosture {
	if number, ok := commonv1.IngressPosture_value[strings.TrimSpace(value)]; ok {
		return commonv1.IngressPosture(number)
	}
	return commonv1.IngressPosture_INGRESS_POSTURE_UNSPECIFIED
}

// assetHealthFromUnits 取绑定单元里最差的健康短名，供列表与详情徽章。
func assetHealthFromUnits(ctx context.Context, pool *pgxpool.Pool, assetID string) (string, error) {
	rows, err := pool.Query(ctx, `SELECT u.health FROM units u JOIN unit_assets ua ON ua.unit_id=u.unit_id WHERE ua.asset_id=$1`, assetID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	worst := "healthy"
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return "", err
		}
		if healthRank(h) > healthRank(worst) {
			worst = h
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return worst, nil
}

func healthRank(h string) int {
	switch h {
	case "tap_silent", "tap_skew", "UNIT_HEALTH_TAP_SILENT", "UNIT_HEALTH_TAP_SKEW":
		return 2
	case "degraded", "UNIT_HEALTH_DEGRADED":
		return 1
	default:
		return 0
	}
}

// scanAsset 把资产行还原为 Protocol Buffers 消息；transports 等列是 JavaScript 对象表示法文本，此处完成反序列化。
func scanAsset(row pgx.Row) (*assetv1.Asset, error) {
	var id, display, access, transports, capabilities, criticality, tier, labels string
	var version int64
	var updatedAt time.Time
	if err := row.Scan(&id, &display, &access, &transports, &capabilities, &criticality, &tier, &labels, &version, &updatedAt); err != nil {
		return nil, err
	}
	a := &assetv1.Asset{Id: id, DisplayName: display, Labels: map[string]string{}}
	if !updatedAt.IsZero() {
		a.UpdatedAt = timestamppb.New(updatedAt)
	}
	// transports 列是 protojson 数组文本：先解到局部结构再挂回消息。
	var ts []struct {
		Kind     string `json:"kind"`
		Endpoint string `json:"endpoint"`
	}
	if err := json.Unmarshal([]byte(transports), &ts); err != nil {
		return nil, fmt.Errorf("asset %s transports: %w", id, err)
	}
	for _, t := range ts {
		a.Transports = append(a.Transports, &assetv1.Transport{Kind: transportKindEnum(t.Kind), Endpoint: t.Endpoint})
	}
	if err := json.Unmarshal([]byte(labels), &a.Labels); err != nil {
		return nil, fmt.Errorf("asset %s labels: %w", id, err)
	}
	if a.Labels == nil {
		a.Labels = map[string]string{}
	}
	a.AccessMode = accessModeEnum(access)
	a.Criticality = criticalityEnum(criticality)
	a.MaxAutoTier = tierEnum(tier)
	var capMsg assetv1.CapabilityMatrix
	if err := protojson.Unmarshal([]byte(capabilities), &capMsg); err != nil {
		return nil, fmt.Errorf("asset %s capabilities: %w", id, err)
	}
	a.Capabilities = &capMsg
	return a, nil
}

func assetColumns(a *assetv1.Asset) (string, string, string, error) {
	transports, err := jsonList(a.Transports)
	if err != nil {
		return "", "", "", err
	}
	capabilities, err := protoJSON(a.Capabilities)
	if err != nil {
		return "", "", "", err
	}
	labels, err := json.Marshal(a.Labels)
	if err != nil {
		return "", "", "", err
	}
	return transports, capabilities, string(labels), nil
}

// transportKindEnum 把 transports 列里 protojson 的枚举名还原为枚举值。
func transportKindEnum(s string) assetv1.Transport_Kind {
	switch s {
	case "KIND_LOCAL":
		return assetv1.Transport_KIND_LOCAL
	case "KIND_SSH":
		return assetv1.Transport_KIND_SSH
	case "KIND_VENDOR_API":
		return assetv1.Transport_KIND_VENDOR_API
	default:
		return assetv1.Transport_KIND_UNSPECIFIED
	}
}

func criticalityFilter(s string) string {
	switch strings.TrimSpace(s) {
	case "", "CRITICALITY_UNSPECIFIED":
		return ""
	case "CRITICALITY_P0", "p0":
		return "p0"
	case "CRITICALITY_P1", "p1":
		return "p1"
	case "CRITICALITY_P2", "p2":
		return "p2"
	default:
		return strings.TrimSpace(s)
	}
}

func accessModeEnum(s string) commonv1.AccessMode {
	switch s {
	case "embedded":
		return commonv1.AccessMode_ACCESS_MODE_EMBEDDED
	case "remote":
		return commonv1.AccessMode_ACCESS_MODE_REMOTE
	default:
		return commonv1.AccessMode_ACCESS_MODE_NETWORK
	}
}

func criticalityEnum(s string) assetv1.Criticality {
	switch s {
	case "p0":
		return assetv1.Criticality_CRITICALITY_P0
	case "p1":
		return assetv1.Criticality_CRITICALITY_P1
	default:
		return assetv1.Criticality_CRITICALITY_P2
	}
}

func tierEnum(s string) commonv1.Tier {
	switch s {
	case "L1":
		return commonv1.Tier_TIER_L1_TRAFFIC
	case "L2":
		return commonv1.Tier_TIER_L2_RUNTIME
	case "L3":
		return commonv1.Tier_TIER_L3_COLD_PATCH
	default:
		return commonv1.Tier_TIER_L0_REPORT
	}
}

func listAssetIDs(ctx context.Context, db dbTX) ([]string, error) {
	rows, err := db.Query(ctx, `SELECT asset_id FROM assets ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if id != "" && id != "bootstrap" {
			ids = append(ids, id)
		}
	}
	return ids, rows.Err()
}

func appendAssetBinding(ctx context.Context, db dbTX, userID, assetID string) error {
	return appendSubjectAssetBinding(ctx, db, "user", userID, assetID)
}

func appendSubjectAssetBinding(ctx context.Context, db dbTX, subjectKind, subjectID, assetID string) error {
	if subjectID == "" || assetID == "" || assetID == "bootstrap" {
		return nil
	}
	rows, err := db.Query(ctx, `SELECT grant_id, bindings FROM grants WHERE subject_kind=$1 AND subject_id=$2`, subjectKind, subjectID)
	if err != nil {
		return err
	}
	defer rows.Close()
	type row struct {
		id    string
		binds []*grantv1.BindingRef
	}
	var found []row
	for rows.Next() {
		var id string
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return err
		}
		var binds []*grantv1.BindingRef
		if err := json.Unmarshal(raw, &binds); err != nil {
			return err
		}
		found = append(found, row{id: id, binds: binds})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range found {
		has := false
		for _, b := range r.binds {
			if b != nil && b.Kind == "asset" && b.Id == assetID {
				has = true
				break
			}
		}
		if has {
			continue
		}
		r.binds = append(r.binds, &grantv1.BindingRef{Kind: "asset", Id: assetID})
		raw, err := json.Marshal(r.binds)
		if err != nil {
			return err
		}
		if _, err := db.Exec(ctx, `UPDATE grants SET bindings=$2::jsonb WHERE grant_id=$1`, r.id, raw); err != nil {
			return err
		}
	}
	return nil
}

func pruneAssetBinding(ctx context.Context, pool *pgxpool.Pool, assetID string) error {
	if assetID == "" {
		return nil
	}
	rows, err := pool.Query(ctx, `SELECT grant_id, bindings FROM grants`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type row struct {
		id    string
		binds []*grantv1.BindingRef
	}
	var found []row
	for rows.Next() {
		var id string
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return err
		}
		var binds []*grantv1.BindingRef
		if err := json.Unmarshal(raw, &binds); err != nil {
			return err
		}
		found = append(found, row{id: id, binds: binds})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range found {
		next := r.binds[:0]
		changed := false
		for _, b := range r.binds {
			if b != nil && b.Kind == "asset" && b.Id == assetID {
				changed = true
				continue
			}
			next = append(next, b)
		}
		if !changed {
			continue
		}
		raw, err := json.Marshal(next)
		if err != nil {
			return err
		}
		if _, err := pool.Exec(ctx, `UPDATE grants SET bindings=$2::jsonb WHERE grant_id=$1`, r.id, raw); err != nil {
			return err
		}
	}
	return nil
}
