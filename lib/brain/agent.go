package brain

import (
	"context"
	"crypto/ed25519"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "yufeng/proto/gen/agentv1"
	"yufeng/proto/gen/agentv1/agentv1connect"

	"yufeng/lib/kernel"
	"yufeng/lib/observability"
)

// dbTX 是连接池与事务共用的最小执行面，入队必须能进调用方事务。
type dbTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func withTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// errNoAgentKey 表示目标智能代理不存在或未登记公钥：事件仍应接受，只是不入队。
var errNoAgentKey = errors.New("agent public key missing")

// 指令/工作项租约与轮询的统一参数：三个轮询端点（指令、工作项、命令）
// 共用同一节拍，改这里即可全局调整。
// 租约与能力令牌共用 CapabilityTokenMaxTTL：令牌不得比租约活得更久。
const (
	pollTick      = 500 * time.Millisecond
	pollMaxWait   = 60 * time.Second
	instrLeaseTTL = kernel.CapabilityTokenMaxTTL
	instrTokenTTL = kernel.CapabilityTokenMaxTTL
	// instrMaxCalls 是指令级能力令牌的调用预算；工具网关按调用记账。
	instrMaxCalls        = 20
	caseReviewMaxRetries = 4
)

// AgentServer 是智能代理控制面服务，供所有智能代理进程统一使用。
type AgentServer struct {
	pool               *pgxpool.Pool
	bootstrapToken     string
	signingKey         ed25519.PrivateKey
	accessTTL          time.Duration
	refreshTTL         time.Duration
	polls              *pollGate
	pollRate           *windowLimiter
	allowUnboundShared bool
}

// NewAgentServer 构造智能代理控制面。
// 测试默认允许未绑定共享令牌；生产 NewMux 在非 DevInsecure 时关闭该回退。
func NewAgentServer(pool *pgxpool.Pool, bootstrapToken string, key ed25519.PrivateKey) *AgentServer {
	return &AgentServer{pool: pool, bootstrapToken: bootstrapToken, signingKey: key, accessTTL: kernel.AccessTokenTTL, refreshTTL: kernel.RefreshTokenTTL, polls: newPollGate(), pollRate: newWindowLimiter(kernel.AgentPollQPS, time.Second), allowUnboundShared: true}
}

// SetProductionBindingMode 禁止共享令牌在缺少服务端资产授予时回退到无绑定模式。
func (s *AgentServer) SetProductionBindingMode() {
	s.allowUnboundShared = false
}

// Handler 返回 Connect 服务端处理器。
func (s *AgentServer) Handler() (string, http.Handler) {
	return agentv1connect.NewAgentControlServiceHandler(s, handlerOptions()...)
}

// RegisterAgent 校验工作负载身份并建立智能代理会话，返回短期访问令牌与可轮换刷新令牌。
func (s *AgentServer) RegisterAgent(ctx context.Context, req *connect.Request[agentv1.RegisterAgentRequest]) (*connect.Response[agentv1.RegisterAgentResponse], error) {
	if err := requireAgentClientCert(ctx); err != nil {
		return nil, err
	}
	agentID := strings.TrimSpace(req.Msg.GetAgentId())
	boot := req.Msg.GetBootstrapToken()
	publicKey := strings.TrimSpace(req.Msg.GetAgentPublicKey())
	if agentID == "" || boot == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid bootstrap token or agent id"))
	}
	if publicKey == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("agent public key is required"))
	}
	refreshRaw, refreshHash, err := newSessionToken()
	if err != nil {
		return nil, err
	}
	accessRaw, accessHash, err := newSessionToken()
	if err != nil {
		return nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	err = consumeAgentBootstrap(ctx, tx, agentID, boot, s.bootstrapToken, s.allowUnboundShared)
	consumed := errors.Is(err, errBootstrapConsumed)
	if err != nil && !consumed {
		return nil, err
	}
	var tag pgconn.CommandTag
	if consumed {
		// 引导令牌已消耗但占位行仍在：补完崩溃窗口内未完成的登记，不得回退成第二次注册。
		tag, err = claimBootstrapPlaceholder(ctx, tx, agentID, refreshHash, publicKey, clientCertHash(ctx), s.refreshTTL)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() != 1 {
			return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("bootstrap token already used"))
		}
	} else {
		// 先认领 EnsureBootstrapJarvis 占位行。INSERT 撞唯一约束会把当前事务打成 aborted，
		// 不能在同一事务里再 UPDATE。
		tag, err = claimBootstrapPlaceholder(ctx, tx, agentID, refreshHash, publicKey, clientCertHash(ctx), s.refreshTTL)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() != 1 {
			tag, err = tx.Exec(ctx, `INSERT INTO agents(agent_id, refresh_token_hash, role, public_key, refresh_expires_at, client_cert_hash)
	VALUES($1,$2,'orchestrator',$3,$4,$5)`,
				agentID, refreshHash, publicKey, time.Now().Add(s.refreshTTL), clientCertHash(ctx))
			if isUniqueViolation(err) {
				return nil, connect.NewError(connect.CodePermissionDenied, errors.New("agent already registered"))
			}
			if err != nil {
				return nil, err
			}
			if tag.RowsAffected() != 1 {
				return nil, connect.NewError(connect.CodePermissionDenied, errors.New("agent already registered"))
			}
		}
	}
	if err := s.storeAccessToken(ctx, tx, accessHash, agentID, s.accessTTL, hashToken(publicKey)); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentv1.RegisterAgentResponse{
		AgentId: agentID, RefreshToken: refreshRaw, AccessToken: accessRaw, ExpiresIn: int64(s.accessTTL.Seconds()),
	}), nil
}

