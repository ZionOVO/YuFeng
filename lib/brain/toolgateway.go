package brain

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/durationpb"

	artifactv1 "yufeng/proto/gen/artifactv1"
	eventv1 "yufeng/proto/gen/eventv1"
	governv1 "yufeng/proto/gen/governv1"
	toolgatewayv1 "yufeng/proto/gen/toolgatewayv1"
	"yufeng/proto/gen/toolgatewayv1/toolgatewayv1connect"
	workerv1 "yufeng/proto/gen/workerv1"

	agenttools "yufeng/agents/tools"
	"yufeng/lib/edgecore"
	"yufeng/lib/kernel"
)

// ToolGatewayServer 是智能代理统一工具调用网关。
type ToolGatewayServer struct {
	pool            *pgxpool.Pool
	key             ed25519.PrivateKey
	pub             ed25519.PublicKey
	artifactPub     ed25519.PublicKey
	implementations *agenttools.Registry
	demoTriage      bool
	invokeRate      *windowLimiter
	// artifactSigner 与 GovernServer 共用生产套接字签发器。
	// 空则退回 key（测试与 -dev-insecure）。工具路径不得另造一把内存钥。
	artifactSigner kernel.Signer
	sensitiveRelay *SensitiveRelay
}

// NewToolGatewayServer 构造工具网关。
func NewToolGatewayServer(pool *pgxpool.Pool, key ed25519.PrivateKey) *ToolGatewayServer {
	pub := key.Public().(ed25519.PublicKey)
	return &ToolGatewayServer{
		pool: pool, key: key, pub: pub, artifactPub: pub, implementations: firstPartyToolRegistry(),
		invokeRate: newWindowLimiter(kernel.ToolInvokeQPS, time.Second),
	}
}

// Handler 返回 Connect 服务端处理器。
func (s *ToolGatewayServer) Handler() (string, http.Handler) {
	return toolgatewayv1connect.NewToolGatewayServiceHandler(s, handlerOptions()...)
}

// ListTools 按访问令牌、能力令牌与当前目录版本返回可见工具的短描述。
func (s *ToolGatewayServer) ListTools(ctx context.Context, req *connect.Request[toolgatewayv1.ListToolsRequest]) (*connect.Response[toolgatewayv1.ListToolsResponse], error) {
	claims, err := s.authenticateToolHeaders(ctx, req.Header())
	if err != nil {
		return nil, err
	}
	if s.demoTriage {
		return connect.NewResponse(&toolgatewayv1.ListToolsResponse{Tools: filterToolItems(demoToolItems(s.implementations), claims.Tools)}), nil
	}
	items, err := s.listPublishedTools(ctx)
	if err != nil {
		return nil, err
	}
	items = filterToolItems(items, claims.Tools)
	items, err = s.filterSkillTools(ctx, claims, items)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&toolgatewayv1.ListToolsResponse{Tools: items}), nil
}

