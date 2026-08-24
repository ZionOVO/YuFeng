package brain

import (
	"context"
	"crypto/ed25519"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/types/known/timestamppb"

	authv1 "yufeng/proto/gen/authv1"
	commonv1 "yufeng/proto/gen/commonv1"
	modelv1 "yufeng/proto/gen/modelv1"
	"yufeng/proto/gen/modelv1/modelv1connect"
	onboardingv1 "yufeng/proto/gen/onboardingv1"
	"yufeng/proto/gen/onboardingv1/onboardingv1connect"

	"yufeng/lib/kernel"
)

// OnboardingServer 装配引导状态机与模型网关。生产未注册则拒绝启动。
//
// [初次配置引导]: ../../docs/glossary.md#onboarding
type OnboardingServer struct {
	onboardingv1connect.UnimplementedOnboardingServiceHandler
	pool           *pgxpool.Pool
	jarvisID       string
	defaultModel   string
	httpClient     *http.Client
	completeFn     func(context.Context, string, string, string, []chatMessage) (string, error)
	signingKey     ed25519.PrivateKey
	capabilityPub  ed25519.PublicKey
	artifactPub    ed25519.PublicKey
	artifactSigner kernel.Signer
	sensitiveRelay *SensitiveRelay
}

// NewOnboardingServer 构造引导服务；超时与缺省模型只引用 kernel 常量。
func NewOnboardingServer(pool *pgxpool.Pool, jarvisID string) *OnboardingServer {
	if strings.TrimSpace(jarvisID) == "" {
		jarvisID = defaultJarvisAgentID
	}
	return &OnboardingServer{
		pool:         pool,
		jarvisID:     jarvisID,
		defaultModel: kernel.DefaultChatModel,
		httpClient:   &http.Client{Timeout: kernel.ChatCompleteTimeout},
	}
}

// Handler 返回 OnboardingService 处理器。
func (s *OnboardingServer) Handler() (string, http.Handler) {
	return onboardingv1connect.NewOnboardingServiceHandler(s, handlerOptions()...)
}