// SeedAgentBootstrap 预生成绑定唯一 agent_id 的一次性引导令牌。
func SeedAgentBootstrap(ctx context.Context, pool *pgxpool.Pool, agentID, token string) error {
	return seedAgentBootstrap(ctx, pool, agentID, token)
}

var errBootstrapConsumed = errors.New("bootstrap token already used")

func claimBootstrapPlaceholder(ctx context.Context, db dbTX, agentID, refreshHash, publicKey, certHash string, refreshTTL time.Duration) (pgconn.CommandTag, error) {
	return db.Exec(ctx, `UPDATE agents SET refresh_token_hash=$1, role='orchestrator', public_key=$2, refresh_expires_at=$3,
		client_cert_hash=$4, updated_at=now() WHERE agent_id=$5 AND refresh_token_hash=$6 AND public_key=$6`,
		refreshHash, publicKey, time.Now().Add(refreshTTL), certHash, agentID, bootstrapJarvisPlaceholder)
}

func seedAgentBootstrap(ctx context.Context, db dbTX, agentID, token string) error {
	if strings.TrimSpace(agentID) == "" || token == "" {
		return errors.New("agent bootstrap agent_id and token are required")
	}
	_, err := db.Exec(ctx, `INSERT INTO agent_bootstrap(token_hash, agent_id, expires_at)
		VALUES($1,$2,now()+interval '30 days') ON CONFLICT (token_hash) DO NOTHING`, hashToken(token), agentID)
	return err
}

func consumeAgentBootstrap(ctx context.Context, db dbTX, agentID, token, shared string, allowUnbound bool) error {
	sum := hashToken(token)
	var bound string
	var used *time.Time
	err := db.QueryRow(ctx, `SELECT agent_id, used_at FROM agent_bootstrap WHERE token_hash=$1`, sum).Scan(&bound, &used)
	if errors.Is(err, pgx.ErrNoRows) {
		if !allowUnbound || shared == "" || subtle.ConstantTimeCompare([]byte(token), []byte(shared)) != 1 {
			return connect.NewError(connect.CodeUnauthenticated, errors.New("invalid bootstrap token or agent id"))
		}
		if err := seedAgentBootstrap(ctx, db, agentID, token); err != nil {
			return err
		}
		err = db.QueryRow(ctx, `SELECT agent_id, used_at FROM agent_bootstrap WHERE token_hash=$1`, sum).Scan(&bound, &used)
	}
	if err != nil {
		return err
	}
	if bound != agentID {
		return connect.NewError(connect.CodePermissionDenied, errors.New("bootstrap token is bound to a different agent"))
	}
	if used != nil {
		return errBootstrapConsumed
	}
	tag, err := db.Exec(ctx, `UPDATE agent_bootstrap SET used_at=now() WHERE token_hash=$1 AND used_at IS NULL`, sum)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("bootstrap token already used"))
	}
	return nil
}

