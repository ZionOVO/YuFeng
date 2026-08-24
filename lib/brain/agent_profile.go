package brain

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "yufeng/proto/gen/agentv1"
	"yufeng/proto/gen/agentv1/agentv1connect"
	authv1 "yufeng/proto/gen/authv1"
	grantv1 "yufeng/proto/gen/grantv1"
)

const (
	agentProfileKindTrafficReview = "traffic_review"
	agentProfileStateEnabled      = "enabled"
	agentProfileStateDisabled     = "disabled"
	agentProfileStateTombstoned   = "tombstoned"
	agentProfilePageMax           = 200
)

const agentProfileSelectColumns = `agent_id, display_name, kind, state, tools, bindings, created_by, created_at, updated_at,
	execution_mode, config_digest,
	(SELECT count(*)::integer FROM runs WHERE runs.agent_profile_id=managed_agent_profiles.agent_id
		AND runs.state IN ('pending','running')),
	(SELECT max(runs.created_at) FROM runs WHERE runs.agent_profile_id=managed_agent_profiles.agent_id), tombstoned_at,
	COALESCE((SELECT work_items.worker_id FROM work_items JOIN runs ON runs.run_id=work_items.run_id
		WHERE runs.agent_profile_id=managed_agent_profiles.agent_id AND work_items.worker_id<>''
		ORDER BY work_items.updated_at DESC LIMIT 1), ''),
	COALESCE((SELECT workers.operating_system || '/' || workers.architecture
		FROM work_items JOIN runs ON runs.run_id=work_items.run_id JOIN workers ON workers.worker_id=work_items.worker_id
		WHERE runs.agent_profile_id=managed_agent_profiles.agent_id
		ORDER BY work_items.updated_at DESC LIMIT 1), '')`

var trafficReviewProfileTools = map[string]struct{}{
	"case.get":              {},
	"case.request_evidence": {},
	"run.create":            {},
	"case.complete":         {},
}

var requiredTrafficReviewProfileTools = []string{"case.get", "case.request_evidence", "run.create"}

// AgentProfileServer 管理由贾维斯编排、由 agentd 承载短命 run 的调查 Agent。
type AgentProfileServer struct {
	pool *pgxpool.Pool
}

// NewAgentProfileServer 构造受管短命 Agent 服务。
func NewAgentProfileServer(pool *pgxpool.Pool) *AgentProfileServer {
	return &AgentProfileServer{pool: pool}
}

// Handler 返回 Connect 服务端处理器。
func (s *AgentProfileServer) Handler() (string, http.Handler) {
	return agentv1connect.NewAgentProfileServiceHandler(s, handlerOptions()...)
}