// edgeReady 只依据已认证单元心跳中回执的签名制品坐标判定人工部署是否就绪。
func edgeReady(ctx context.Context, db dbTX, view onboardingView) bool {
	if view.LocalUnitID == "" || view.LocalAssetID == "" || view.LocalAssetID == "bootstrap" ||
		view.ExpectedGenerationID == "" || view.ExpectedGenerationSeq <= 0 || view.ExpectedListenPlanVersion == 0 {
		return false
	}
	var ready bool
	err := db.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM units u JOIN unit_assets ua ON ua.unit_id=u.unit_id
		WHERE u.unit_id=$1 AND ua.asset_id=$2 AND u.last_heartbeat_at >= $3
		  AND u.current_generation_id=$4 AND u.current_generation_seq=$5
		  AND u.current_listen_plan_version=$6)`,
		view.LocalUnitID, view.LocalAssetID, time.Now().Add(-kernel.EdgeOnlineWindow),
		view.ExpectedGenerationID, view.ExpectedGenerationSeq, view.ExpectedListenPlanVersion).Scan(&ready)
	return err == nil && ready
}

// ModelHandler 返回 ModelGatewayService 处理器。
func (s *OnboardingServer) ModelHandler() (string, http.Handler) {
	return modelv1connect.NewModelGatewayServiceHandler(s, handlerOptions()...)
}

// GetOnboarding 返回初次配置状态及下一步所需的非敏感信息。
func (s *OnboardingServer) GetOnboarding(ctx context.Context, req *connect.Request[onboardingv1.GetOnboardingRequest]) (*connect.Response[onboardingv1.GetOnboardingResponse], error) {
	if _, err := requireUser(ctx, s.pool, req); err != nil {
		return nil, err
	}
	view, err := loadOnboardingView(ctx, s.pool)
	if err != nil {
		return nil, err
	}
	updated := timestamppb.Now()
	if !view.UpdatedAt.IsZero() {
		updated = timestamppb.New(view.UpdatedAt)
	}
	ready := edgeReady(ctx, s.pool, view)
	state := view.State
	if ready && state != OnboardingStateCompleted {
		state = OnboardingStateEdgeLive
	}
	return connect.NewResponse(&onboardingv1.GetOnboardingResponse{
		State:                     protoOnboardingState(state),
		BaseUrl:                   view.BaseURL,
		Model:                     view.Model,
		HasSecret:                 view.HasSecret,
		SecretHint:                view.SecretHint,
		JarvisOnline:              jarvisOnline(ctx, s.pool, s.jarvisID),
		LocalAssetId:              view.publicAssetID(),
		LastError:                 view.LastError,
		UpdatedAt:                 updated,
		Dialect:                   protoModelDialect(view.Dialect),
		EdgeReady:                 ready,
		LocalUnitId:               view.LocalUnitID,
		DeploymentSpecDigest:      view.DeploymentSpecDigest,
		ExpectedGenerationId:      view.ExpectedGenerationID,
		ExpectedGenerationSeq:     view.ExpectedGenerationSeq,
		ExpectedListenPlanVersion: view.ExpectedListenPlanVersion,
	}), nil
}

// PutModelConfig 校验并保存模型端点、方言、模型名和只写密钥。
func (s *OnboardingServer) PutModelConfig(ctx context.Context, req *connect.Request[onboardingv1.PutModelConfigRequest]) (*connect.Response[onboardingv1.PutModelConfigResponse], error) {
	if _, err := s.requireAdmin(ctx, req); err != nil {
		return nil, err
	}
	resp := &onboardingv1.PutModelConfigResponse{}
	err := idempotentProto(ctx, s.pool, "onboarding.put_model", idempotencyKey(req.Header()), req.Msg, resp, func(tx pgx.Tx) error {
		view, err := loadOnboardingViewTx(ctx, tx, true)
		if err != nil {
			return err
		}
		if err := onboardingEdgeError(view.State, actionPutModelConfig, view.HasSecret, view.ModelLive); err != nil {
			return err
		}
		baseURL := strings.TrimSpace(req.Msg.GetBaseUrl())
		if err := requireAbsoluteHTTPS(baseURL); err != nil {
			return connect.NewError(connect.CodeInvalidArgument, err)
		}
		secret := req.Msg.GetSecret()
		if strings.TrimSpace(secret) == "" {
			return connect.NewError(connect.CodeInvalidArgument, errors.New("secret is required"))
		}
		model := strings.TrimSpace(req.Msg.GetModel())
		if model == "" {
			model = s.defaultModel
		}
		dialect, err := dialectFromProto(req.Msg.GetDialect())
		if err != nil {
			return connect.NewError(connect.CodeInvalidArgument, err)
		}
		if dialect == "" {
			dialect = modelDialectOpenAIChat
		}
		if err := writeModelSecret(ctx, tx, secret); err != nil {
			return err
		}
		if err := writeOnboardingRow(ctx, tx, OnboardingStateModelConfigured, view.LocalAssetID, baseURL, model, "", false); err != nil {
			return err
		}
		return writeOnboardingSlot(ctx, tx, baseURL, model, dialect)
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

// TestModelConnectivity 使用服务端凭据槽探测模型端点，并据结果推进初次配置状态。
func (s *OnboardingServer) TestModelConnectivity(ctx context.Context, req *connect.Request[onboardingv1.TestModelConnectivityRequest]) (*connect.Response[onboardingv1.TestModelConnectivityResponse], error) {
	if _, err := s.requireAdmin(ctx, req); err != nil {
		return nil, err
	}
	scope := "onboarding.test_model"
	resp := &onboardingv1.TestModelConnectivityResponse{}
	key, digest, execute, err := beginIdempotentProto(ctx, s.pool, scope, idempotencyKey(req.Header()), req.Msg, resp)
	if err != nil {
		return nil, err
	}
	if !execute {
		return connect.NewResponse(resp), nil
	}
	defer func() { _ = abortIdempotency(context.WithoutCancel(ctx), s.pool, scope, key) }()
	var view onboardingView
	err = withTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		view, err = loadOnboardingViewTx(ctx, tx, true)
		if err != nil {
			return err
		}
		return onboardingEdgeError(view.State, actionTestModel, view.HasSecret, view.ModelLive)
	})
	if err != nil {
		return nil, err
	}
	text, probeErr := s.completeChat(ctx, view, []chatMessage{{Role: "user", Content: "ping"}})
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	locked, err := loadOnboardingViewTx(ctx, tx, true)
	if err != nil {
		return nil, err
	}
	if locked.State == OnboardingStateCompleted || onboardingSlotChanged(view, locked) {
		return nil, connect.NewError(connect.CodeAborted, errors.New("onboarding changed during probe"))
	}
	if err := onboardingEdgeError(locked.State, actionTestModel, locked.HasSecret, locked.ModelLive); err != nil {
		return nil, err
	}
	var callErr error
	if probeErr != nil || strings.TrimSpace(text) == "" {
		cause := probeErr
		if cause == nil {
			cause = errors.New("model endpoint returned empty text")
		}
		callErr = connect.NewError(probeConnectCode(cause), errors.New(lowercaseError(cause)))
		if err := writeOnboardingRow(ctx, tx, OnboardingStateFailed, locked.LocalAssetID, locked.BaseURL, locked.Model, lowercaseError(cause), false); err != nil {
			return nil, err
		}
	} else if err := writeOnboardingRow(ctx, tx, OnboardingStateModelLive, locked.LocalAssetID, locked.BaseURL, locked.Model, "", true); err != nil {
		return nil, err
	}
	if callErr != nil {
		err = storeIdempotentErrorDB(ctx, tx, scope, key, digest, callErr)
	} else {
		err = storeIdempotentProtoDB(ctx, tx, scope, key, digest, resp)
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	if callErr != nil {
		return nil, callErr
	}
	return connect.NewResponse(resp), nil
}

// PutDeploymentSpecification 由 Brain 确定性签发人工安装 Edge 所需的监听计划、基线世代和模型档案。
func (s *OnboardingServer) PutDeploymentSpecification(ctx context.Context, req *connect.Request[onboardingv1.PutDeploymentSpecificationRequest]) (*connect.Response[onboardingv1.PutDeploymentSpecificationResponse], error) {
	user, err := s.requireAdmin(ctx, req)
	if err != nil {
		return nil, err
	}
	spec, err := normalizeDeploymentSpec(req.Msg)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	resp := &onboardingv1.PutDeploymentSpecificationResponse{}
	err = idempotentProto(ctx, s.pool, "onboarding.put_deployment_specification", idempotencyKey(req.Header()), spec.Request, resp, func(tx pgx.Tx) error {
		view, err := loadOnboardingViewTx(ctx, tx, true)
		if err != nil {
			return err
		}
		if err := onboardingEdgeError(view.State, actionPutDeploymentSpec, view.HasSecret, view.ModelLive); err != nil {
			return err
		}
		if view.DeploymentSpecDigest == spec.Digest && view.ExpectedListenPlanVersion > 0 && view.ExpectedGenerationID != "" {
			resp.UnitId = view.LocalUnitID
			resp.AssetId = view.LocalAssetID
			resp.DeploymentSpecDigest = view.DeploymentSpecDigest
			resp.ListenPlanVersion = view.ExpectedListenPlanVersion
			resp.GenerationId = view.ExpectedGenerationID
			resp.GenerationSeq = view.ExpectedGenerationSeq
			return nil
		}
		listenVersion, generationID, generationSeq, err := publishDeploymentSpecification(
			ctx, tx, spec, s.signingKey, s.artifactSigner, user.GetUserId())
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE deployment_onboarding SET state=$1,last_error='',updated_at=now() WHERE id=1`, OnboardingStateModelLive); err != nil {
			return err
		}
		resp.UnitId = spec.Request.GetUnitId()
		resp.AssetId = spec.Request.GetAssetId()
		resp.DeploymentSpecDigest = spec.Digest
		resp.ListenPlanVersion = listenVersion
		resp.GenerationId = generationID
		resp.GenerationSeq = generationSeq
		return nil
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