// RefreshAccessToken 消耗当前刷新令牌并原子轮换智能代理会话凭据。
func (s *AgentServer) RefreshAccessToken(ctx context.Context, req *connect.Request[agentv1.RefreshAccessTokenRequest]) (*connect.Response[agentv1.RefreshAccessTokenResponse], error) {
	if err := requireAgentClientCert(ctx); err != nil {
		return nil, err
	}
	oldHash := hashToken(req.Msg.GetRefreshToken())
	if strings.TrimSpace(req.Msg.GetRefreshToken()) == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid refresh token"))
	}
	newRefreshRaw, newRefreshHash, err := newSessionToken()
	if err != nil {
		return nil, err
	}
	accessRaw, accessHash, err := newSessionToken()
	if err != nil {
		return nil, err
	}
	var agentID, pub string
	err = withTx(ctx, s.pool, func(tx pgx.Tx) error {
		qerr := tx.QueryRow(ctx, `UPDATE agents SET refresh_token_hash=$1, client_cert_hash=CASE WHEN client_cert_hash='' THEN $3 ELSE client_cert_hash END, updated_at=now()
			WHERE refresh_token_hash=$2 AND revoked_at IS NULL AND (client_cert_hash='' OR client_cert_hash=$3)
			  AND refresh_expires_at IS NOT NULL AND refresh_expires_at > now()
			RETURNING agent_id, public_key`, newRefreshHash, oldHash, clientCertHash(ctx)).Scan(&agentID, &pub)
		if errors.Is(qerr, pgx.ErrNoRows) {
			return connect.NewError(connect.CodeUnauthenticated, errors.New("invalid refresh token"))
		}
		if qerr != nil {
			return qerr
		}
		if strings.TrimSpace(pub) == "" {
			return connect.NewError(connect.CodeUnauthenticated, errors.New("agent public key missing"))
		}
		return s.storeAccessToken(ctx, tx, accessHash, agentID, s.accessTTL, hashToken(pub))
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentv1.RefreshAccessTokenResponse{
		AccessToken: accessRaw, RefreshToken: newRefreshRaw, ExpiresIn: int64(s.accessTTL.Seconds()),
	}), nil
}

// PollInstructions 为已认证智能代理长轮询并租赁可执行指令。
func (s *AgentServer) PollInstructions(ctx context.Context, req *connect.Request[agentv1.PollInstructionsRequest]) (*connect.Response[agentv1.PollInstructionsResponse], error) {
	agentID, err := requireAgent(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	if req.Msg.AgentId != "" && req.Msg.AgentId != agentID {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("access token subject must match agent_id"))
	}
	if s.pollRate != nil && !s.pollRate.Allow(agentID, time.Now()) {
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("agent poll qps exceeded"))
	}
	if s.polls != nil {
		if err := s.polls.acquire(agentID); err != nil {
			return nil, err
		}
		defer s.polls.release(agentID)
	}
	wait, err := kernel.ResolveLongPoll(req.Msg.LongPollSeconds, kernel.AgentLongPollDefault, kernel.AgentLongPollMax)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := touchAgentSeen(ctx, s.pool, agentID); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for {
		inst, err := s.leaseInstruction(ctx, agentID)
		if err != nil {
			return nil, err
		}
		if inst != nil {
			return connect.NewResponse(&agentv1.PollInstructionsResponse{Instructions: []*agentv1.AgentInstruction{inst}, LeaseId: inst.LeaseId}), nil
		}
		if time.Now().After(deadline) {
			return connect.NewResponse(&agentv1.PollInstructionsResponse{}), nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pollTick):
		}
	}
}