// ListAgentProfiles 按用户资产 Bindings 裁剪受管短命 Agent。
func (s *AgentProfileServer) ListAgentProfiles(ctx context.Context, req *connect.Request[agentv1.ListAgentProfilesRequest]) (*connect.Response[agentv1.ListAgentProfilesResponse], error) {
	_, scope, err := s.readScope(ctx, req)
	if err != nil {
		return nil, err
	}
	resp := &agentv1.ListAgentProfilesResponse{}
	if !scope.hasTool("console.read") || len(scope.assets) == 0 {
		return connect.NewResponse(resp), nil
	}
	limit := ClampPageSize(req.Msg.GetPageSize())
	if limit > agentProfilePageMax {
		limit = agentProfilePageMax
	}
	offset, err := decodePageOffset(req.Msg.GetPageToken())
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT `+agentProfileSelectColumns+`
		FROM managed_agent_profiles
		WHERE EXISTS (
			SELECT 1 FROM jsonb_array_elements(bindings) binding
			WHERE binding->>'kind'='asset' AND binding->>'id'=ANY($1::text[])
		)
		ORDER BY updated_at DESC, agent_id LIMIT $2 OFFSET $3`, scope.assetIDs(), limit+1, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		profile, err := scanAgentProfile(rows)
		if err != nil {
			return nil, err
		}
		profile.CanManage = canManageAgentProfile(profile.GetBindings(), scope)
		profile.Bindings = visibleProfileBindings(profile.GetBindings(), scope)
		resp.Profiles = append(resp.Profiles, profile)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(resp.Profiles) > limit {
		resp.Profiles = resp.Profiles[:limit]
		resp.NextPageToken = encodePageOffset(offset + limit)
	}
	return connect.NewResponse(resp), nil
}

// CreateAgentProfile 创建一个只用于流量案件的逻辑调查岗位。
func (s *AgentProfileServer) CreateAgentProfile(ctx context.Context, req *connect.Request[agentv1.CreateAgentProfileRequest]) (*connect.Response[agentv1.CreateAgentProfileResponse], error) {
	user, scope, err := s.manageScope(ctx, req)
	if err != nil {
		return nil, err
	}
	name, tools, bindings, assetIDs, err := validateAgentProfileInput(req.Msg.GetDisplayName(), req.Msg.GetTools(), req.Msg.GetBindings())
	if err != nil {
		return nil, err
	}
	if !scope.coversAllAssets(assetIDs) {
		return nil, objectDenied()
	}
	agentID, err := newID("profile")
	if err != nil {
		return nil, err
	}
	var profile *agentv1.AgentProfile
	err = withTx(ctx, s.pool, func(tx pgx.Tx) error {
		toolsRaw, bindsRaw, err := marshalAgentProfileScope(tools, bindings)
		if err != nil {
			return err
		}
		configDigest, err := digestAgentProfileConfiguration(agentID, name, tools, bindings)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO managed_agent_profiles(
			agent_id, display_name, kind, state, tools, bindings, created_by, execution_mode, config_digest)
			VALUES($1,$2,$3,$4,$5::jsonb,$6::jsonb,$7,'ephemeral_run',$8)`, agentID, name,
			agentProfileKindTrafficReview, agentProfileStateEnabled, toolsRaw, bindsRaw, user.GetUserId(), configDigest); err != nil {
			return err
		}
		profile, err = scanAgentProfile(tx.QueryRow(ctx, `SELECT `+agentProfileSelectColumns+`
			FROM managed_agent_profiles WHERE agent_id=$1`, agentID))
		if err != nil {
			return err
		}
		return appendAuditTx(ctx, tx, "user", user.GetUserId(), "agent_profile.create", "agent_profile", agentID,
			map[string]any{"asset_ids": assetIDs, "tools": tools})
	})
	if err != nil {
		return nil, err
	}
	profile.CanManage = canManageAgentProfile(profile.GetBindings(), scope)
	return connect.NewResponse(&agentv1.CreateAgentProfileResponse{Profile: profile}), nil
}

