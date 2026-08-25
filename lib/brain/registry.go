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
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	assetv1 "yufeng/proto/gen/assetv1"
	commonv1 "yufeng/proto/gen/commonv1"

	"yufeng/lib/edgecore"
	"yufeng/lib/kernel"
	artifactv1 "yufeng/proto/gen/artifactv1"
	registryv1 "yufeng/proto/gen/registryv1"
	"yufeng/proto/gen/registryv1/registryv1connect"
)

// heartbeatIntervalSeconds 是下发给单元的心跳周期（秒）。
const heartbeatIntervalSeconds = 30

// RegistryServer 是单元域注册服务。
type RegistryServer struct {
	pool           *pgxpool.Pool
	pub            ed25519.PublicKey
	bootstrapToken string
	publicAuth     *windowLimiter
	trustedProxies []netip.Prefix
	tapMu          sync.Mutex
	tap            map[string]*tapUnitState
}

// SetTrustedProxies 配置只有直接对端命中时才可信的转发代理网段。
func (s *RegistryServer) SetTrustedProxies(prefixes []netip.Prefix) {
	s.trustedProxies = append([]netip.Prefix(nil), prefixes...)
}

type tapUnitState struct {
	posture    commonv1.IngressPosture
	trafficKey string
	follow     string
	last       uint64
	prev       uint64
	routes     []string
	total      uint64
}

// NewRegistryServer 构造注册服务；bootstrapToken 是单元首次注册的部署级引导令牌。
func NewRegistryServer(pool *pgxpool.Pool, pub ed25519.PublicKey, bootstrapToken string) *RegistryServer {
	return &RegistryServer{pool: pool, pub: pub, bootstrapToken: bootstrapToken, publicAuth: newWindowLimiter(kernel.PublicAuthRatePerMinute, time.Minute), tap: map[string]*tapUnitState{}}
}

// Handler 返回 Connect 服务端处理器。
func (s *RegistryServer) Handler() (string, http.Handler) {
	return registryv1connect.NewRegistryServiceHandler(s, handlerOptions()...)
}

// authorizeRegister 校验注册凭据：已注册单元凭现有会话令牌（凭据轮换），
// 新单元凭部署级引导令牌。两者皆无则拒绝——开放注册等于允许任意方
// 顶替任意 unit_id，接管其发布流与指令。
func (s *RegistryServer) authorizeRegister(ctx context.Context, req *connect.Request[registryv1.RegisterRequest], unitID string) error {
	raw := bearerToken(req.Header().Get("Authorization"))
	if raw == "" {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("register requires bootstrap token or current unit token"))
	}
	var existingTokenHash string
	err := s.pool.QueryRow(ctx, `SELECT token_hash FROM units WHERE unit_id=$1`, unitID).Scan(&existingTokenHash)
	exists := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	isBootstrap := s.bootstrapToken != "" && subtle.ConstantTimeCompare([]byte(raw), []byte(s.bootstrapToken)) == 1
	if exists {
		// 管理员写入人工 Edge 接入配置时只预声明身份与签名计划，不签发进程凭据；
		// 首次人工启动仍须用部署级引导令牌完成唯一一次接管。
		if existingTokenHash == "" {
			if isBootstrap {
				return nil
			}
			return connect.NewError(connect.CodeUnauthenticated, errors.New("predeclared unit requires bootstrap token"))
		}
		if isBootstrap {
			return connect.NewError(connect.CodePermissionDenied, errors.New("bootstrap token cannot replace an existing unit"))
		}
		sum := sha256.Sum256([]byte(raw))
		var one int
		err := s.pool.QueryRow(ctx, `SELECT 1 FROM units WHERE unit_id=$1 AND token_hash=$2 AND (token_expires_at IS NULL OR token_expires_at > now())`, unitID, hex.EncodeToString(sum[:])).Scan(&one)
		if errors.Is(err, pgx.ErrNoRows) {
			return connect.NewError(connect.CodeUnauthenticated, errors.New("register requires current unit access token"))
		}
		return err
	}
	if !isBootstrap {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("new unit requires bootstrap token"))
	}
	return nil
}