// InvokeTool 同时校验身份、能力、对象绑定和调用预算后执行已注册服务端工具。
func (s *ToolGatewayServer) InvokeTool(ctx context.Context, req *connect.Request[toolgatewayv1.InvokeToolRequest]) (*connect.Response[toolgatewayv1.InvokeToolResponse], error) {
	claims, err := s.authenticateTool(ctx, req)
	if err != nil {
		return nil, err
	}
	if !claimsAllows(claims.Tools, req.Msg.ToolName) {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("tool %s not allowed", req.Msg.ToolName))
	}
	if err := s.authorizeSkillTool(ctx, claims, req.Msg.GetToolName()); err != nil {
		return nil, err
	}
	if s.invokeRate != nil && !s.invokeRate.Allow(claims.TokenID, time.Now()) {
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("tool invoke qps exceeded"))
	}
	if err := s.authorizePublishedTool(ctx, req.Msg.ToolName); err != nil {
		return nil, err
	}
	toolName := req.Msg.ToolName
	if impl, err := s.resolveToolImpl(ctx, toolName); err != nil {
		return nil, err
	} else if impl != "" {
		toolName = impl
	}
	idem := strings.TrimSpace(req.Msg.GetIdempotencyKey())
	if h := strings.TrimSpace(req.Header().Get("Idempotency-Key")); h != "" {
		idem = h
	}
	digest := requestDigest(req.Msg.ToolName, req.Msg.ArgsJson, idem)
	budgetID := claims.BudgetID
	if budgetID == "" {
		budgetID = claims.TokenID
	}
	scope := "tool:" + budgetID
	if !s.demoTriage && idem != "" {
		hit, status, body, err := reserveIdempotency(ctx, s.pool, scope, idem, digest)
		if err != nil {
			return nil, err
		}
		if hit {
			if status == "error" {
				return nil, connect.NewError(connect.CodeInternal, errors.New(unwrapIdemBody(body)))
			}
			return connect.NewResponse(&toolgatewayv1.InvokeToolResponse{ResultJson: unwrapIdemBody(body)}), nil
		}
	}
	if err := s.authorizeToolArgs(ctx, claims, toolName, req.Msg.ArgsJson); err != nil {
		if !s.demoTriage && idem != "" {
			_ = abortIdempotency(ctx, s.pool, scope, idem)
		}
		return nil, err
	}
	reservationKey := idem
	if reservationKey == "" {
		reservationKey, err = newID("toolcall")
		if err != nil {
			return nil, err
		}
	} else {
		reservationKey += ":" + digest
	}
	budgetReservationID := ""
	invocationID := ""
	argumentsDigest := canonicalJSONDigest(req.Msg.ArgsJson)
	if !s.demoTriage {
		err = withTx(ctx, s.pool, func(tx pgx.Tx) error {
			var reserveErr error
			budgetReservationID, reserveErr = reserveRunBudget(ctx, tx, budgetID, "tool", reservationKey, runBudgetAmount{
				ToolCalls: 1, ToolResultBytes: kernel.RunToolResultBytesPerCall,
			})
			if reserveErr != nil {
				return reserveErr
			}
			invocationID, reserveErr = recordToolIntentTx(ctx, tx, claims, reservationKey, req.Msg.ToolName,
				argumentsDigest, budgetReservationID)
			return reserveErr
		})
		if err != nil {
			if idem != "" {
				_ = abortIdempotency(ctx, s.pool, scope, idem)
			}
			return nil, err
		}
	}
	remaining := claims.MaxCalls - 1
	if remaining < 0 {
		remaining = 0
	}
	if !s.demoTriage {
		left, err := consumeBudget(ctx, s.pool, budgetID, claims.Subject, claims.AuthorizedParty, claims.MaxCalls)
		if err != nil {
			if settleErr := s.settleToolInvocation(ctx, claims, invocationID, budgetReservationID, "denied", "", err.Error(), runBudgetAmount{}); settleErr != nil {
				return nil, settleErr
			}
			if idem != "" {
				_ = abortIdempotency(ctx, s.pool, scope, idem)
			}
			return nil, err
		}
		remaining = left
		if err := withTx(ctx, s.pool, func(tx pgx.Tx) error {
			return markToolEffectStartedTx(ctx, tx, claims, invocationID, remaining)
		}); err != nil {
			return nil, err
		}
	}
	var result any
	switch toolName {
	case "ticket.get":
		result, err = s.toolTicketGet(ctx, claims, req.Msg.ArgsJson)
	case "event.get":
		result, err = s.toolEventGet(ctx, claims, req.Msg.ArgsJson)
	case "event.list":
		result, err = s.toolEventList(ctx, claims, req.Msg.ArgsJson)
	case "cluster.get":
		result, err = s.toolClusterGet(ctx, claims, req.Msg.ArgsJson)
	case "triage.get":
		result, err = s.toolTriageGet(ctx, claims, req.Msg.ArgsJson)
	case "triage.complete":
		result, err = s.toolTriageComplete(ctx, claims, req.Msg.ArgsJson)
	case "release.list":
		result, err = s.toolReleaseList(ctx, claims, req.Msg.ArgsJson)
	case "session.reply":
		result, err = s.toolSessionReply(ctx, claims, req.Msg.ArgsJson)
	case "govern.propose":
		result, err = s.toolPropose(ctx, claims, req.Msg.ArgsJson)
	case "govern.gate":
		result, err = s.toolGate(ctx, claims, req.Msg.ArgsJson)
	case "govern.start_shadow":
		result, err = s.toolStartShadow(ctx, claims, req.Msg.ArgsJson)
	case "case.get":
		result, err = s.toolCaseGet(ctx, claims, req.Msg.ArgsJson)
	case "case.request_evidence":
		result, err = s.toolCaseRequestEvidence(ctx, claims, req.Msg.ArgsJson)
	case "run.create":
		result, err = s.toolCaseRunCreate(ctx, claims, req.Msg.ArgsJson)
	case "case.complete":
		result, err = s.toolCaseComplete(ctx, claims, req.Msg.ArgsJson)
	case "worker.capacity.request":
		result, err = s.toolWorkerCapacityRequest(ctx, claims, req.Msg.ArgsJson)
	default:
		if settleErr := s.settleToolInvocation(ctx, claims, invocationID, budgetReservationID, "denied", "",
			"tool not allowed", runBudgetAmount{ToolCalls: 1}); settleErr != nil {
			return nil, settleErr
		}
		if !s.demoTriage && idem != "" {
			_ = abortIdempotency(ctx, s.pool, scope, idem)
		}
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("tool %s not allowed", req.Msg.ToolName))
	}
	if err != nil {
		if settleErr := s.settleToolInvocationUnknown(ctx, claims, invocationID, budgetReservationID, err.Error()); settleErr != nil {
			return nil, settleErr
		}
		if !s.demoTriage && idem != "" {
			_ = abortIdempotency(ctx, s.pool, scope, idem)
		}
		return nil, err
	}
	raw, err := json.Marshal(result)
	if err != nil {
		if settleErr := s.settleToolInvocationUnknown(ctx, claims, invocationID, budgetReservationID, err.Error()); settleErr != nil {
			return nil, settleErr
		}
		return nil, err
	}
	if err := s.settleToolInvocation(ctx, claims, invocationID, budgetReservationID, "succeeded", string(raw), "", runBudgetAmount{
		ToolCalls: 1, ToolResultBytes: int64(len(raw)),
	}); err != nil {
		return nil, err
	}
	if !s.demoTriage && idem != "" {
		if err := storeIdempotency(ctx, s.pool, scope, idem, digest, "ok", string(raw)); err != nil {
			return nil, err
		}
	}
	return connect.NewResponse(&toolgatewayv1.InvokeToolResponse{ResultJson: string(raw), CallsRemaining: remaining}), nil
}

func canonicalJSONDigest(raw string) string {
	var value any
	if json.Unmarshal([]byte(raw), &value) != nil {
		return auditPayloadDigest(raw)
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return auditPayloadDigest(raw)
	}
	return auditPayloadDigest(string(canonical))
}