// ExtendInstructionLease 延长当前指令租约并轮换同一预算账户与所有权代次的能力令牌。
// 正常续租保持 lease_id / lease_epoch / budget_id；旧令牌可在原 exp 前完成同代次在途请求，不得因续租制造假失败。
func (s *AgentServer) ExtendInstructionLease(ctx context.Context, req *connect.Request[agentv1.ExtendInstructionLeaseRequest]) (*connect.Response[agentv1.ExtendInstructionLeaseResponse], error) {
	tokens, err := ParseDualTokens(req.Header())
	if err != nil {
		return nil, err
	}
	agentID, err := requireAgentToken(ctx, s.pool, tokens.Access)
	if err != nil {
		return nil, err
	}
	claims, err := kernel.VerifyCapabilityToken(tokens.Capability, s.signingKey.Public().(ed25519.PublicKey), time.Now())
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	if claims.Audience != "tools" || claims.Subject != agentID {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("capability token does not belong to agent instruction"))
	}
	if err := BindDualTokens(agentID, claims); err != nil {
		return nil, err
	}
	if claims.LeaseEpoch != req.Msg.LeaseEpoch {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("capability lease epoch does not match instruction"))
	}
	if err := requireLiveCapability(ctx, s.pool, claims, tokens.Capability); err != nil {
		return nil, err
	}
	var kind, payload, budgetID, oldToken string
	var leaseEpoch int64
	err = s.pool.QueryRow(ctx, `SELECT kind, payload_ref, budget_id, lease_epoch, capability_token
		FROM agent_instructions WHERE instruction_id=$1 AND agent_id=$2 AND lease_id=$3 AND lease_epoch=$4
		AND status='leased' AND lease_expires_at > now()`, req.Msg.InstructionId, agentID, req.Msg.LeaseId, req.Msg.LeaseEpoch).
		Scan(&kind, &payload, &budgetID, &leaseEpoch, &oldToken)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("instruction lease is expired or not held"))
	}
	if err != nil {
		return nil, err
	}
	if claims.BudgetID != budgetID {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("capability budget does not belong to instruction"))
	}
	until := time.Now().Add(instrLeaseTTL)
	var token string
	err = withTx(ctx, s.pool, func(tx pgx.Tx) error {
		fresh, err := s.signInstructionLeaseCapability(ctx, tx, agentID, kind, payload, budgetID, req.Msg.LeaseId, leaseEpoch, until)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE agent_instructions SET lease_expires_at=$1, capability_token=$2
			WHERE instruction_id=$3 AND agent_id=$4 AND lease_id=$5 AND lease_epoch=$6 AND capability_token=$7
			AND status='leased' AND lease_expires_at > now()`, until, fresh, req.Msg.InstructionId, agentID,
			req.Msg.LeaseId, leaseEpoch, oldToken)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("instruction lease is expired or not held"))
		}
		token = fresh
		return nil
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentv1.ExtendInstructionLeaseResponse{
		LeaseDeadline: timestamppb.New(until), CapabilityToken: token, LeaseId: req.Msg.LeaseId,
		BudgetId: budgetID, LeaseEpoch: leaseEpoch,
	}), nil
}

// AckInstruction 仅接受与当前租约标识和纪元匹配的指令终态回执。
func (s *AgentServer) AckInstruction(ctx context.Context, req *connect.Request[agentv1.AckInstructionRequest]) (*connect.Response[agentv1.AckInstructionResponse], error) {
	agentID, err := requireAgent(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	status := strings.TrimSpace(req.Msg.Status)
	if status == "" {
		status = "acked"
	}
	switch status {
	case "acked", "completed", "failed", "cancelled":
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("instruction terminal status is invalid"))
	}
	var tag pgconn.CommandTag
	err = withTx(ctx, s.pool, func(tx pgx.Tx) error {
		var turnID, kind, payloadRef, capabilityToken string
		var retryCount int
		if err := tx.QueryRow(ctx, `SELECT turn_id, kind, payload_ref, capability_token, retry_count
			FROM agent_instructions WHERE instruction_id=$1 AND lease_id=$2 AND agent_id=$3 AND lease_epoch=$4
			AND status='leased' AND (lease_expires_at IS NULL OR lease_expires_at > now()) FOR UPDATE`,
			req.Msg.InstructionId, req.Msg.LeaseId, agentID, req.Msg.LeaseEpoch).
			Scan(&turnID, &kind, &payloadRef, &capabilityToken, &retryCount); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return connect.NewError(connect.CodeFailedPrecondition, errors.New("instruction lease is expired or not held"))
			}
			return err
		}
		if kind == instructionCaseReview && status == "failed" && retryCount < caseReviewMaxRetries {
			if err := revokeStoredCapability(ctx, tx, capabilityToken, s.signingKey.Public().(ed25519.PublicKey)); err != nil {
				return err
			}
			backoff := time.Second << retryCount
			tag, err = tx.Exec(ctx, `UPDATE agent_instructions SET status='pending', retry_count=retry_count+1,
				next_attempt_at=$2, ack_error=$3, acked_at=NULL, lease_id='', lease_expires_at=NULL, capability_token=''
				WHERE instruction_id=$1`, req.Msg.InstructionId, time.Now().Add(backoff), auditPayloadDigest(req.Msg.GetError()))
			return err
		}
		if err := revokeStoredCapability(ctx, tx, capabilityToken, s.signingKey.Public().(ed25519.PublicKey)); err != nil {
			return err
		}
		ackError := req.Msg.GetError()
		if kind == instructionCaseReview && status == "failed" {
			// 调查执行错误可能包含不可信流量片段；最终失败与中间重试一样只保存摘要。
			ackError = auditPayloadDigest(ackError)
		}
		tag, err = tx.Exec(ctx, `UPDATE agent_instructions SET status=$3, ack_error=$4, acked_at=now()
			WHERE instruction_id=$1 AND lease_id=$2 AND agent_id=$5 AND lease_epoch=$6 AND status='leased'
			  AND (lease_expires_at IS NULL OR lease_expires_at > now())`,
			req.Msg.InstructionId, req.Msg.LeaseId, status, ackError, agentID, req.Msg.LeaseEpoch)
		if err != nil || tag.RowsAffected() != 1 {
			return err
		}
		if kind == instructionCaseReview && status == "failed" {
			if _, err := tx.Exec(ctx, `UPDATE investigation_cases SET state='failed', updated_at=now()
				WHERE case_id=$1 AND state NOT IN ('finding_ready','shadow_observing','resolved','failed','evidence_expired')`, payloadRef); err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `INSERT INTO case_activities(case_id, kind, ref_id, summary)
				SELECT $1,'run_progress',$2,'案件编排达到重试上限并已失败关闭'
				WHERE EXISTS (SELECT 1 FROM investigation_cases WHERE case_id=$1)
				AND NOT EXISTS (SELECT 1 FROM case_activities WHERE case_id=$1 AND ref_id=$2)`,
				payloadRef, "case-review-failed:"+req.Msg.InstructionId)
			return err
		}
		if turnID == "" {
			return nil
		}
		turnState := ""
		switch status {
		case "acked", "completed":
			turnState = "completed"
		case "failed":
			turnState = "failed"
		case "cancelled":
			turnState = "cancelled"
		}
		if turnState != "" {
			_, err = tx.Exec(ctx, `UPDATE agent_turns SET state=$2, completed_at=now(), updated_at=now()
				WHERE turn_id=$1 AND state NOT IN ('completed','failed','cancelled','outcome_unknown')`, turnID, turnState)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() != 1 {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("instruction lease is expired or not held"))
	}
	return connect.NewResponse(&agentv1.AckInstructionResponse{}), nil
}

// Heartbeat 更新智能代理在线状态并返回服务端建议的下一次心跳时间。
func (s *AgentServer) Heartbeat(ctx context.Context, req *connect.Request[agentv1.HeartbeatRequest]) (*connect.Response[agentv1.HeartbeatResponse], error) {
	agentID, err := requireAgent(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	if err := touchAgentSeen(ctx, s.pool, agentID); err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentv1.HeartbeatResponse{ServerTime: timestamppb.Now()}), nil
}

func touchAgentSeen(ctx context.Context, pool *pgxpool.Pool, agentID string) error {
	_, err := pool.Exec(ctx, `UPDATE agents SET last_heartbeat_at=now() WHERE agent_id=$1`, agentID)
	return err
}

func (s *AgentServer) storeAccessToken(ctx context.Context, db dbTX, hash, agentID string, ttl time.Duration, pubkeyHash string) error {
	_, err := db.Exec(ctx, `INSERT INTO agent_tokens(token_hash, agent_id, kind, expires_at, pubkey_hash, client_cert_hash)
		VALUES($1,$2,'access',$3,$4,$5)`, hash, agentID, time.Now().Add(ttl), pubkeyHash, clientCertHash(ctx))
	return err
}

func (s *AgentServer) leaseInstruction(ctx context.Context, agentID string) (*agentv1.AgentInstruction, error) {
	leaseID, err := newID("lease")
	if err != nil {
		return nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var id, kind, payload, turnID, capToken, prevStatus, budgetID string
	var leaseEpoch int64
	var createdAt, deadline time.Time
	err = tx.QueryRow(ctx, `SELECT instruction_id, kind, payload_ref, turn_id, capability_token, created_at, deadline, status, budget_id, lease_epoch
	FROM agent_instructions WHERE agent_id=$1 AND (
		(status='pending' AND next_attempt_at<=now()) OR (status='leased' AND lease_expires_at IS NOT NULL AND lease_expires_at < now())
	) ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED`, agentID).
		Scan(&id, &kind, &payload, &turnID, &capToken, &createdAt, &deadline, &prevStatus, &budgetID, &leaseEpoch)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if prevStatus == "leased" {
		observability.Default().Add(observability.MetricLeaseExpired, 1)
	}
	if err := revokeStoredCapability(ctx, tx, capToken, s.signingKey.Public().(ed25519.PublicKey)); err != nil {
		return nil, err
	}
	if budgetID == "" {
		budgetID = "instruction:" + id
	}
	leaseEpoch++
	leaseUntil := time.Now().Add(instrLeaseTTL)
	fresh, err := s.signInstructionLeaseCapability(ctx, tx, agentID, kind, payload, budgetID, leaseID, leaseEpoch, leaseUntil)
	if err != nil {
		return nil, err
	}
	capToken = fresh
	if _, err := tx.Exec(ctx, `UPDATE agent_instructions SET status='leased', lease_id=$1, lease_expires_at=$2,
	capability_token=$3, budget_id=$4, lease_epoch=$5 WHERE instruction_id=$6`,
		leaseID, leaseUntil, capToken, budgetID, leaseEpoch, id); err != nil {
		return nil, err
	}
	threadID, sourceRef, resumeGenerationID, stepID := "", "", "", ""
	var checkpoint []byte
	var expectedItemSequence int64
	if turnID != "" {
		if err := tx.QueryRow(ctx, `SELECT th.thread_id, th.source_ref, t.next_item_sequence, t.checkpoint
			FROM agent_turns t JOIN agent_threads th ON th.thread_id=t.thread_id WHERE t.turn_id=$1`, turnID).
			Scan(&threadID, &sourceRef, &expectedItemSequence, &checkpoint); err != nil {
			return nil, err
		}
		_ = tx.QueryRow(ctx, `SELECT generation_id FROM model_generations
			WHERE turn_id=$1 AND state IN ('pending','running') ORDER BY created_at DESC LIMIT 1`, turnID).
			Scan(&resumeGenerationID)
		if err := tx.QueryRow(ctx, `SELECT step_id FROM agent_steps
			WHERE turn_id=$1 ORDER BY step_sequence DESC LIMIT 1`, turnID).Scan(&stepID); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE agent_turns SET state='running', updated_at=now()
			WHERE turn_id=$1 AND state IN ('pending','running')`, turnID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &agentv1.AgentInstruction{
		InstructionId: id, AgentId: agentID, Kind: kind, PayloadRef: payload,
		CapabilityToken: capToken, CreatedAt: timestamppb.New(createdAt), Deadline: timestamppb.New(deadline), LeaseId: leaseID,
		BudgetId: budgetID, LeaseEpoch: leaseEpoch, LeaseDeadline: timestamppb.New(leaseUntil), TurnId: turnID,
		ThreadId: threadID, SourceRef: sourceRef, ExpectedItemSequence: expectedItemSequence,
		ResumeGenerationId: resumeGenerationID, StepId: stepID, CheckpointJson: string(checkpoint),
	}, nil
}

func (s *AgentServer) signInstructionLeaseCapability(ctx context.Context, db dbTX, agentID, kind, payloadRef, budgetID, leaseID string, leaseEpoch int64, until time.Time) (string, error) {
	tools, bindings, err := s.instructionCapabilityScope(ctx, db, kind, payloadRef)
	if err != nil {
		return "", err
	}
	jti, err := newID("jti")
	if err != nil {
		return "", err
	}
	now := time.Now()
	token, err := kernel.SignCapabilityToken(kernel.Claims{
		Subject: agentID, AuthorizedParty: agentID, Role: "orchestrator", Audience: "tools", TokenID: jti,
		BudgetID: budgetID, LeaseEpoch: leaseEpoch,
		ExpiresAt: until.Unix(), IssuedAt: now.Unix(), NotBefore: now.Add(-time.Second).Unix(),
		Tools: tools, Bindings: bindings, MaxCalls: instrMaxCalls,
	}, s.signingKey)
	if err != nil {
		return "", err
	}
	if err := seedCapabilityBudget(ctx, db, budgetID, agentID, agentID, instrMaxCalls, until); err != nil {
		return "", err
	}
	if err := registerCapabilityToken(ctx, db, jti, budgetID, leaseID, leaseEpoch, until); err != nil {
		return "", err
	}
	return token, nil
}

func (s *AgentServer) signInstructionCapability(ctx context.Context, db dbTX, agentID, kind, payloadRef string) (string, error) {
	tools, bindings, err := s.instructionCapabilityScope(ctx, db, kind, payloadRef)
	if err != nil {
		return "", err
	}
	now := time.Now()
	id, err := newID("jti")
	if err != nil {
		return "", err
	}
	token, err := kernel.SignCapabilityToken(kernel.Claims{
		Subject: agentID, AuthorizedParty: agentID, Role: "orchestrator", Audience: "tools", TokenID: id,
		ExpiresAt: now.Add(instrTokenTTL).Unix(), IssuedAt: now.Unix(), NotBefore: now.Add(-time.Second).Unix(),
		Tools: tools, Bindings: bindings, MaxCalls: instrMaxCalls,
	}, s.signingKey)
	if err != nil {
		return "", err
	}
	if err := seedCapabilityBudget(ctx, db, id, agentID, agentID, instrMaxCalls, now.Add(instrTokenTTL)); err != nil {
		return "", err
	}
	return token, nil
}

func (s *AgentServer) instructionCapabilityScope(ctx context.Context, db dbTX, kind, payloadRef string) ([]string, []string, error) {
	tools := sessionInstructionTools
	bindings := []string{payloadRef}
	if kind == instructionCaseReview {
		profile, assetID, err := frozenCaseInstructionScope(ctx, db, payloadRef)
		if err != nil {
			return nil, nil, err
		}
		return caseInstructionTools(profile), []string{assetBinding(assetID), "case:" + payloadRef}, nil
	}
	hasTurn := false
	if kind == instructionSession || kind == instructionTriage {
		var err error
		hasTurn, err = cognitiveTurnExists(ctx, db, payloadRef)
		if err != nil {
			return nil, nil, err
		}
	}
	if kind == instructionSession && hasTurn {
		sourceRef, err := sourceRefForTurn(ctx, db, payloadRef, threadSourceSession)
		if err != nil {
			return nil, nil, err
		}
		bindings = []string{sourceRef}
	}
	if kind == instructionTriage {
		tools = demoTriageInstructionTools
		if hasTurn {
			tools = triageInstructionTools
			bindings = []string{turnBinding(payloadRef)}
		}
		if assetID := triageBindingAsset(ctx, db, payloadRef); assetID != "" {
			bindings = append(bindings, assetBinding(assetID))
		}
	}
	return tools, bindings, nil
}

// EnqueueInstruction 给指定智能代理写入一条指令，并签发最小能力令牌。
func (s *AgentServer) EnqueueInstruction(ctx context.Context, agentID, kind, payloadRef string, tools, bindings []string) error {
	return withTx(ctx, s.pool, func(tx pgx.Tx) error {
		return s.enqueueInstruction(ctx, tx, agentID, kind, payloadRef, tools, bindings)
	})
}

func (s *AgentServer) enqueueInstruction(ctx context.Context, db dbTX, agentID, kind, payloadRef string, tools, bindings []string) error {
	return s.enqueueInstructionDeduped(ctx, db, agentID, kind, payloadRef, tools, bindings, "")
}

func (s *AgentServer) enqueueInstructionDeduped(ctx context.Context, db dbTX, agentID, kind, payloadRef string, tools, bindings []string, dedupeKey string) error {
	var pub string
	err := db.QueryRow(ctx, `SELECT public_key FROM agents WHERE agent_id=$1`, agentID).Scan(&pub)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && strings.TrimSpace(pub) == "") {
		return errNoAgentKey
	}
	if err != nil {
		return err
	}
	id, err := newID("ins")
	if err != nil {
		return err
	}
	now := time.Now()
	// 入队令牌仅兼容旧客户端；正式能力令牌在领取时绑定预算账户和租约代次。
	claims := kernel.Claims{
		Subject: agentID, AuthorizedParty: agentID, Role: "orchestrator", Audience: "tools", TokenID: id,
		ExpiresAt: now.Add(instrTokenTTL).Unix(), IssuedAt: now.Unix(), NotBefore: now.Add(-time.Second).Unix(),
		Tools: tools, Bindings: bindings, MaxCalls: instrMaxCalls,
	}
	token, err := kernel.SignCapabilityToken(claims, s.signingKey)
	if err != nil {
		return err
	}
	turnID := ""
	if kind == instructionTriage || kind == instructionSession {
		exists, err := cognitiveTurnExists(ctx, db, payloadRef)
		if err != nil {
			return err
		}
		if exists {
			turnID = payloadRef
		}
	}
	var insertedID string
	err = db.QueryRow(ctx, `INSERT INTO agent_instructions(instruction_id, agent_id, kind, payload_ref, turn_id, capability_token, dedupe_key)
		VALUES($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT DO NOTHING RETURNING instruction_id`, id, agentID, kind, payloadRef, turnID, token, dedupeKey).Scan(&insertedID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return seedCapabilityBudget(ctx, db, id, agentID, agentID, instrMaxCalls, now.Add(instrTokenTTL))
}

// triageBindingAsset 按聚类解析研判令牌资产；找不到聚类再按 event_id。
// 禁止回落到「最近一条事件」。
func triageBindingAsset(ctx context.Context, db dbTX, payloadRef string) string {
	var assetID string
	_ = db.QueryRow(ctx, `SELECT t.input_snapshot->>'assetId' FROM agent_turns t
		JOIN agent_threads th ON th.thread_id=t.thread_id
		WHERE t.turn_id=$1 AND th.source_kind=$2`, payloadRef, threadSourceTriage).Scan(&assetID)
	if assetID != "" {
		return assetID
	}
	_ = db.QueryRow(ctx, `SELECT asset_id FROM triage_clusters WHERE cluster_id=$1`, payloadRef).Scan(&assetID)
	if assetID != "" {
		return assetID
	}
	_ = db.QueryRow(ctx, `SELECT asset_id FROM events WHERE event_id=$1`, payloadRef).Scan(&assetID)
	return assetID
}

func requireAgent(ctx context.Context, pool *pgxpool.Pool, req interface{ Header() http.Header }) (string, error) {
	raw := bearerToken(req.Header().Get("Authorization"))
	if raw == "" {
		return "", connect.NewError(connect.CodeUnauthenticated, errors.New("missing agent access token"))
	}
	return requireAgentToken(ctx, pool, raw)
}

func revokeStoredCapability(ctx context.Context, db dbTX, raw string, pub ed25519.PublicKey) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	claims, err := kernel.VerifyCapabilityToken(raw, pub, time.Now())
	if err != nil {
		return nil
	}
	if claims.TokenID == "" {
		return nil
	}
	tag, err := db.Exec(ctx, `UPDATE capability_token_instances SET revoked=true WHERE jti=$1`, claims.TokenID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	budgetID := claims.BudgetID
	if budgetID == "" {
		budgetID = claims.TokenID
	}
	expires := time.Unix(claims.ExpiresAt, 0)
	if expires.IsZero() {
		expires = time.Now()
	}
	_, err = db.Exec(ctx, `INSERT INTO capability_token_instances(jti, budget_id, lease_id, lease_epoch, expires_at, revoked)
		VALUES($1,$2,'',$3,$4,true) ON CONFLICT (jti) DO UPDATE SET revoked=true`,
		claims.TokenID, budgetID, claims.LeaseEpoch, expires)
	return err
}