// CompleteOnboarding 仅在模型、智能代理、数据面、本机资产和权限授予全部就绪时关闭初次配置。
func (s *OnboardingServer) CompleteOnboarding(ctx context.Context, req *connect.Request[onboardingv1.CompleteOnboardingRequest]) (*connect.Response[onboardingv1.CompleteOnboardingResponse], error) {
	user, err := s.requireAdmin(ctx, req)
	if err != nil {
		return nil, err
	}
	scope := "onboarding.complete"
	resp := &onboardingv1.CompleteOnboardingResponse{}
	key, digest, execute, err := beginIdempotentProto(ctx, s.pool, scope, idempotencyKey(req.Header()), req.Msg, resp)
	if err != nil {
		return nil, err
	}
	if !execute {
		return connect.NewResponse(resp), nil
	}
	defer func() { _ = abortIdempotency(context.WithoutCancel(ctx), s.pool, scope, key) }()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	view, err := loadOnboardingViewTx(ctx, tx, true)
	if err != nil {
		return nil, err
	}
	if err := onboardingEdgeError(view.State, actionCompleteOnboarding, view.HasSecret, view.ModelLive); err != nil {
		return nil, err
	}
	ready := edgeReady(ctx, tx, view)
	missing := missingCompletePredicates(ctx, tx, completeCheck{
		AdminUserID:   user.UserId,
		JarvisAgentID: s.jarvisID,
		LocalAssetID:  view.LocalAssetID,
		ModelLive:     view.ModelLive,
		EdgeReady:     ready,
	})
	if len(missing) > 0 {
		return nil, onboardingGateError(missing)
	}
	ids := []string{view.LocalAssetID}
	extra, err := listAssetIDs(ctx, tx)
	if err != nil {
		return nil, err
	}
	ids = append(ids, extra...)
	if err := writeAdminSystemGrantAssets(ctx, tx, user.UserId, ids); err != nil {
		return nil, err
	}
	if err := writeOnboardingRow(ctx, tx, OnboardingStateCompleted, view.LocalAssetID, view.BaseURL, view.Model, "", true); err != nil {
		return nil, err
	}
	if err := storeIdempotentProtoDB(ctx, tx, scope, key, digest, resp); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

// CompleteChat 通过中台唯一模型出口完成兼容性聊天补全；生产智能代理使用 Generate。
func (s *OnboardingServer) CompleteChat(ctx context.Context, req *connect.Request[modelv1.CompleteChatRequest]) (*connect.Response[modelv1.CompleteChatResponse], error) {
	if _, err := requireAgent(ctx, s.pool, req); err != nil {
		return nil, err
	}
	view, err := loadOnboardingView(ctx, s.pool)
	if err != nil {
		return nil, err
	}
	if !view.completed() || !view.HasSecret {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("onboarding is incomplete or model secret is missing"))
	}
	var msgs []chatMessage
	for _, m := range req.Msg.GetMessages() {
		if m == nil {
			continue
		}
		msgs = append(msgs, chatMessage{Role: m.GetRole(), Content: m.GetContent()})
	}
	if len(msgs) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("messages are required"))
	}
	started := time.Now()
	text, err := s.completeChatJSON(ctx, view, msgs)
	s.noteGatewayCall(ctx, view, gatewayCallComplete, text, err, started)
	if err != nil {
		return nil, connect.NewError(probeConnectCode(err), errors.New(lowercaseError(err)))
	}
	if strings.TrimSpace(text) == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("model endpoint returned empty text"))
	}
	return connect.NewResponse(&modelv1.CompleteChatResponse{Text: text}), nil
}