func recordToolIntentTx(ctx context.Context, tx pgx.Tx, claims kernel.Claims, requestKey, toolName,
	argumentsDigest, budgetReservationID string) (string, error) {
	coordinates, err := toolAuditCoordinates(ctx, tx, claims)
	if err != nil {
		return "", err
	}
	var invocationID, storedDigest, state string
	err = tx.QueryRow(ctx, `SELECT invocation_id, arguments_digest, state FROM tool_invocations
		WHERE budget_id=$1 AND request_key=$2 FOR UPDATE`, coordinates.BudgetID, requestKey).
		Scan(&invocationID, &storedDigest, &state)
	if err == nil {
		if storedDigest != argumentsDigest {
			return "", connect.NewError(connect.CodeFailedPrecondition, errors.New("tool invocation arguments digest changed"))
		}
		if state != "intent_recorded" {
			return "", connect.NewError(connect.CodeFailedPrecondition, errors.New("tool invocation already crossed effect boundary"))
		}
		if _, err := tx.Exec(ctx, `UPDATE tool_invocations SET lease_epoch=$2 WHERE invocation_id=$1`,
			invocationID, coordinates.LeaseEpoch); err != nil {
			return "", err
		}
		coordinates.PayloadDigest = argumentsDigest
		if err := appendToolAuditTx(ctx, tx, claims, "tool.intent_reclaimed", coordinates, map[string]any{
			"invocation_id": invocationID, "tool_name": toolName, "arguments_digest": argumentsDigest,
			"budget_reservation_id": budgetReservationID,
		}); err != nil {
			return "", err
		}
		return invocationID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	invocationID, err = newID("toolinv")
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO tool_invocations(
		invocation_id, budget_id, request_key, run_id, turn_id, lease_epoch, tool_name,
		arguments_digest, budget_reservation_id, state)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'intent_recorded')`, invocationID, coordinates.BudgetID,
		requestKey, coordinates.RunID, coordinates.TurnID, coordinates.LeaseEpoch, toolName,
		argumentsDigest, budgetReservationID); err != nil {
		return "", err
	}
	coordinates.PayloadDigest = argumentsDigest
	if err := appendToolAuditTx(ctx, tx, claims, "tool.intent_recorded", coordinates, map[string]any{
		"invocation_id": invocationID, "tool_name": toolName, "arguments_digest": argumentsDigest,
		"budget_reservation_id": budgetReservationID,
	}); err != nil {
		return "", err
	}
	return invocationID, nil
}

func markToolEffectStartedTx(ctx context.Context, tx pgx.Tx, claims kernel.Claims, invocationID string, callsRemaining int64) error {
	if invocationID == "" {
		return nil
	}
	tag, err := tx.Exec(ctx, `UPDATE tool_invocations SET state='effect_started', effect_started_at=now()
		WHERE invocation_id=$1 AND state='intent_recorded'`, invocationID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("tool invocation cannot cross effect boundary"))
	}
	coordinates, err := toolAuditCoordinates(ctx, tx, claims)
	if err != nil {
		return err
	}
	return appendToolAuditTx(ctx, tx, claims, "tool.effect_started", coordinates, map[string]any{
		"invocation_id": invocationID, "calls_remaining": callsRemaining,
	})
}

func (s *ToolGatewayServer) settleToolInvocation(ctx context.Context, claims kernel.Claims, invocationID,
	budgetReservationID, outcome, result, failure string, actual runBudgetAmount) error {
	return withTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := settleRunBudget(ctx, tx, budgetReservationID, "settled", actual); err != nil {
			return err
		}
		return settleToolInvocationRowTx(ctx, tx, claims, invocationID, outcome, result, failure, actual, "settled")
	})
}

func (s *ToolGatewayServer) settleToolInvocationUnknown(ctx context.Context, claims kernel.Claims, invocationID,
	budgetReservationID, failure string) error {
	return withTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := settleRunBudgetFull(ctx, tx, budgetReservationID, "outcome_unknown"); err != nil {
			return err
		}
		return settleToolInvocationRowTx(ctx, tx, claims, invocationID, "outcome_unknown", "", failure,
			runBudgetAmount{}, "outcome_unknown")
	})
}

func settleToolInvocationRowTx(ctx context.Context, tx pgx.Tx, claims kernel.Claims, invocationID, outcome,
	result, failure string, actual runBudgetAmount, state string) error {
	if invocationID == "" {
		return nil
	}
	resultDigest := auditPayloadDigest(result)
	errorDigest := auditPayloadDigest(failure)
	tag, err := tx.Exec(ctx, `UPDATE tool_invocations SET state=$2, outcome=$3, result_digest=$4,
		error_digest=$5, settled_at=now() WHERE invocation_id=$1 AND state IN ('intent_recorded','effect_started')`,
		invocationID, state, outcome, resultDigest, errorDigest)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("tool invocation is already settled"))
	}
	coordinates, err := toolAuditCoordinates(ctx, tx, claims)
	if err != nil {
		return err
	}
	coordinates.PayloadDigest = resultDigest
	if coordinates.PayloadDigest == "" {
		coordinates.PayloadDigest = errorDigest
	}
	action := "tool.settled"
	if state == "outcome_unknown" {
		action = "tool.outcome_unknown"
	}
	return appendToolAuditTx(ctx, tx, claims, action, coordinates, map[string]any{
		"invocation_id": invocationID, "outcome": outcome, "result_digest": resultDigest,
		"error_digest": errorDigest, "tool_calls": actual.ToolCalls, "tool_result_bytes": actual.ToolResultBytes,
	})
}

func toolAuditCoordinates(ctx context.Context, db dbTX, claims kernel.Claims) (auditCoordinates, error) {
	budgetID := claims.BudgetID
	if budgetID == "" {
		budgetID = claims.TokenID
	}
	coordinates := auditCoordinates{BudgetID: budgetID, LeaseEpoch: claims.LeaseEpoch}
	err := db.QueryRow(ctx, `SELECT run_id, turn_id FROM work_items WHERE budget_id=$1 ORDER BY created_at LIMIT 1`, budgetID).
		Scan(&coordinates.RunID, &coordinates.TurnID)
	if err == nil {
		return coordinates, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return auditCoordinates{}, err
	}
	var sourceKind, sourceRef string
	err = db.QueryRow(ctx, `SELECT t.turn_id, th.source_kind, th.source_ref FROM agent_turns t
		JOIN agent_threads th USING(thread_id) WHERE t.budget_id=$1 ORDER BY t.created_at DESC LIMIT 1`, budgetID).
		Scan(&coordinates.TurnID, &sourceKind, &sourceRef)
	if errors.Is(err, pgx.ErrNoRows) {
		return coordinates, nil
	}
	if err != nil {
		return auditCoordinates{}, err
	}
	if sourceKind == threadSourceRun {
		coordinates.RunID = sourceRef
	}
	return coordinates, nil
}

func appendToolAuditTx(ctx context.Context, tx pgx.Tx, claims kernel.Claims, action string, coordinates auditCoordinates,
	details map[string]any) error {
	actorID := claims.AuthorizedParty
	if actorID == "" {
		actorID = claims.Subject
	}
	objectType, objectID := "agent", claims.Subject
	if coordinates.TurnID != "" {
		objectType, objectID = "turn", coordinates.TurnID
	} else if coordinates.RunID != "" {
		objectType, objectID = "run", coordinates.RunID
	}
	return appendAgentAuditTx(ctx, tx, "agent", actorID, action, objectType, objectID, coordinates, details)
}

func (s *ToolGatewayServer) authenticateTool(ctx context.Context, req *connect.Request[toolgatewayv1.InvokeToolRequest]) (kernel.Claims, error) {
	return s.authenticateToolHeaders(ctx, req.Header())
}

func (s *ToolGatewayServer) authenticateToolHeaders(ctx context.Context, header http.Header) (kernel.Claims, error) {
	if s.demoTriage {
		return s.requireCapabilityLegacy(header)
	}
	tokens, err := ParseDualTokens(header)
	if err != nil {
		return kernel.Claims{}, err
	}
	claims, err := kernel.VerifyCapabilityToken(tokens.Capability, s.pub, time.Now())
	if err != nil {
		return kernel.Claims{}, connect.NewError(connect.CodeUnauthenticated, err)
	}
	if claims.Audience != "tools" {
		return kernel.Claims{}, connect.NewError(connect.CodePermissionDenied, errors.New("capability token audience must be tools"))
	}
	accessSub, agentErr := requireAgentToken(ctx, s.pool, tokens.Access)
	if agentErr == nil {
		if err := BindDualTokens(accessSub, claims); err != nil {
			return kernel.Claims{}, err
		}
		if err := requireLiveCapability(ctx, s.pool, claims, tokens.Capability); err != nil {
			return kernel.Claims{}, err
		}
		return claims, nil
	}
	principal, workerErr := requireWorkerToken(ctx, s.pool, tokens.Access)
	if workerErr != nil {
		return kernel.Claims{}, agentErr
	}
	if principal.Kind != workerv1.WorkerKind_WORKER_KIND_RUN_SUPERVISOR {
		return kernel.Claims{}, connect.NewError(connect.CodePermissionDenied, errors.New("analysis worker cannot invoke tools"))
	}
	if err := BindDualTokens(principal.ID, claims); err != nil {
		return kernel.Claims{}, err
	}
	if err := requireLiveCapability(ctx, s.pool, claims, tokens.Capability); err != nil {
		return kernel.Claims{}, err
	}
	budgetID := claims.BudgetID
	if budgetID == "" {
		budgetID = claims.TokenID
	}
	var live int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM work_items WHERE run_id=$1 AND worker_id=$2
		AND budget_id=$3 AND lease_epoch=$4 AND status='leased' AND lease_deadline>now()`, claims.Subject,
		principal.ID, budgetID, claims.LeaseEpoch).Scan(&live); err != nil {
		return kernel.Claims{}, err
	}
	if live != 1 {
		return kernel.Claims{}, connect.NewError(connect.CodePermissionDenied, errors.New("worker lease does not cover capability"))
	}
	return claims, nil
}