// Register 幂等注册/更新单元、资产与绑定，并签发会话令牌。
func (s *RegistryServer) Register(ctx context.Context, req *connect.Request[registryv1.RegisterRequest]) (*connect.Response[registryv1.RegisterResponse], error) {
	if s.publicAuth != nil && !s.publicAuth.Allow(requestSource(req.Peer().Addr, req.Header(), s.trustedProxies), time.Now()) {
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("public register rate exceeded"))
	}
	msg := req.Msg
	if strings.TrimSpace(msg.UnitId) == "" || len(msg.UnitId) > 64 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("unit_id must be 1-64 characters"))
	}
	if msg.ContractVersion != "v1" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("contract version %q unsupported, want v1", msg.ContractVersion))
	}
	if err := s.authorizeRegister(ctx, req, msg.UnitId); err != nil {
		return nil, err
	}
	var previousTokenHash string
	lookupErr := s.pool.QueryRow(ctx, `SELECT token_hash FROM units WHERE unit_id=$1`, msg.UnitId).Scan(&previousTokenHash)
	if lookupErr != nil && !errors.Is(lookupErr, pgx.ErrNoRows) {
		return nil, lookupErr
	}
	credentialed := lookupErr == nil && previousTokenHash != ""

	if len(s.pub) == ed25519.PublicKeySize && msg.PubkeyHint != "" && msg.PubkeyHint != kernel.KeyID(s.pub) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("pubkey_hint does not match brain signing key"))
	}
	capabilities, err := kernel.NormalizeProducerCapabilities(msg.GetCapabilities())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if msg.Kind != registryv1.UnitKind_UNIT_KIND_HOST && capabilities == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("edge producer capabilities are required"))
	}
	asset := msg.Asset
	if asset == nil {
		asset = &assetv1.Asset{}
	}
	assetID := strings.TrimSpace(asset.Id)
	if assetID == "" {
		assetID = msg.UnitId
	}
	var expectedAssetID string
	expectErr := s.pool.QueryRow(ctx, `SELECT asset_id FROM edge_enrollments WHERE unit_id=$1`, msg.UnitId).Scan(&expectedAssetID)
	if expectErr != nil && !errors.Is(expectErr, pgx.ErrNoRows) && !isUndefinedTable(expectErr) {
		return nil, expectErr
	}
	if expectedAssetID != "" && assetID != expectedAssetID {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("registered asset does not match edge enrollment"))
	}
	if expectedAssetID != "" && msg.Kind == registryv1.UnitKind_UNIT_KIND_HOST {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("edge enrollment cannot register as host"))
	}
	raw, tokenHash, err := newSessionToken()
	if err != nil {
		return nil, err
	}
	refreshRaw, refreshHash, err := newSessionToken()
	if err != nil {
		return nil, err
	}
	accessExp := time.Now().Add(kernel.AccessTokenTTL)
	refreshExp := time.Now().Add(kernel.RefreshTokenTTL)
	unitKind := "edge"
	if msg.Kind == registryv1.UnitKind_UNIT_KIND_HOST {
		unitKind = "host"
	}
	transports, err := jsonList(asset.Transports)
	if err != nil {
		return nil, err
	}
	assetCapabilities, err := protoJSON(asset.Capabilities)
	if err != nil {
		return nil, err
	}
	producerCapabilities, err := protoJSON(capabilities)
	if err != nil {
		return nil, err
	}
	// json.Marshal(nil map) 会产出 "null" 文本绕过列默认 '{}'，读写不对称。
	labels := "{}"
	if len(asset.Labels) > 0 {
		raw, err := json.Marshal(asset.Labels)
		if err != nil {
			return nil, err
		}
		labels = string(raw)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `INSERT INTO units(unit_id, kind, version, contract_version, pubkey_hint, token_hash, refresh_token_hash, token_expires_at, refresh_expires_at, producer_capabilities, updated_at)
	VALUES($1,$2,$3,'v1',$4,$5,$6,$7,$8,$9::jsonb,now())
	ON CONFLICT(unit_id) DO UPDATE SET kind=EXCLUDED.kind, version=EXCLUDED.version,
	  contract_version=EXCLUDED.contract_version, pubkey_hint=EXCLUDED.pubkey_hint,
	  token_hash=EXCLUDED.token_hash, refresh_token_hash=EXCLUDED.refresh_token_hash,
	  token_expires_at=EXCLUDED.token_expires_at, refresh_expires_at=EXCLUDED.refresh_expires_at,
	  producer_capabilities=EXCLUDED.producer_capabilities, updated_at=now()`,
		msg.UnitId, unitKind, msg.Version, msg.PubkeyHint, tokenHash, refreshHash, accessExp, refreshExp, producerCapabilities); err != nil {
		return nil, err
	}
	displayName := strings.TrimSpace(asset.DisplayName)
	if displayName == "" {
		displayName = assetID
	}
	if _, err := tx.Exec(ctx, `INSERT INTO assets(asset_id, display_name, access_mode, transports, capabilities, criticality, max_auto_tier, labels, last_probe_at, updated_at)
	VALUES($1,$2,$3,$4::jsonb,$5::jsonb,$6,$7,$8::jsonb,now(),now())
	ON CONFLICT(asset_id) DO UPDATE SET capabilities=EXCLUDED.capabilities, last_probe_at=now()`,
		assetID, displayName, accessModeString(asset.AccessMode), transports, assetCapabilities,
		criticalityString(asset.Criticality), tierString(asset.MaxAutoTier), string(labels)); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO unit_assets(unit_id, asset_id, relation, is_primary)
	VALUES($1,$2,'protects',true)
	ON CONFLICT(unit_id, asset_id) DO NOTHING`, msg.UnitId, assetID); err != nil {
		return nil, err
	}
	if err := backfillUnitLiveFeed(ctx, tx, msg.UnitId, assetID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	resp := &registryv1.RegisterResponse{
		UnitId:            msg.UnitId,
		AssetId:           assetID,
		Token:             raw,
		HeartbeatInterval: heartbeatIntervalSeconds,
		ServerTime:        timestamppb.Now(),
		ContractVersion:   "v1",
		AccessExpiresIn:   int32(kernel.AccessTokenTTL.Seconds()),
	}
	if !credentialed {
		resp.RefreshToken = refreshRaw
	}
	return connect.NewResponse(resp), nil
}

// Refresh 用刷新令牌换取新的访问令牌并轮换刷新令牌。
func (s *RegistryServer) Refresh(ctx context.Context, req *connect.Request[registryv1.RefreshRequest]) (*connect.Response[registryv1.RefreshResponse], error) {
	if s.publicAuth != nil && !s.publicAuth.Allow(requestSource(req.Peer().Addr, req.Header(), s.trustedProxies), time.Now()) {
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("public refresh rate exceeded"))
	}
	unitID := strings.TrimSpace(req.Msg.GetUnitId())
	refresh := strings.TrimSpace(req.Msg.GetRefreshToken())
	if unitID == "" || refresh == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("unit_id and refresh_token are required"))
	}
	sum := sha256.Sum256([]byte(refresh))
	hash := hex.EncodeToString(sum[:])
	accessRaw, accessHash, err := newSessionToken()
	if err != nil {
		return nil, err
	}
	newRefresh, newRefreshHash, err := newSessionToken()
	if err != nil {
		return nil, err
	}
	accessExp := time.Now().Add(kernel.AccessTokenTTL)
	refreshExp := time.Now().Add(kernel.RefreshTokenTTL)
	tag, err := s.pool.Exec(ctx, `UPDATE units SET token_hash=$1, refresh_token_hash=$2, token_expires_at=$3, refresh_expires_at=$4, updated_at=now()
		WHERE unit_id=$5 AND refresh_token_hash=$6 AND refresh_expires_at IS NOT NULL AND refresh_expires_at > now()`,
		accessHash, newRefreshHash, accessExp, refreshExp, unitID, hash)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() != 1 {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid or expired refresh token"))
	}
	return connect.NewResponse(&registryv1.RefreshResponse{
		UnitId:          unitID,
		Token:           accessRaw,
		RefreshToken:    newRefresh,
		AccessExpiresIn: int32(kernel.AccessTokenTTL.Seconds()),
		ServerTime:      timestamppb.Now(),
	}), nil
}

// Heartbeat 更新单元健康并写入按 release 的单调计数器。
func (s *RegistryServer) Heartbeat(ctx context.Context, req *connect.Request[registryv1.HeartbeatRequest]) (*connect.Response[registryv1.HeartbeatResponse], error) {
	unitID, err := requireUnit(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	capabilities, err := kernel.NormalizeProducerCapabilities(req.Msg.GetCapabilities())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	healthProjection, err := kernel.NormalizeProducerHealth(req.Msg.GetProducerHealth())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	currentGenerationID := strings.TrimSpace(req.Msg.GetCurrentGenerationId())
	currentGenerationSeq := req.Msg.GetCurrentGenerationSeq()
	currentListenPlanVersion := req.Msg.GetCurrentListenPlanVersion()
	if (currentGenerationID == "") != (currentGenerationSeq == 0) || currentGenerationSeq < 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("current generation identity is incomplete"))
	}
	if currentGenerationID != "" {
		var valid bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM asset_generations g JOIN unit_assets ua ON ua.asset_id=g.asset_id
			WHERE ua.unit_id=$1 AND g.generation_id=$2 AND g.generation_seq=$3 AND g.signed)`, unitID,
			currentGenerationID, currentGenerationSeq).Scan(&valid); err != nil {
			return nil, err
		}
		if !valid {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("current generation is not a signed generation for this unit"))
		}
	}
	if currentListenPlanVersion > 0 {
		var valid bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM unit_listen_plans
			WHERE unit_id=$1 AND version=$2 AND signed)`, unitID, currentListenPlanVersion).Scan(&valid); err != nil {
			return nil, err
		}
		if !valid {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("current listen plan is not signed for this unit"))
		}
	}
	var prevGen uint64
	_ = tx.QueryRow(ctx, `SELECT generation FROM units WHERE unit_id=$1`, unitID).Scan(&prevGen)
	// 心跳只更新存活时钟。侧载静默/偏斜写在事务提交后的单元健康落盘，
	// 不得在此处一律写成 healthy，否则控制台永远看不到「执行面可能看不见」。
	if _, err := tx.Exec(ctx, `UPDATE units SET generation=GREATEST(generation, $1), version=CASE WHEN $2<>'' THEN $2 ELSE version END,
		posture=CASE WHEN $3<>'' THEN $3 ELSE posture END, traffic_key=CASE WHEN $4<>'' THEN $4 ELSE traffic_key END,
		current_generation_id=$5,current_generation_seq=$6,current_listen_plan_version=$7,last_heartbeat_at=now(), updated_at=now() WHERE unit_id=$8`,
		req.Msg.Generation, strings.TrimSpace(req.Msg.GetVersion()), heartbeatPosture(req.Msg.GetPosture()), strings.TrimSpace(req.Msg.GetTrafficKey()),
		currentGenerationID, currentGenerationSeq, currentListenPlanVersion, unitID); err != nil {
		return nil, err
	}
	if capabilities != nil {
		raw, err := protoJSON(capabilities)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE units SET producer_capabilities=$1::jsonb WHERE unit_id=$2`, raw, unitID); err != nil {
			return nil, err
		}
	}
	if healthProjection != nil {
		raw, err := protoJSON(healthProjection)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE units SET producer_health=$1::jsonb WHERE unit_id=$2`, raw, unitID); err != nil {
			return nil, err
		}
	}
	if req.Msg.Generation > prevGen {
		if _, err := tx.Exec(ctx, `DELETE FROM release_guards WHERE release_id IN (SELECT release_id FROM release_counters WHERE unit_id=$1)`, unitID); err != nil {
			return nil, err
		}
	}
	for _, c := range req.Msg.ReleaseCounters {
		// 计数器按"纪元 + 单调"收账：新纪元整体采纳（单元重启清零是合法的），
		// 同纪元取 GREATEST，旧纪元丢弃——否则回拨计数可以让门禁永远不满足。
		if _, err := tx.Exec(ctx, `INSERT INTO release_counters(unit_id, release_id, generation, mode,
			requests_total, blocks_total, observe_total, canary_selected_total, upstream_5xx_total,
			latency_micros_total, latency_samples, latency_p99_micros, updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,now())
		ON CONFLICT(unit_id, release_id) DO UPDATE SET
		  generation = GREATEST(release_counters.generation, EXCLUDED.generation),
		  mode = CASE WHEN EXCLUDED.generation > release_counters.generation OR (EXCLUDED.generation = release_counters.generation AND EXCLUDED.mode IS DISTINCT FROM release_counters.mode) THEN EXCLUDED.mode WHEN EXCLUDED.generation >= release_counters.generation THEN EXCLUDED.mode ELSE release_counters.mode END,
		  requests_total = CASE WHEN EXCLUDED.generation > release_counters.generation OR (EXCLUDED.generation = release_counters.generation AND EXCLUDED.mode IS DISTINCT FROM release_counters.mode) THEN EXCLUDED.requests_total WHEN EXCLUDED.generation = release_counters.generation THEN GREATEST(release_counters.requests_total, EXCLUDED.requests_total) ELSE release_counters.requests_total END,
		  blocks_total = CASE WHEN EXCLUDED.generation > release_counters.generation OR (EXCLUDED.generation = release_counters.generation AND EXCLUDED.mode IS DISTINCT FROM release_counters.mode) THEN EXCLUDED.blocks_total WHEN EXCLUDED.generation = release_counters.generation THEN GREATEST(release_counters.blocks_total, EXCLUDED.blocks_total) ELSE release_counters.blocks_total END,
		  observe_total = CASE WHEN EXCLUDED.generation > release_counters.generation OR (EXCLUDED.generation = release_counters.generation AND EXCLUDED.mode IS DISTINCT FROM release_counters.mode) THEN EXCLUDED.observe_total WHEN EXCLUDED.generation = release_counters.generation THEN GREATEST(release_counters.observe_total, EXCLUDED.observe_total) ELSE release_counters.observe_total END,
		  canary_selected_total = CASE WHEN EXCLUDED.generation > release_counters.generation OR (EXCLUDED.generation = release_counters.generation AND EXCLUDED.mode IS DISTINCT FROM release_counters.mode) THEN EXCLUDED.canary_selected_total WHEN EXCLUDED.generation = release_counters.generation THEN GREATEST(release_counters.canary_selected_total, EXCLUDED.canary_selected_total) ELSE release_counters.canary_selected_total END,
		  upstream_5xx_total = CASE WHEN EXCLUDED.generation > release_counters.generation OR (EXCLUDED.generation = release_counters.generation AND EXCLUDED.mode IS DISTINCT FROM release_counters.mode) THEN EXCLUDED.upstream_5xx_total WHEN EXCLUDED.generation = release_counters.generation THEN GREATEST(release_counters.upstream_5xx_total, EXCLUDED.upstream_5xx_total) ELSE release_counters.upstream_5xx_total END,
		  latency_micros_total = CASE WHEN EXCLUDED.generation > release_counters.generation OR (EXCLUDED.generation = release_counters.generation AND EXCLUDED.mode IS DISTINCT FROM release_counters.mode) THEN EXCLUDED.latency_micros_total WHEN EXCLUDED.generation = release_counters.generation THEN GREATEST(release_counters.latency_micros_total, EXCLUDED.latency_micros_total) ELSE release_counters.latency_micros_total END,
		  latency_samples = CASE WHEN EXCLUDED.generation > release_counters.generation OR (EXCLUDED.generation = release_counters.generation AND EXCLUDED.mode IS DISTINCT FROM release_counters.mode) THEN EXCLUDED.latency_samples WHEN EXCLUDED.generation = release_counters.generation THEN GREATEST(release_counters.latency_samples, EXCLUDED.latency_samples) ELSE release_counters.latency_samples END,
		  latency_p99_micros = CASE WHEN EXCLUDED.generation > release_counters.generation OR (EXCLUDED.generation = release_counters.generation AND EXCLUDED.mode IS DISTINCT FROM release_counters.mode) THEN EXCLUDED.latency_p99_micros WHEN EXCLUDED.generation = release_counters.generation THEN GREATEST(release_counters.latency_p99_micros, EXCLUDED.latency_p99_micros) ELSE release_counters.latency_p99_micros END,
		  updated_at = now()`,
			unitID, c.ReleaseId, req.Msg.Generation, releaseModeString(c.Mode),
			int64(c.RequestsTotal), int64(c.BlocksTotal), int64(c.ObserveTotal), int64(c.CanarySelectedTotal),
			int64(c.Upstream_5XxTotal), int64(c.LatencyMicrosTotal), int64(c.LatencySamples), int64(c.GetLatencyP99Micros())); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	if req.Msg.Posture != commonv1.IngressPosture_INGRESS_POSTURE_UNSPECIFIED {
		if err := s.advertiseListen(unitID, req.Msg); err != nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, err)
		}
	}
	health := s.unitTapHealth(unitID)
	if _, err := s.pool.Exec(ctx, `UPDATE units SET health=$1, updated_at=now() WHERE unit_id=$2`, unitHealthDB(health), unitID); err != nil {
		return nil, err
	}
	return connect.NewResponse(&registryv1.HeartbeatResponse{
		Health:     health,
		ServerTime: timestamppb.Now(),
	}), nil
}

func heartbeatPosture(posture commonv1.IngressPosture) string {
	if posture == commonv1.IngressPosture_INGRESS_POSTURE_UNSPECIFIED {
		return ""
	}
	return posture.String()
}

// unitHealthDB 把单元健康收成库内短名，与控制台 HealthBadge 两侧都认。
func unitHealthDB(h commonv1.UnitHealth) string {
	switch h {
	case commonv1.UnitHealth_UNIT_HEALTH_TAP_SILENT:
		return "tap_silent"
	case commonv1.UnitHealth_UNIT_HEALTH_TAP_SKEW:
		return "tap_skew"
	case commonv1.UnitHealth_UNIT_HEALTH_DEGRADED:
		return "degraded"
	default:
		return "healthy"
	}
}

func (s *RegistryServer) advertiseListen(unitID string, hb *registryv1.HeartbeatRequest) error {
	s.tapMu.Lock()
	defer s.tapMu.Unlock()
	if s.tap == nil {
		s.tap = map[string]*tapUnitState{}
	}
	st := s.tap[unitID]
	if st == nil {
		st = &tapUnitState{}
		s.tap[unitID] = st
	}
	st.prev = st.last
	st.last = hb.WindowRequests
	st.total += hb.WindowRequests
	st.posture = hb.Posture
	st.trafficKey = hb.TrafficKey
	st.follow = hb.FollowUnitId
	st.routes = append([]string(nil), hb.RouteTemplates...)
	var plans []*artifactv1.UnitListenPlan
	for id, u := range s.tap {
		plans = append(plans, &artifactv1.UnitListenPlan{
			UnitId: id, Posture: u.posture, TrafficKey: u.trafficKey, FollowUnitId: u.follow,
		})
	}
	return edgecore.ValidateListenPlans(plans)
}

func (s *RegistryServer) unitTapHealth(unitID string) commonv1.UnitHealth {
	s.tapMu.Lock()
	defer s.tapMu.Unlock()
	var windows []edgecore.TapWindow
	for id, u := range s.tap {
		windows = append(windows, edgecore.TapWindow{
			UnitID: id, Posture: u.posture, TrafficKey: u.trafficKey, FollowUnitID: u.follow,
			WindowReqs: u.last, PrevWindowReqs: u.prev, Routes: u.routes, TotalRequests: u.total,
		})
	}
	health := edgecore.EvaluateTapHealth(windows)
	if h, ok := health[unitID]; ok {
		return h
	}
	return commonv1.UnitHealth_UNIT_HEALTH_HEALTHY
}

func requireUnitRPC(ctx context.Context, pool *pgxpool.Pool, req interface{ Header() http.Header }) (string, error) {
	unitID, err := requireUnit(ctx, pool, req)
	if err != nil {
		return "", err
	}
	if err := AllowUnitRPC(unitID, time.Now()); err != nil {
		return "", err
	}
	return unitID, nil
}

// InvalidateAccessTokens 在进程启动时作废短期访问令牌；刷新令牌仍有效。
func InvalidateAccessTokens(ctx context.Context, pool *pgxpool.Pool) error {
	return withTx(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE units SET token_hash='', token_expires_at=now()
			WHERE refresh_token_hash IS NOT NULL AND refresh_token_hash <> ''`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM worker_access_tokens`)
		return err
	})
}

func requireUnit(ctx context.Context, pool *pgxpool.Pool, req interface{ Header() http.Header }) (string, error) {
	raw := bearerToken(req.Header().Get("Authorization"))
	if raw == "" {
		return "", connect.NewError(connect.CodeUnauthenticated, errors.New("missing unit token"))
	}
	sum := sha256.Sum256([]byte(raw))
	var unitID string
	err := pool.QueryRow(ctx, `SELECT unit_id FROM units WHERE token_hash=$1 AND (token_expires_at IS NULL OR token_expires_at > now())`, hex.EncodeToString(sum[:])).Scan(&unitID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", connect.NewError(connect.CodeUnauthenticated, errors.New("invalid unit token"))
	}
	if err != nil {
		return "", err
	}
	return unitID, nil
}

func protoJSON(m proto.Message) (string, error) {
	if m == nil {
		return "{}", nil
	}
	b, err := protojson.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func jsonList(msgs []*assetv1.Transport) (string, error) {
	if len(msgs) == 0 {
		return "[]", nil
	}
	items := make([]string, 0, len(msgs))
	for _, m := range msgs {
		s, err := protoJSON(m)
		if err != nil {
			return "", err
		}
		items = append(items, s)
	}
	return "[" + strings.Join(items, ",") + "]", nil
}

func accessModeString(m commonv1.AccessMode) string {
	switch m {
	case commonv1.AccessMode_ACCESS_MODE_EMBEDDED:
		return "embedded"
	case commonv1.AccessMode_ACCESS_MODE_REMOTE:
		return "remote"
	default:
		return "network"
	}
}

func criticalityString(c assetv1.Criticality) string {
	switch c {
	case assetv1.Criticality_CRITICALITY_P0:
		return "p0"
	case assetv1.Criticality_CRITICALITY_P1:
		return "p1"
	default:
		return "p2"
	}
}

func tierString(t commonv1.Tier) string {
	switch t {
	case commonv1.Tier_TIER_L1_TRAFFIC:
		return "L1"
	case commonv1.Tier_TIER_L2_RUNTIME:
		return "L2"
	case commonv1.Tier_TIER_L3_COLD_PATCH:
		return "L3"
	default:
		return "L0"
	}
}

func releaseModeString(m commonv1.ReleaseMode) string {
	switch m {
	case commonv1.ReleaseMode_RELEASE_MODE_SHADOW:
		return "shadow"
	case commonv1.ReleaseMode_RELEASE_MODE_CANARY:
		return "canary"
	case commonv1.ReleaseMode_RELEASE_MODE_ENFORCE:
		return "enforce"
	default:
		return "shadow"
	}
}