// GetModelGateway 返回管理员可见的模型槽配置与脱敏运行统计。
func (s *OnboardingServer) GetModelGateway(ctx context.Context, req *connect.Request[modelv1.GetModelGatewayRequest]) (*connect.Response[modelv1.GetModelGatewayResponse], error) {
	if _, err := s.requireAdmin(ctx, req); err != nil {
		return nil, err
	}
	resp, err := s.loadModelGateway(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

// UpdateModelGateway 校验并更新已完成初次配置后的模型槽，不回退引导状态。
func (s *OnboardingServer) UpdateModelGateway(ctx context.Context, req *connect.Request[modelv1.UpdateModelGatewayRequest]) (*connect.Response[modelv1.UpdateModelGatewayResponse], error) {
	if _, err := s.requireAdmin(ctx, req); err != nil {
		return nil, err
	}
	resp := &modelv1.UpdateModelGatewayResponse{}
	err := idempotentProto(ctx, s.pool, "model.update_gateway", idempotencyKey(req.Header()), req.Msg, resp, func(tx pgx.Tx) error {
		view, err := loadOnboardingViewTx(ctx, tx, true)
		if err != nil {
			return err
		}
		if !view.completed() {
			return onboardingIncompleteError()
		}
		baseURL := strings.TrimSpace(req.Msg.GetBaseUrl())
		if err := requireAbsoluteHTTPS(baseURL); err != nil {
			return connect.NewError(connect.CodeInvalidArgument, err)
		}
		if secret := req.Msg.GetSecret(); strings.TrimSpace(secret) != "" {
			if err := writeModelSecret(ctx, tx, secret); err != nil {
				return err
			}
		}
		model := strings.TrimSpace(req.Msg.GetModel())
		if model == "" {
			model = strings.TrimSpace(view.Model)
		}
		if model == "" {
			model = s.defaultModel
		}
		dialect, err := dialectFromProto(req.Msg.GetDialect())
		if err != nil {
			return connect.NewError(connect.CodeInvalidArgument, err)
		}
		if dialect == "" {
			dialect = view.Dialect
		}
		if dialect == "" {
			dialect = modelDialectOpenAIChat
		}
		if err := writeOnboardingSlot(ctx, tx, baseURL, model, dialect); err != nil {
			return err
		}
		updated, err := loadOnboardingViewTx(ctx, tx, false)
		if err != nil {
			return err
		}
		win, err := loadGatewayWindow(ctx, s.pool, updated.BaseURL)
		if err != nil {
			return err
		}
		copyModelGateway(resp, projectModelGateway(updated, win))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

// ProbeModelGateway 使用当前模型槽执行管理员探测并记录窗口统计。
func (s *OnboardingServer) ProbeModelGateway(ctx context.Context, req *connect.Request[modelv1.ProbeModelGatewayRequest]) (*connect.Response[modelv1.ProbeModelGatewayResponse], error) {
	if _, err := s.requireAdmin(ctx, req); err != nil {
		return nil, err
	}
	view, err := s.requireCompletedView(ctx)
	if err != nil {
		return nil, err
	}
	if !view.HasSecret {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("model secret is missing"))
	}
	started := time.Now()
	text, err := s.completeChat(ctx, view, []chatMessage{{Role: "user", Content: "ping"}})
	s.noteGatewayCall(ctx, view, gatewayCallProbe, text, err, started)
	if err != nil || strings.TrimSpace(text) == "" {
		if err == nil {
			err = errors.New("model endpoint returned empty text")
		}
		return nil, connect.NewError(probeConnectCode(err), errors.New(lowercaseError(err)))
	}
	return connect.NewResponse(&modelv1.ProbeModelGatewayResponse{Ok: true}), nil
}

func (s *OnboardingServer) requireCompletedView(ctx context.Context) (onboardingView, error) {
	view, err := loadOnboardingView(ctx, s.pool)
	if err != nil {
		return view, err
	}
	if !view.completed() {
		return view, onboardingIncompleteError()
	}
	return view, nil
}

func (s *OnboardingServer) loadModelGateway(ctx context.Context) (*modelv1.GetModelGatewayResponse, error) {
	view, err := s.requireCompletedView(ctx)
	if err != nil {
		return nil, err
	}
	win, err := loadGatewayWindow(ctx, s.pool, view.BaseURL)
	if err != nil {
		return nil, err
	}
	return projectModelGateway(view, win), nil
}

func (s *OnboardingServer) noteGatewayCall(ctx context.Context, view onboardingView, kind, text string, callErr error, started time.Time) {
	ok := callErr == nil && strings.TrimSpace(text) != ""
	msg := ""
	if !ok {
		if callErr != nil {
			msg = lowercaseError(callErr)
		} else {
			msg = "model endpoint returned empty text"
		}
	}
	model := view.Model
	if model == "" {
		model = s.defaultModel
	}
	// 记账失败不得挡已完成的出网。
	_ = recordGatewayCall(ctx, s.pool, gatewayCall{
		Kind:     kind,
		OK:       ok,
		Host:     modelHostOf(view.BaseURL),
		Model:    model,
		Latency:  time.Since(started),
		Err:      msg,
		Occurred: started,
	})
}

func (s *OnboardingServer) completeChat(ctx context.Context, view onboardingView, messages []chatMessage) (string, error) {
	if s.completeFn != nil {
		return s.completeFn(ctx, view.BaseURL, view.SecretPlain, view.Model, messages)
	}
	return postModelCompletion(ctx, s.httpClient, slotFromView(view, s.defaultModel), messages, chatCompletionSpec{
		MaxTokens: kernel.ChatProbeMaxTokens,
	})
}

func (s *OnboardingServer) completeChatJSON(ctx context.Context, view onboardingView, messages []chatMessage) (string, error) {
	if s.completeFn != nil {
		return s.completeFn(ctx, view.BaseURL, view.SecretPlain, view.Model, messages)
	}
	return postModelCompletion(ctx, s.httpClient, slotFromView(view, s.defaultModel), messages, chatCompletionSpec{
		MaxTokens: kernel.ChatCompleteMaxTokens,
		JSONMode:  true,
	})
}

func (s *OnboardingServer) requireAdmin(ctx context.Context, req interface{ Header() http.Header }) (*authv1.User, error) {
	user, err := requireUser(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	if user.Role != commonv1.UserRole_USER_ROLE_ADMIN {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("admin role required"))
	}
	return user, nil
}

func requireAbsoluteHTTPS(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || !u.IsAbs() {
		return errors.New("base_url must be an absolute https url")
	}
	return nil
}

func lowercaseError(err error) string {
	if err == nil {
		return "model request failed"
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	msg = strings.TrimRight(msg, ".!。")
	if msg == "" {
		return "model request failed"
	}
	return msg
}

func probeConnectCode(err error) connect.Code {
	if err == nil {
		return connect.CodeFailedPrecondition
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "unreachable") || strings.Contains(msg, "status 5") || strings.Contains(msg, "timeout") {
		return connect.CodeUnavailable
	}
	return connect.CodeFailedPrecondition
}

// CheckRequiredServices 生产装配未挂授予/引导/模型网关则拒绝启动。
func CheckRequiredServices(registered map[string]struct{}) error {
	for _, p := range requiredServicePaths() {
		if _, ok := registered[p]; !ok {
			return errors.New("production requires " + strings.Trim(p, "/"))
		}
	}
	return nil
}

func requiredServicePaths() []string {
	return []string{
		"/yufeng.auth.v1.AuthService/",
		"/yufeng.user.v1.UserService/",
		"/yufeng.grant.v1.GrantService/",
		"/yufeng.asset.v1.AssetService/",
		"/yufeng.console.v1.ConsoleService/",
		"/yufeng.govern.v1.GovernService/",
		"/yufeng.audit.v1.AuditService/",
		"/yufeng.onboarding.v1.OnboardingService/",
		"/yufeng.model.v1.ModelGatewayService/",
		"/yufeng.modelside.v1.ModelResultService/",
		"/yufeng.session.v1.SessionService/",
		"/yufeng.case.v1.CaseService/",
		"/yufeng.evidence.v1.EvidenceService/",
		"/yufeng.module.v1.ModuleCatalogService/",
		"/yufeng.agent.v1.AgentInteractionService/",
		"/yufeng.agent.v1.AgentProfileService/",
		"/yufeng.worker.v1.WorkerService/",
	}
}