func (s *ToolGatewayServer) requireCapabilityLegacy(header http.Header) (kernel.Claims, error) {
	raw := bearerToken(header.Get("Authorization"))
	if raw == "" {
		return kernel.Claims{}, connect.NewError(connect.CodeUnauthenticated, errors.New("capability token required"))
	}
	claims, err := kernel.VerifyCapabilityToken(raw, s.pub, time.Now())
	if err != nil {
		return kernel.Claims{}, connect.NewError(connect.CodeUnauthenticated, err)
	}
	if claims.Audience != "tools" {
		return kernel.Claims{}, connect.NewError(connect.CodePermissionDenied, errors.New("capability token audience must be tools"))
	}
	return claims, nil
}

func requireAgentToken(ctx context.Context, pool *pgxpool.Pool, raw string) (string, error) {
	if err := requireAgentClientCert(ctx); err != nil {
		return "", err
	}
	var agentID, storedPubHash, storedCertHash, pub string
	var revoked *time.Time
	err := pool.QueryRow(ctx, `SELECT t.agent_id, t.pubkey_hash, t.client_cert_hash, a.public_key, a.revoked_at
		FROM agent_tokens t JOIN agents a ON a.agent_id=t.agent_id
		WHERE t.token_hash=$1 AND t.kind='access' AND t.expires_at > now()`, hashToken(raw)).
		Scan(&agentID, &storedPubHash, &storedCertHash, &pub, &revoked)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", connect.NewError(connect.CodeUnauthenticated, errors.New("invalid or expired agent token"))
	}
	if err != nil {
		return "", err
	}
	if revoked != nil {
		return "", connect.NewError(connect.CodeUnauthenticated, errors.New("agent is revoked"))
	}
	if strings.TrimSpace(pub) == "" {
		return "", connect.NewError(connect.CodeUnauthenticated, errors.New("agent public key missing"))
	}
	if storedPubHash != "" && storedPubHash != hashToken(pub) {
		return "", connect.NewError(connect.CodeUnauthenticated, errors.New("access token is not bound to registered public key"))
	}
	required, _ := ctx.Value(clientCertRequiredKey{}).(bool)
	if required && storedCertHash == "" {
		return "", connect.NewError(connect.CodeUnauthenticated, errors.New("access token is not bound to client certificate"))
	}
	if storedCertHash != "" && storedCertHash != clientCertHash(ctx) {
		return "", connect.NewError(connect.CodeUnauthenticated, errors.New("access token is not bound to client certificate"))
	}
	return agentID, nil
}