// UpdateAgentProfile 原子替换一个逻辑调查岗位的名称、状态、工具和资产范围。
func (s *AgentProfileServer) UpdateAgentProfile(ctx context.Context, req *connect.Request[agentv1.UpdateAgentProfileRequest]) (*connect.Response[agentv1.UpdateAgentProfileResponse], error) {
	user, scope, err := s.manageScope(ctx, req)
	if err != nil {
		return nil, err
	}
	agentID := strings.TrimSpace(req.Msg.GetAgentId())
	if agentID == "" || strings.HasPrefix(strings.ToLower(agentID), "jarvis") {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("managed agent id is required"))
	}
	name, tools, bindings, assetIDs, err := validateAgentProfileInput(req.Msg.GetDisplayName(), req.Msg.GetTools(), req.Msg.GetBindings())
	if err != nil {
		return nil, err
	}
	state, err := agentProfileStateString(req.Msg.GetState())
	if err != nil {
		return nil, err
	}
	if !scope.coversAllAssets(assetIDs) {
		return nil, objectDenied()
	}
	var profile *agentv1.AgentProfile
	err = withTx(ctx, s.pool, func(tx pgx.Tx) error {
		current, err := loadAgentProfileForUpdate(ctx, tx, agentID)
		if err != nil {
			return err
		}
		if !scope.coversAllAssets(profileAssetIDs(current.GetBindings())) {
			return objectDenied()
		}
		toolsRaw, bindsRaw, err := marshalAgentProfileScope(tools, bindings)
		if err != nil {
			return err
		}
		if current.GetState() == agentv1.AgentProfileState_AGENT_PROFILE_STATE_TOMBSTONED {
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("tombstoned managed agent cannot be updated"))
		}
		configDigest, err := digestAgentProfileConfiguration(agentID, name, tools, bindings)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE managed_agent_profiles
			SET display_name=$2, state=$3, tools=$4::jsonb, bindings=$5::jsonb, config_digest=$6, updated_at=now()
			WHERE agent_id=$1`, agentID, name, state, toolsRaw, bindsRaw, configDigest); err != nil {
			return err
		}
		profile, err = scanAgentProfile(tx.QueryRow(ctx, `SELECT `+agentProfileSelectColumns+`
			FROM managed_agent_profiles WHERE agent_id=$1`, agentID))
		if err != nil {
			return err
		}
		return appendAuditTx(ctx, tx, "user", user.GetUserId(), "agent_profile.update", "agent_profile", agentID,
			map[string]any{"asset_ids": assetIDs, "tools": tools, "state": state})
	})
	if err != nil {
		return nil, err
	}
	profile.CanManage = canManageAgentProfile(profile.GetBindings(), scope)
	return connect.NewResponse(&agentv1.UpdateAgentProfileResponse{Profile: profile}), nil
}

// DeleteAgentProfile 墓碑式停用短命 Agent 定义，并保留在途案件使用的冻结快照。
func (s *AgentProfileServer) DeleteAgentProfile(ctx context.Context, req *connect.Request[agentv1.DeleteAgentProfileRequest]) (*connect.Response[agentv1.DeleteAgentProfileResponse], error) {
	user, scope, err := s.manageScope(ctx, req)
	if err != nil {
		return nil, err
	}
	agentID := strings.TrimSpace(req.Msg.GetAgentId())
	if agentID == "" || strings.HasPrefix(strings.ToLower(agentID), "jarvis") {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("managed agent id is required"))
	}
	err = withTx(ctx, s.pool, func(tx pgx.Tx) error {
		profile, err := loadAgentProfileForUpdate(ctx, tx, agentID)
		if err != nil {
			return err
		}
		if !scope.coversAllAssets(profileAssetIDs(profile.GetBindings())) {
			return objectDenied()
		}
		if profile.GetState() == agentv1.AgentProfileState_AGENT_PROFILE_STATE_TOMBSTONED {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE managed_agent_profiles SET state='tombstoned',tombstoned_at=now(),updated_at=now()
			WHERE agent_id=$1`, agentID); err != nil {
			return err
		}
		return appendAuditTx(ctx, tx, "user", user.GetUserId(), "agent_profile.delete", "agent_profile", agentID,
			map[string]any{"asset_ids": profileAssetIDs(profile.GetBindings())})
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentv1.DeleteAgentProfileResponse{}), nil
}