func requireLiveCapability(ctx context.Context, pool *pgxpool.Pool, claims kernel.Claims, rawToken string) error {
	budgetID := claims.BudgetID
	if budgetID == "" {
		budgetID = claims.TokenID
	}
	var revoked bool
	err := pool.QueryRow(ctx, `SELECT revoked FROM capability_token_instances WHERE jti=$1 AND budget_id=$2 AND lease_epoch=$3 AND expires_at > now()`,
		claims.TokenID, budgetID, claims.LeaseEpoch).Scan(&revoked)
	if err == nil && revoked {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("capability token revoked"))
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err == nil {
		var live int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM (
			SELECT 1 FROM agent_instructions
			WHERE budget_id=$1 AND lease_epoch=$2 AND status='leased' AND lease_expires_at > now()
			UNION ALL
			SELECT 1 FROM work_items
			WHERE budget_id=$1 AND lease_epoch=$2 AND status='leased' AND lease_deadline > now()
		) t`, budgetID, claims.LeaseEpoch).Scan(&live); err != nil {
			return err
		}
		if live == 0 {
			return connect.NewError(connect.CodeUnauthenticated, errors.New("capability lease is not live"))
		}
		return nil
	}

	// 迁移前签发的令牌没有 budget_id/lease_epoch，只允许按原始令牌精确匹配。
	err = pool.QueryRow(ctx, `SELECT revoked FROM capability_budget WHERE jti=$1`, claims.TokenID).Scan(&revoked)
	if err == nil && revoked {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("capability token revoked"))
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var recorded int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM (
		SELECT 1 FROM agent_instructions WHERE capability_token=$1
		UNION ALL
		SELECT 1 FROM work_items WHERE capability_token=$1
	) t`, rawToken).Scan(&recorded); err != nil {
		return err
	}
	if recorded == 0 {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("capability token is not registered"))
	}
	var live int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM (
		SELECT 1 FROM agent_instructions
		WHERE capability_token=$1 AND status='leased' AND lease_expires_at > now()
		UNION ALL
		SELECT 1 FROM work_items
		WHERE capability_token=$1 AND status='leased' AND lease_deadline > now()
	) t`, rawToken).Scan(&live); err != nil {
		return err
	}
	if live == 0 {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("capability lease is not live"))
	}
	return nil
}

func (s *ToolGatewayServer) authorizeToolArgs(ctx context.Context, claims kernel.Claims, toolName, argsJSON string) error {
	args, err := parseArgs(argsJSON)
	if err != nil {
		return err
	}
	switch toolName {
	case "ticket.get":
		eventID := argString(args, "event_id")
		ticketDigest := argString(args, "ticket_digest")
		if eventID == "" || ticketDigest == "" {
			return connect.NewError(connect.CodeInvalidArgument, errors.New("event_id and ticket_digest are required"))
		}
		var assetID, storedDigest string
		err := s.pool.QueryRow(ctx, `SELECT e.asset_id, t.ticket_digest FROM check_tickets t
			JOIN events e USING(event_id) WHERE t.event_id=$1 AND t.status='ready'`, eventID).Scan(&assetID, &storedDigest)
		if errors.Is(err, pgx.ErrNoRows) {
			return deniedObject()
		}
		if err != nil {
			return err
		}
		if storedDigest != ticketDigest {
			return deniedObject()
		}
		if !bindingsCoverAssets(claims.Bindings, []string{assetID}) {
			return deniedObject()
		}
	case "event.get":
		eventID := argString(args, "event_id")
		if eventID == "" {
			return connect.NewError(connect.CodeInvalidArgument, errors.New("event_id is required"))
		}
		var assetID string
		err := s.pool.QueryRow(ctx, `SELECT asset_id FROM events WHERE event_id=$1`, eventID).Scan(&assetID)
		if errors.Is(err, pgx.ErrNoRows) {
			return deniedObject()
		}
		if err != nil {
			return err
		}
		if !bindingsCoverAssets(claims.Bindings, []string{assetID}) {
			return deniedObject()
		}
	case "event.list":
		if filter := argString(args, "asset_id"); filter != "" && !bindingsCoverAssets(claims.Bindings, []string{filter}) {
			return deniedObject()
		}
	case "cluster.get":
		clusterID := argString(args, "cluster_id")
		if clusterID == "" {
			return connect.NewError(connect.CodeInvalidArgument, errors.New("cluster_id is required"))
		}
		var assetID string
		err := s.pool.QueryRow(ctx, `SELECT asset_id FROM triage_clusters WHERE cluster_id=$1`, clusterID).Scan(&assetID)
		if errors.Is(err, pgx.ErrNoRows) {
			return deniedObject()
		}
		if err != nil {
			return err
		}
		if !bindingsCoverAssets(claims.Bindings, []string{assetID}) {
			return deniedObject()
		}
	case "triage.get", "triage.complete":
		turnID := argString(args, "turn_id")
		if turnID == "" {
			return connect.NewError(connect.CodeInvalidArgument, errors.New("turn_id is required"))
		}
		if !bindingAllowsTurn(claims.Bindings, turnID) {
			return deniedObject()
		}
	case "govern.propose":
		msg, err := proposeArgs(args)
		if err != nil {
			return err
		}
		if msg.Scope == nil || !bindingsCoverAssets(claims.Bindings, msg.Scope.AssetIds) {
			return deniedObject()
		}
	case "govern.gate", "govern.start_shadow":
		releaseID := argString(args, "release_id")
		if releaseID == "" {
			return connect.NewError(connect.CodeInvalidArgument, errors.New("release_id is required"))
		}
		ids, err := releaseAssetIDs(ctx, s.pool, releaseID)
		if err != nil {
			return err
		}
		if !bindingsCoverAssets(claims.Bindings, ids) {
			return deniedObject()
		}
	case "case.get", "case.request_evidence", "run.create", "case.complete", "worker.capacity.request":
		caseID := argString(args, "case_id")
		if caseID == "" {
			return connect.NewError(connect.CodeInvalidArgument, errors.New("case_id is required"))
		}
		var assetID string
		if err := s.pool.QueryRow(ctx, `SELECT asset_id FROM investigation_cases WHERE case_id=$1`, caseID).Scan(&assetID); errors.Is(err, pgx.ErrNoRows) {
			return deniedObject()
		} else if err != nil {
			return err
		}
		if !bindingsCoverAssets(claims.Bindings, []string{assetID}) || !bindingsCoverCase(claims.Bindings, caseID) {
			return deniedObject()
		}
		if toolName == "worker.capacity.request" && argString(args, "worker_id") == "" {
			return connect.NewError(connect.CodeInvalidArgument, errors.New("worker_id is required"))
		}
	}
	return nil
}

func claimsAllows(tools []string, name string) bool {
	for _, t := range tools {
		if t == "*" || t == name {
			return true
		}
	}
	return false
}

func bindingsCoverAssets(bindings, assetIDs []string) bool {
	if len(assetIDs) == 0 {
		return false
	}
	have := map[string]bool{}
	for _, b := range bindings {
		if id, ok := assetIDFromBinding(b); ok {
			have[id] = true
		}
	}
	for _, id := range assetIDs {
		if !have[id] {
			return false
		}
	}
	return true
}

func bindingsCoverCase(bindings []string, caseID string) bool {
	want := "case:" + strings.TrimSpace(caseID)
	if want == "case:" {
		return false
	}
	for _, binding := range bindings {
		if binding == want {
			return true
		}
	}
	return false
}

func deniedObject() error {
	return connect.NewError(connect.CodePermissionDenied, errors.New("object outside bindings"))
}

func parseArgs(argsJSON string) (map[string]any, error) {
	if strings.TrimSpace(argsJSON) == "" {
		return map[string]any{}, nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &m); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("args_json: %w", err))
	}
	return m, nil
}

func argString(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	s, _ := v.(string)
	return s
}

func (s *ToolGatewayServer) toolEventGet(ctx context.Context, claims kernel.Claims, argsJSON string) (any, error) {
	args, err := parseArgs(argsJSON)
	if err != nil {
		return nil, err
	}
	eventID := argString(args, "event_id")
	if eventID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("event_id is required"))
	}
	var raw []byte
	err = s.pool.QueryRow(ctx, `SELECT payload FROM events WHERE event_id=$1`, eventID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, deniedObject()
	}
	if err != nil {
		return nil, err
	}
	var ev eventv1.Event
	if err := protojson.Unmarshal(raw, &ev); err != nil {
		return nil, err
	}
	if !bindingsCoverAssets(claims.Bindings, []string{ev.AssetId}) {
		return nil, deniedObject()
	}
	return eventView(&ev), nil
}

func (s *ToolGatewayServer) toolTicketGet(ctx context.Context, claims kernel.Claims, argsJSON string) (any, error) {
	args, err := parseArgs(argsJSON)
	if err != nil {
		return nil, err
	}
	eventID := argString(args, "event_id")
	ticketDigest := argString(args, "ticket_digest")
	if eventID == "" || ticketDigest == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("event_id and ticket_digest are required"))
	}
	var assetID, storedDigest string
	var raw []byte
	err = s.pool.QueryRow(ctx, `SELECT e.asset_id, t.ticket, t.ticket_digest FROM check_tickets t
		JOIN events e USING(event_id) WHERE t.event_id=$1 AND t.status='ready'`, eventID).Scan(&assetID, &raw, &storedDigest)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, deniedObject()
	}
	if err != nil {
		return nil, err
	}
	if storedDigest != ticketDigest {
		return nil, deniedObject()
	}
	if !bindingsCoverAssets(claims.Bindings, []string{assetID}) {
		return nil, deniedObject()
	}
	var ticket eventv1.CheckTicket
	if err := protojson.Unmarshal(raw, &ticket); err != nil {
		return nil, err
	}
	digest, err := kernel.CheckTicketDigest(&ticket)
	if err != nil || digest != storedDigest {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("frozen check ticket digest mismatch"))
	}
	projected, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(&ticket)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(projected, &out); err != nil {
		return nil, err
	}
	out["ticket_digest"] = storedDigest
	return out, nil
}

func (s *ToolGatewayServer) toolEventList(ctx context.Context, claims kernel.Claims, argsJSON string) (any, error) {
	args, err := parseArgs(argsJSON)
	if err != nil {
		return nil, err
	}
	assets := boundAssetIDs(claims.Bindings)
	if len(assets) == 0 {
		return []any{}, nil
	}
	filterAsset := argString(args, "asset_id")
	if filterAsset != "" {
		if !bindingsCoverAssets(claims.Bindings, []string{filterAsset}) {
			return nil, deniedObject()
		}
		assets = []string{filterAsset}
	}
	verdict := argString(args, "verdict")
	rows, err := s.pool.Query(ctx, `SELECT payload FROM events
		WHERE asset_id = ANY($1) AND ($2='' OR verdict=$2)
		ORDER BY occurred_at DESC LIMIT 50`, assets, verdict)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var ev eventv1.Event
		if err := protojson.Unmarshal(raw, &ev); err != nil {
			continue
		}
		out = append(out, eventView(&ev))
	}
	if out == nil {
		out = []map[string]any{}
	}
	return out, rows.Err()
}

func (s *ToolGatewayServer) toolClusterGet(ctx context.Context, claims kernel.Claims, argsJSON string) (any, error) {
	args, err := parseArgs(argsJSON)
	if err != nil {
		return nil, err
	}
	clusterID := argString(args, "cluster_id")
	if clusterID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("cluster_id is required"))
	}
	var assetID, route, method, reason, representative string
	var rawIDs []byte
	err = s.pool.QueryRow(ctx, `SELECT asset_id, route_template, method, reason, event_ids, representative
		FROM triage_clusters WHERE cluster_id=$1`, clusterID).
		Scan(&assetID, &route, &method, &reason, &rawIDs, &representative)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, deniedObject()
	}
	if err != nil {
		return nil, err
	}
	if !bindingsCoverAssets(claims.Bindings, []string{assetID}) {
		return nil, deniedObject()
	}
	var ids []string
	_ = json.Unmarshal(rawIDs, &ids)
	cluster, err := loadProposalCluster(ctx, s.pool, clusterID)
	if err != nil {
		return nil, err
	}
	keys, err := clusterDetectionKeyMaps(cluster.events)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"clusterId": clusterID, "assetId": assetID, "routeTemplate": route,
		"method": method, "reason": reason, "eventIds": ids, "representative": representative,
		"detectionKeys": keys,
	}, nil
}

func clusterDetectionKeyMaps(events []*eventv1.Event) ([]map[string]any, error) {
	seen := map[string]bool{}
	var out []map[string]any
	for _, event := range events {
		for _, detection := range event.GetDetections() {
			key := detection.GetKey()
			if key == nil || strings.TrimSpace(key.GetRuleId()) == "" {
				continue
			}
			raw, err := protojson.Marshal(key)
			if err != nil {
				return nil, err
			}
			if seen[string(raw)] {
				continue
			}
			seen[string(raw)] = true
			var projected map[string]any
			if err := json.Unmarshal(raw, &projected); err != nil {
				return nil, err
			}
			out = append(out, projected)
		}
	}
	if out == nil {
		out = []map[string]any{}
	}
	return out, nil
}

func (s *ToolGatewayServer) toolReleaseList(ctx context.Context, claims kernel.Claims, argsJSON string) (any, error) {
	assets := boundAssetIDs(claims.Bindings)
	if len(assets) == 0 {
		return []any{}, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT r.release_id, r.state, r.created_by
		FROM releases r JOIN release_assets ra ON ra.release_id=r.release_id
		WHERE ra.asset_id = ANY($1)
		ORDER BY r.release_id DESC LIMIT 20`, assets)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id, state, by string
		if err := rows.Scan(&id, &state, &by); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"releaseId": id, "state": state, "createdBy": by})
	}
	if out == nil {
		out = []map[string]any{}
	}
	return out, rows.Err()
}