// BatchUpdateAgentProfiles 用同一份工具与资产范围原子覆盖多个逻辑调查岗位。
func (s *AgentProfileServer) BatchUpdateAgentProfiles(ctx context.Context, req *connect.Request[agentv1.BatchUpdateAgentProfilesRequest]) (*connect.Response[agentv1.BatchUpdateAgentProfilesResponse], error) {
	user, scope, err := s.manageScope(ctx, req)
	if err != nil {
		return nil, err
	}
	ids, err := normalizeAgentProfileIDs(req.Msg.GetAgentIds())
	if err != nil {
		return nil, err
	}
	_, tools, bindings, assetIDs, err := validateAgentProfileInput("batch", req.Msg.GetTools(), req.Msg.GetBindings())
	if err != nil {
		return nil, err
	}
	if !scope.coversAllAssets(assetIDs) {
		return nil, objectDenied()
	}
	resp := &agentv1.BatchUpdateAgentProfilesResponse{}
	err = withTx(ctx, s.pool, func(tx pgx.Tx) error {
		for _, id := range ids {
			profile, err := loadAgentProfileForUpdate(ctx, tx, id)
			if err != nil {
				return err
			}
			if !scope.coversAllAssets(profileAssetIDs(profile.GetBindings())) {
				return objectDenied()
			}
		}
		toolsRaw, bindsRaw, err := marshalAgentProfileScope(tools, bindings)
		if err != nil {
			return err
		}
		for _, id := range ids {
			current, err := loadAgentProfileForUpdate(ctx, tx, id)
			if err != nil {
				return err
			}
			if current.GetState() == agentv1.AgentProfileState_AGENT_PROFILE_STATE_TOMBSTONED {
				return connect.NewError(connect.CodeFailedPrecondition, errors.New("tombstoned managed agent cannot be batch updated"))
			}
			configDigest, err := digestAgentProfileConfiguration(id, current.GetDisplayName(), tools, bindings)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE managed_agent_profiles SET tools=$2::jsonb,bindings=$3::jsonb,
				config_digest=$4,updated_at=now() WHERE agent_id=$1`, id, toolsRaw, bindsRaw, configDigest); err != nil {
				return err
			}
			profile, err := scanAgentProfile(tx.QueryRow(ctx, `SELECT `+agentProfileSelectColumns+`
				FROM managed_agent_profiles WHERE agent_id=$1`, id))
			if err != nil {
				return err
			}
			resp.Profiles = append(resp.Profiles, profile)
		}
		return appendAuditTx(ctx, tx, "user", user.GetUserId(), "agent_profile.batch_update", "agent_profile_batch", strings.Join(ids, ","),
			map[string]any{"agent_ids": ids, "asset_ids": assetIDs, "tools": tools})
	})
	if err != nil {
		return nil, err
	}
	for _, profile := range resp.Profiles {
		profile.CanManage = canManageAgentProfile(profile.GetBindings(), scope)
	}
	return connect.NewResponse(resp), nil
}

func (s *AgentProfileServer) readScope(ctx context.Context, req interface{ Header() http.Header }) (*authv1.User, accessScope, error) {
	user, err := requireUser(ctx, s.pool, req)
	if err != nil {
		return nil, accessScope{}, err
	}
	if err := requireCompletedOnboarding(ctx, s.pool); err != nil {
		return nil, accessScope{}, err
	}
	access, err := loadEffectiveAccess(ctx, s.pool, user)
	if err != nil {
		return nil, accessScope{}, err
	}
	return user, scopeFromAccess(access), nil
}

func (s *AgentProfileServer) manageScope(ctx context.Context, req interface{ Header() http.Header }) (*authv1.User, accessScope, error) {
	user, scope, err := s.readScope(ctx, req)
	if err != nil {
		return nil, accessScope{}, err
	}
	if !scope.hasTool("agent.manage") {
		return nil, accessScope{}, grantMissingError()
	}
	return user, scope, nil
}

func validateAgentProfileInput(displayName string, rawTools []string, rawBindings []*grantv1.BindingRef) (string, []string, []*grantv1.BindingRef, []string, error) {
	name := strings.TrimSpace(displayName)
	if name == "" || len([]rune(name)) > 80 {
		return "", nil, nil, nil, connect.NewError(connect.CodeInvalidArgument, errors.New("display name must be between 1 and 80 characters"))
	}
	toolSet := map[string]struct{}{}
	for _, raw := range rawTools {
		tool := strings.TrimSpace(raw)
		if _, ok := trafficReviewProfileTools[tool]; !ok {
			return "", nil, nil, nil, connect.NewError(connect.CodeInvalidArgument, errors.New("unsupported traffic review agent tool"))
		}
		toolSet[tool] = struct{}{}
	}
	if len(toolSet) == 0 {
		return "", nil, nil, nil, connect.NewError(connect.CodeInvalidArgument, errors.New("at least one traffic review agent tool is required"))
	}
	for _, tool := range requiredTrafficReviewProfileTools {
		if _, ok := toolSet[tool]; !ok {
			return "", nil, nil, nil, connect.NewError(connect.CodeInvalidArgument, errors.New("traffic review agent requires case.get, case.request_evidence and run.create"))
		}
	}
	tools := make([]string, 0, len(toolSet))
	for tool := range toolSet {
		tools = append(tools, tool)
	}
	sort.Strings(tools)
	assetSet := map[string]struct{}{}
	for _, binding := range rawBindings {
		if binding == nil || binding.GetKind() != "asset" {
			return "", nil, nil, nil, connect.NewError(connect.CodeInvalidArgument, errors.New("managed agent bindings must be assets"))
		}
		id := strings.TrimSpace(binding.GetId())
		if id == "" || id == "*" {
			return "", nil, nil, nil, connect.NewError(connect.CodeInvalidArgument, errors.New("managed agent asset binding must be concrete"))
		}
		assetSet[id] = struct{}{}
	}
	if len(assetSet) == 0 {
		return "", nil, nil, nil, connect.NewError(connect.CodeInvalidArgument, errors.New("at least one managed asset is required"))
	}
	assetIDs := make([]string, 0, len(assetSet))
	for id := range assetSet {
		assetIDs = append(assetIDs, id)
	}
	sort.Strings(assetIDs)
	bindings := make([]*grantv1.BindingRef, 0, len(assetIDs))
	for _, id := range assetIDs {
		bindings = append(bindings, &grantv1.BindingRef{Kind: "asset", Id: id})
	}
	return name, tools, bindings, assetIDs, nil
}

func normalizeAgentProfileIDs(raw []string) ([]string, error) {
	set := map[string]struct{}{}
	for _, value := range raw {
		id := strings.TrimSpace(value)
		if id == "" || strings.HasPrefix(strings.ToLower(id), "jarvis") {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("managed agent ids must not include jarvis"))
		}
		set[id] = struct{}{}
	}
	if len(set) == 0 || len(set) > 100 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("batch must include between 1 and 100 managed agents"))
	}
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

func agentProfileStateString(state agentv1.AgentProfileState) (string, error) {
	switch state {
	case agentv1.AgentProfileState_AGENT_PROFILE_STATE_ENABLED:
		return agentProfileStateEnabled, nil
	case agentv1.AgentProfileState_AGENT_PROFILE_STATE_DISABLED:
		return agentProfileStateDisabled, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("managed agent state is required"))
	}
}

func protoAgentProfileKind(kind string) agentv1.AgentProfileKind {
	if kind == agentProfileKindTrafficReview {
		return agentv1.AgentProfileKind_AGENT_PROFILE_KIND_TRAFFIC_REVIEW
	}
	return agentv1.AgentProfileKind_AGENT_PROFILE_KIND_UNSPECIFIED
}

func protoAgentProfileState(state string) agentv1.AgentProfileState {
	if state == agentProfileStateEnabled {
		return agentv1.AgentProfileState_AGENT_PROFILE_STATE_ENABLED
	}
	if state == agentProfileStateDisabled {
		return agentv1.AgentProfileState_AGENT_PROFILE_STATE_DISABLED
	}
	if state == agentProfileStateTombstoned {
		return agentv1.AgentProfileState_AGENT_PROFILE_STATE_TOMBSTONED
	}
	return agentv1.AgentProfileState_AGENT_PROFILE_STATE_UNSPECIFIED
}

func scanAgentProfile(row interface{ Scan(...any) error }) (*agentv1.AgentProfile, error) {
	var agentID, name, kind, state, createdBy, executionMode, configDigest, lastWorkerID, lastWorkerPlatform string
	var toolsRaw, bindingsRaw []byte
	var created, updated time.Time
	var lastRun, tombstoned *time.Time
	var activeRuns int32
	if err := row.Scan(&agentID, &name, &kind, &state, &toolsRaw, &bindingsRaw, &createdBy, &created, &updated,
		&executionMode, &configDigest, &activeRuns, &lastRun, &tombstoned, &lastWorkerID, &lastWorkerPlatform); err != nil {
		return nil, err
	}
	var tools []string
	var bindings []*grantv1.BindingRef
	if err := json.Unmarshal(toolsRaw, &tools); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(bindingsRaw, &bindings); err != nil {
		return nil, err
	}
	profile := &agentv1.AgentProfile{AgentId: agentID, DisplayName: name, Kind: protoAgentProfileKind(kind),
		State: protoAgentProfileState(state), Tools: tools, Bindings: bindings, CreatedBy: createdBy,
		ConfigDigest: configDigest, ActiveRunCount: activeRuns, LastWorkerId: lastWorkerID, LastWorkerPlatform: lastWorkerPlatform,
		CreatedAt: timestamppb.New(created), UpdatedAt: timestamppb.New(updated)}
	if executionMode == "ephemeral_run" {
		profile.ExecutionMode = agentv1.AgentExecutionMode_AGENT_EXECUTION_MODE_EPHEMERAL_RUN
	}
	if lastRun != nil {
		profile.LastRunAt = timestamppb.New(*lastRun)
	}
	if tombstoned != nil {
		profile.TombstonedAt = timestamppb.New(*tombstoned)
	}
	return profile, nil
}

func marshalAgentProfileScope(tools []string, bindings []*grantv1.BindingRef) (string, string, error) {
	toolsRaw, err := json.Marshal(tools)
	if err != nil {
		return "", "", err
	}
	bindingsRaw, err := json.Marshal(bindings)
	if err != nil {
		return "", "", err
	}
	return string(toolsRaw), string(bindingsRaw), nil
}

func loadAgentProfileForUpdate(ctx context.Context, tx pgx.Tx, agentID string) (*agentv1.AgentProfile, error) {
	profile, err := scanAgentProfile(tx.QueryRow(ctx, `SELECT `+agentProfileSelectColumns+`
		FROM managed_agent_profiles WHERE agent_id=$1 FOR UPDATE`, agentID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, objectDenied()
	}
	return profile, err
}

func digestAgentProfileConfiguration(agentID, displayName string, tools []string, bindings []*grantv1.BindingRef) (string, error) {
	frozenBindings := make([]frozenAgentBinding, 0, len(bindings))
	for _, binding := range bindings {
		if binding != nil {
			frozenBindings = append(frozenBindings, frozenAgentBinding{Kind: binding.GetKind(), ID: binding.GetId()})
		}
	}
	return digestFrozenAgentProfile(frozenAgentProfile{AgentID: agentID, DisplayName: displayName,
		Kind: agentProfileKindTrafficReview, Tools: tools, Bindings: frozenBindings})
}

func profileAssetIDs(bindings []*grantv1.BindingRef) []string {
	ids := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		if binding != nil && binding.GetKind() == "asset" && binding.GetId() != "" {
			ids = append(ids, binding.GetId())
		}
	}
	return ids
}

func visibleProfileBindings(bindings []*grantv1.BindingRef, scope accessScope) []*grantv1.BindingRef {
	out := make([]*grantv1.BindingRef, 0, len(bindings))
	for _, binding := range bindings {
		if binding != nil && binding.GetKind() == "asset" && scope.coversAsset(binding.GetId()) {
			out = append(out, binding)
		}
	}
	return out
}

func canManageAgentProfile(bindings []*grantv1.BindingRef, scope accessScope) bool {
	if !scope.hasTool("agent.manage") || len(bindings) == 0 {
		return false
	}
	assetIDs := profileAssetIDs(bindings)
	return len(assetIDs) == len(bindings) && scope.coversAllAssets(assetIDs)
}