func (s *ToolGatewayServer) toolSessionReply(ctx context.Context, claims kernel.Claims, argsJSON string) (any, error) {
	args, err := parseArgs(argsJSON)
	if err != nil {
		return nil, err
	}
	sessionID := argString(args, "session_id")
	content := argString(args, "content")
	if sessionID == "" || content == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("session_id and content are required"))
	}
	if !utf8.ValidString(content) || len(content) > sessionContentMaxBytes {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("content exceeds 8192 bytes"))
	}
	allowed := false
	for _, b := range claims.Bindings {
		if b == sessionID {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, deniedObject()
	}
	var seq int64
	err = s.pool.QueryRow(ctx, `INSERT INTO session_messages(session_id, sender, content)
		VALUES($1,$2,$3) RETURNING sequence`, sessionID, claims.Subject, RedactSecrets(content)).Scan(&seq)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return map[string]any{"sessionId": sessionID, "sequence": seq, "sender": claims.Subject}, nil
}

func (s *ToolGatewayServer) toolPropose(ctx context.Context, claims kernel.Claims, argsJSON string) (any, error) {
	args, err := parseArgs(argsJSON)
	if err != nil {
		return nil, err
	}
	msg, err := proposeArgs(args)
	if err != nil {
		return nil, err
	}
	if err := rejectProductionRegexProposal(s.demoTriage, msg); err != nil {
		return nil, err
	}
	if !bindingsCoverAssets(claims.Bindings, msg.Scope.AssetIds) {
		return nil, deniedObject()
	}
	resp, err := writePropose(ctx, s.pool, claims.Subject, msg, nil)
	if err != nil {
		return nil, err
	}
	if err := appendAudit(ctx, s.pool, "agent", claims.Subject, "release.propose", "release", resp.ReleaseId, map[string]any{"created_by": claims.Subject}); err != nil {
		return nil, auditFailedError(err)
	}
	return map[string]any{
		"releaseId": resp.ReleaseId,
		"state":     "DRAFT",
		"createdBy": claims.Subject,
	}, nil
}

func (s *ToolGatewayServer) toolGate(ctx context.Context, claims kernel.Claims, argsJSON string) (any, error) {
	args, err := parseArgs(argsJSON)
	if err != nil {
		return nil, err
	}
	releaseID := argString(args, "release_id")
	if releaseID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("release_id is required"))
	}
	ids, err := releaseAssetIDs(ctx, s.pool, releaseID)
	if err != nil {
		return nil, err
	}
	if !bindingsCoverAssets(claims.Bindings, ids) {
		return nil, deniedObject()
	}
	resp, err := writeGate(ctx, s.pool, s.key, "agent", claims.Subject, releaseID, s.artifactSigner, nil)
	if err != nil {
		return nil, err
	}
	state := "DRAFT"
	if resp.State.String() == "RELEASE_STATE_SIGNED" {
		state = "SIGNED"
	}
	return map[string]any{"releaseId": resp.ReleaseId, "state": state, "passed": resp.State.String() == "RELEASE_STATE_SIGNED"}, nil
}

func (s *ToolGatewayServer) toolStartShadow(ctx context.Context, claims kernel.Claims, argsJSON string) (any, error) {
	args, err := parseArgs(argsJSON)
	if err != nil {
		return nil, err
	}
	releaseID := argString(args, "release_id")
	if releaseID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("release_id is required"))
	}
	ids, err := releaseAssetIDs(ctx, s.pool, releaseID)
	if err != nil {
		return nil, err
	}
	if !bindingsCoverAssets(claims.Bindings, ids) {
		return nil, deniedObject()
	}
	shadow, err := writeStartShadow(ctx, s.pool, "agent", claims.Subject, releaseID, s.key, s.artifactSigner, nil)
	if err != nil {
		return nil, err
	}
	return map[string]any{"releaseId": shadow.ID, "state": "SHADOW"}, nil
}

func proposeArgs(args map[string]any) (*governv1.ProposeArtifactRequest, error) {
	if rawIntent, ok := args["intent"]; ok && rawIntent != nil {
		b, err := json.Marshal(rawIntent)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		var intent governv1.ProposalIntent
		if err := protojson.Unmarshal(b, &intent); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("intent: %w", err))
		}
		scope := &artifactv1.Scope{}
		if args["scope"] != nil {
			parsed, err := parseScope(args["scope"])
			if err != nil {
				return nil, err
			}
			scope = parsed
		}
		return &governv1.ProposeArtifactRequest{Intent: &intent, Scope: scope}, nil
	}
	kind := argString(args, "kind")
	if kind != "" && kind != "KIND_RULE" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("only KIND_RULE is supported in L1"))
	}
	schema := argString(args, "payload_schema")
	if schema == "" {
		schema = edgecore.RulePayloadSchema
	}
	payload, err := payloadBytes(args["payload"])
	if err != nil {
		return nil, err
	}
	scope, err := parseScope(args["scope"])
	if err != nil {
		return nil, err
	}
	ttl := 24 * time.Hour
	if s := argString(args, "ttl"); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("ttl: %w", err))
		}
		ttl = d
	}
	return &governv1.ProposeArtifactRequest{
		Kind:          artifactv1.Kind_KIND_RULE,
		Payload:       payload,
		PayloadSchema: schema,
		Scope:         scope,
		Ttl:           durationpb.New(ttl),
		Supersedes:    argString(args, "supersedes"),
	}, nil
}

func payloadBytes(v any) ([]byte, error) {
	switch t := v.(type) {
	case string:
		if strings.TrimSpace(t) == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("payload is required"))
		}
		return []byte(t), nil
	case []any, map[string]any:
		raw, err := json.Marshal(t)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		return raw, nil
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("payload is required"))
	}
}

func parseScope(v any) (*artifactv1.Scope, error) {
	raw, err := json.Marshal(v)
	if err != nil || v == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("scope is required"))
	}
	var s struct {
		AssetIDs      []string `json:"asset_ids"`
		AssetIdsCamel []string `json:"assetIds"`
		RouteSelector string   `json:"route_selector"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("scope: %w", err))
	}
	ids := s.AssetIDs
	if len(ids) == 0 {
		ids = s.AssetIdsCamel
	}
	if len(ids) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("scope.asset_ids is required"))
	}
	return &artifactv1.Scope{AssetIds: ids, RouteSelector: s.RouteSelector}, nil
}

func boundAssetIDs(bindings []string) []string {
	var out []string
	for _, b := range bindings {
		if id, ok := assetIDFromBinding(b); ok {
			out = append(out, id)
		}
	}
	return out
}

func eventView(ev *eventv1.Event) map[string]any {
	view := map[string]any{
		"eventId":      ev.Id,
		"assetId":      ev.AssetId,
		"verdict":      eventVerdictString(ev.Verdict),
		"kind":         eventKindString(ev.Kind),
		"clusterId":    ev.ClusterId,
		"triageReason": ev.TriageReason.String(),
	}
	if h := ev.GetHttp(); h != nil {
		view["http"] = map[string]any{"method": h.Method, "path": h.Path, "query": RedactQuery(h.QueryRedacted)}
	}
	if len(ev.Detections) > 0 {
		keys := make([]map[string]any, 0, len(ev.Detections))
		for _, d := range ev.Detections {
			if d == nil {
				continue
			}
			item := map[string]any{"ruleId": d.RuleId}
			if k := d.GetKey(); k != nil {
				item["ruleId"] = k.RuleId
				item["selector"] = k.TargetSelector
			}
			keys = append(keys, item)
		}
		view["detections"] = keys
	}
	return view
}

func demoToolItems(registry *agenttools.Registry) []*toolgatewayv1.ToolDescriptorItem {
	items := []*toolgatewayv1.ToolDescriptorItem{
		{Name: "ticket.get", Description: "读取冻结的字段级检查票据"},
		{Name: "event.get", Description: "读取一条流量事件"},
		{Name: "event.list", Description: "列出绑定资产上的事件"},
		{Name: "cluster.get", Description: "读取研判聚类"},
		{Name: "triage.get", Description: "读取钉死的研判回合投影"},
		{Name: "triage.complete", Description: "提交非可信研判结论"},
		{Name: "release.list", Description: "列出绑定资产上的发布"},
		{Name: "session.reply", Description: "以 Agent 身份回写会话"},
		{Name: "govern.propose", Description: "提出规则制品"},
		{Name: "govern.gate", Description: "对草稿跑回放门禁"},
		{Name: "govern.start_shadow", Description: "将已签名发布推进到 shadow"},
		{Name: "case.get", Description: "读取资产绑定案件冻结摘要"},
		{Name: "case.request_evidence", Description: "请求案件一次性证据批准"},
		{Name: "run.create", Description: "创建案件只读调查执行实例"},
		{Name: "case.complete", Description: "完成或解决案件"},
		{Name: "worker.capacity.request", Description: "请求中央调查执行池临时扩容"},
	}
	for _, item := range items {
		item.Version = "1.0.0"
		item.SchemaDigest = schemaDigest(nil)
		if impl, ok := registry.Lookup(item.Name); ok {
			item.Effect, item.Replay = impl.Effect, impl.Replay
		}
	}
	return items
}

func filterToolItems(items []*toolgatewayv1.ToolDescriptorItem, allowed []string) []*toolgatewayv1.ToolDescriptorItem {
	out := make([]*toolgatewayv1.ToolDescriptorItem, 0, len(items))
	for _, item := range items {
		if item != nil && claimsAllows(allowed, item.Name) {
			out = append(out, item)
		}
	}
	return out
}

func schemaDigest(schema []byte) string {
	sum := sha256.Sum256(schema)
	return fmt.Sprintf("sha256:%x", sum[:])
}

func (s *ToolGatewayServer) resolveToolImpl(ctx context.Context, name string) (string, error) {
	if s.demoTriage {
		return name, nil
	}
	desc, err := s.lookupPublishedTool(ctx, name)
	if err != nil {
		return "", err
	}
	if desc != nil && desc.Binding != nil && desc.Binding.GetPrimitive() != "" {
		return desc.Binding.GetPrimitive(), nil
	}
	return name, nil
}

func (s *ToolGatewayServer) authorizePublishedTool(ctx context.Context, name string) error {
	if s.demoTriage {
		return nil
	}
	desc, err := s.lookupPublishedTool(ctx, name)
	if err != nil {
		return err
	}
	if desc != nil {
		impl := name
		if desc.Binding != nil && desc.Binding.GetPrimitive() != "" {
			impl = desc.Binding.GetPrimitive()
		}
		if desc.Binding != nil && desc.Binding.GetProcedure() != "" {
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("procedure tool execution is not available"))
		}
		if _, ok := s.implementations.Lookup(impl); !ok {
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("unknown tool implementation"))
		}
		return nil
	}
	return connect.NewError(connect.CodePermissionDenied, errors.New("tool is not published"))
}
