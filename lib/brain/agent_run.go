package brain

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	authv1 "yufeng/proto/gen/authv1"
	eventv1 "yufeng/proto/gen/eventv1"
	runv1 "yufeng/proto/gen/runv1"
	"yufeng/proto/gen/runv1/runv1connect"
	workerv1 "yufeng/proto/gen/workerv1"
	"yufeng/proto/gen/workerv1/workerv1connect"

	"yufeng/lib/kernel"
)

// RunServer 是执行实例查询与控制服务。
type RunServer struct {
	pool *pgxpool.Pool
	key  ed25519.PrivateKey
}

const runViewSelectColumns = `run_id,state,role,plan_ref,created_at,deadline,error,bindings,
	agent_profile_id,agent_config_digest,case_id`

// NewRunServer 构造执行实例服务。
func NewRunServer(pool *pgxpool.Pool, key ed25519.PrivateKey) *RunServer {
	return &RunServer{pool: pool, key: key}
}

// Handler 返回 Connect 服务端处理器。
func (s *RunServer) Handler() (string, http.Handler) {
	return runv1connect.NewRunServiceHandler(s, handlerOptions()...)
}

// CreateRun 按调用者授予裁剪岗位、工具与资产范围，并创建短命执行实例。
func (s *RunServer) CreateRun(ctx context.Context, req *connect.Request[runv1.CreateRunRequest]) (*connect.Response[runv1.CreateRunResponse], error) {
	user, err := requireUser(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	clipped, err := clipCreateRun(ctx, s.pool, user, req.Msg)
	if err != nil {
		return nil, err
	}
	runID := req.Msg.RunId
	if runID == "" {
		runID, err = newID("run")
		if err != nil {
			return nil, err
		}
	}
	toolset, err := json.Marshal(clipped.tools)
	if err != nil {
		return nil, err
	}
	bindings, err := json.Marshal(clipped.bindings)
	if err != nil {
		return nil, err
	}
	workID, err := newID("work")
	if err != nil {
		return nil, err
	}
	budgetID := "work:" + workID
	budgetCalls := clipRunMaxCalls(clipped.budget)
	ttl, err := time.ParseDuration(clipped.ttl)
	if err != nil {
		return nil, err
	}
	createdAt := time.Now()
	deadline := createdAt.Add(ttl)
	planDigest := runPlanDigest(req.Msg.GetPlanRef())
	var turnID string
	err = withTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO runs(run_id, state, role, plan_ref, toolset, budget, ttl, bindings, created_by, created_at, deadline)
		VALUES($1,'pending',$2,$3,$4::jsonb,$5,$6,$7::jsonb,$8,$9,$10)`, runID, clipped.role, req.Msg.PlanRef,
			string(toolset), clipped.budget, clipped.ttl, string(bindings), user.UserId, createdAt, deadline); err != nil {
			return err
		}
		if _, err := createRunBudgetAccount(ctx, tx, budgetID, runID, budgetCalls, ttl, createdAt); err != nil {
			return err
		}
		_, turnID, err = ensureAgentTurn(ctx, tx, turnSeed{
			SourceKind: threadSourceRun, SourceRef: runID, SubjectID: runID, SourceVersion: 1,
			SourceCursor: map[string]any{"runId": runID, "workId": workID, "planDigest": planDigest},
			InputSnapshot: map[string]any{
				"runId": runID, "workId": workID, "planRef": req.Msg.GetPlanRef(), "planDigest": planDigest,
				"role": clipped.role, "bindings": clipped.bindings,
			},
			BudgetID: budgetID, ContentRef: "run-plan:" + planDigest,
		})
		if err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO work_items(work_id, run_id, turn_id, plan_digest, budget_id)
			VALUES($1,$2,$3,$4,$5)`, workID, runID, turnID, planDigest, budgetID)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO run_sagas(run_id, work_id) VALUES($1,$2)`, runID, workID); err != nil {
			return err
		}
		return appendRunEvent(ctx, tx, runID, "created", planDigest)
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&runv1.CreateRunResponse{RunId: runID, State: "pending", TurnId: turnID}), nil
}

// GetRun 返回调用者可见的单个执行实例及其当前状态。
func (s *RunServer) GetRun(ctx context.Context, req *connect.Request[runv1.GetRunRequest]) (*connect.Response[runv1.GetRunResponse], error) {
	user, err := requireUser(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	view, err := s.authorizedRun(ctx, user, req.Msg.GetRunId(), "console.read")
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&runv1.GetRunResponse{Run: view.run}), nil
}

// ListRuns 按调用者可见范围分页列出执行实例。
func (s *RunServer) ListRuns(ctx context.Context, req *connect.Request[runv1.ListRunsRequest]) (*connect.Response[runv1.ListRunsResponse], error) {
	user, err := requireUser(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	access, err := loadEffectiveAccess(ctx, s.pool, user)
	if err != nil {
		return nil, err
	}
	scope := scopeFromAccess(access)
	if !scope.hasTool("console.read") || scope.emptyObjects() {
		return nil, grantMissingError()
	}
	if _, err := pauseExpiredRunBudgetLeases(ctx, s.pool, time.Now()); err != nil {
		return nil, err
	}
	if _, err := expireDueRuns(ctx, s.pool, time.Now()); err != nil {
		return nil, err
	}
	offset, err := decodePageOffset(req.Msg.GetPageToken())
	if err != nil {
		return nil, err
	}
	limit := ClampPageSize(req.Msg.GetPageSize())
	batchSize := limit * 2
	if batchSize < kernel.PageSizeDefault {
		batchSize = kernel.PageSizeDefault
	}
	resp := &runv1.ListRunsResponse{}
	scanOffset := offset
	for {
		rows, err := s.pool.Query(ctx, `SELECT `+runViewSelectColumns+`
			FROM runs WHERE ($1='' OR state=$1)
			ORDER BY created_at DESC, run_id DESC LIMIT $2 OFFSET $3`, strings.TrimSpace(req.Msg.GetState()), batchSize, scanOffset)
		if err != nil {
			return nil, err
		}
		seen := 0
		for rows.Next() {
			candidateOffset := scanOffset + seen
			seen++
			view, err := scanRunView(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			if !scopeCoversRun(scope, view.bindings) {
				continue
			}
			view.run.BudgetSnapshot, err = loadRunBudgetForRun(ctx, s.pool, view.run.GetRunId())
			if err != nil {
				rows.Close()
				return nil, err
			}
			if len(resp.Runs) == limit {
				resp.NextPageToken = encodePageOffset(candidateOffset)
				rows.Close()
				return connect.NewResponse(resp), nil
			}
			resp.Runs = append(resp.Runs, view.run)
		}
		rowErr := rows.Err()
		rows.Close()
		if rowErr != nil {
			return nil, rowErr
		}
		if seen < batchSize {
			break
		}
		scanOffset += seen
	}
	return connect.NewResponse(resp), nil
}

// CancelRun 请求取消尚未进入不可逆终态的执行实例。
func (s *RunServer) CancelRun(ctx context.Context, req *connect.Request[runv1.CancelRunRequest]) (*connect.Response[runv1.CancelRunResponse], error) {
	user, err := requireUser(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	view, err := s.authorizedRun(ctx, user, req.Msg.GetRunId(), "run.create")
	if err != nil {
		return nil, err
	}
	if view.run.GetState() == "cancelled" {
		return connect.NewResponse(&runv1.CancelRunResponse{Run: view.run}), nil
	}
	if isRunTerminal(view.run.GetState()) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("terminal run cannot be cancelled"))
	}
	var cancelled *runv1.RunRecord
	err = withTx(ctx, s.pool, func(tx pgx.Tx) error {
		locked, err := scanRunView(tx.QueryRow(ctx, `SELECT `+runViewSelectColumns+`
			FROM runs WHERE run_id=$1 FOR UPDATE`, req.Msg.GetRunId()))
		if err != nil {
			return err
		}
		if locked.run.GetState() == "cancelled" {
			cancelled = locked.run
			return nil
		}
		if isRunTerminal(locked.run.GetState()) {
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("terminal run cannot be cancelled"))
		}
		rows, err := tx.Query(ctx, `SELECT work_id, capability_token, budget_id FROM work_items WHERE run_id=$1 FOR UPDATE`, req.Msg.GetRunId())
		if err != nil {
			return err
		}
		var tokens []string
		var workIDs []string
		budgetID := ""
		for rows.Next() {
			var workID, token, rowBudgetID string
			if err := rows.Scan(&workID, &token, &rowBudgetID); err != nil {
				rows.Close()
				return err
			}
			if token != "" {
				tokens = append(tokens, token)
			}
			workIDs = append(workIDs, workID)
			if budgetID == "" {
				budgetID = rowBudgetID
			}
		}
		rowErr := rows.Err()
		rows.Close()
		if rowErr != nil {
			return rowErr
		}
		var hasSagaEffects, hasModelEffects bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM run_saga_steps WHERE run_id=$1 AND (action_effect_started OR action_phase='outcome_unknown'))`,
			req.Msg.GetRunId()).Scan(&hasSagaEffects); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM model_attempts a
			JOIN model_generations g USING(generation_id)
			JOIN work_items w ON w.turn_id=g.turn_id
			WHERE w.run_id=$1 AND a.state IN ('effect_started','outcome_unknown'))`,
			req.Msg.GetRunId()).Scan(&hasModelEffects); err != nil {
			return err
		}
		hasEffects := hasSagaEffects || hasModelEffects
		if _, err := tx.Exec(ctx, `UPDATE run_sagas SET cancel_requested=true,
			state=CASE WHEN $2 THEN 'cancelling' ELSE 'compensated' END, updated_at=now() WHERE run_id=$1`,
			req.Msg.GetRunId(), hasEffects); err != nil {
			return err
		}
		if hasEffects {
			if _, err := tx.Exec(ctx, `UPDATE runs SET state='cancelling', updated_at=now() WHERE run_id=$1`, req.Msg.GetRunId()); err != nil {
				return err
			}
			locked.run.State = "cancelling"
		} else {
			if len(s.key) != ed25519.PrivateKeySize {
				return errors.New("run signing key is invalid")
			}
			pub := s.key.Public().(ed25519.PublicKey)
			for _, token := range tokens {
				if err := revokeStoredCapability(ctx, tx, token, pub); err != nil {
					return err
				}
			}
			if err := closeRunBudget(ctx, tx, budgetID, "cancelled", false, time.Now()); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE work_items SET status='cancelled', updated_at=now()
				WHERE run_id=$1 AND status IN ('pending','leased')`, req.Msg.GetRunId()); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE runs SET state='cancelled', updated_at=now() WHERE run_id=$1`, req.Msg.GetRunId()); err != nil {
				return err
			}
			for _, workID := range workIDs {
				if err := recordInvestigationTerminal(ctx, tx, workID, "cancelled", "cancelled", "run cancelled by user"); err != nil {
					return err
				}
			}
			locked.run.State = "cancelled"
		}
		if err := appendRunEvent(ctx, tx, req.Msg.GetRunId(), "cancel", user.UserId); err != nil {
			return err
		}
		cancelled = locked.run
		return nil
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&runv1.CancelRunResponse{Run: cancelled}), nil
}

// WatchRun 从指定序号开始流式发送调用者可见的执行实例事件。
func (s *RunServer) WatchRun(ctx context.Context, req *connect.Request[runv1.WatchRunRequest], stream *connect.ServerStream[runv1.WatchRunResponse]) error {
	user, err := requireUser(ctx, s.pool, req)
	if err != nil {
		return err
	}
	// 首查必须确认执行实例存在，否则不存在的 run_id 会空转轮询到客户端断开。
	view, err := s.authorizedRun(ctx, user, req.Msg.GetRunId(), "console.read")
	if err != nil {
		return err
	}
	run := view.run
	last := ""
	if run.State != last {
		last = run.State
		if err := stream.Send(&runv1.WatchRunResponse{Run: run}); err != nil {
			return err
		}
	}
	for {
		if isRunTerminal(last) {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Second):
		}
		view, err := s.authorizedRun(ctx, user, req.Msg.GetRunId(), "console.read")
		if err != nil {
			return err
		}
		run := view.run
		if run.State != last {
			last = run.State
			if err := stream.Send(&runv1.WatchRunResponse{Run: run}); err != nil {
				return err
			}
		}
	}
}

// ListRunEvents 分页返回指定执行实例的只追加事件记录。
func (s *RunServer) ListRunEvents(ctx context.Context, req *connect.Request[runv1.ListRunEventsRequest]) (*connect.Response[runv1.ListRunEventsResponse], error) {
	user, err := requireUser(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	if _, err := s.authorizedRun(ctx, user, req.Msg.GetRunId(), "console.read"); err != nil {
		return nil, err
	}
	offset, err := decodePageOffset(req.Msg.GetPageToken())
	if err != nil {
		return nil, err
	}
	limit := ClampPageSize(req.Msg.GetPageSize())
	rows, err := s.pool.Query(ctx, `SELECT sequence, run_id, action, payload_digest, occurred_at
		FROM audit_entries WHERE run_id=$1 ORDER BY sequence LIMIT $2 OFFSET $3`, req.Msg.GetRunId(), limit+1, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	resp := &runv1.ListRunEventsResponse{}
	for rows.Next() {
		var e runv1.RunEvent
		var at time.Time
		if err := rows.Scan(&e.Sequence, &e.RunId, &e.Kind, &e.PayloadRef, &at); err != nil {
			return nil, err
		}
		e.Kind = strings.TrimPrefix(e.Kind, "run.")
		e.OccurredAt = timestamppb.New(at)
		resp.Events = append(resp.Events, &e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(resp.Events) > limit {
		resp.Events = resp.Events[:limit]
		resp.NextPageToken = encodePageOffset(offset + limit)
	}
	return connect.NewResponse(resp), nil
}

// WorkerServer 处理执行实例监督进程的独立工作负载身份与工作车道。
type WorkerServer struct {
	pool             *pgxpool.Pool
	key              ed25519.PrivateKey
	accessTTL        time.Duration
	refreshTTL       time.Duration
	allowAgentCompat bool
	sensitiveRelay   *SensitiveRelay
	workloadIssuer   kernel.WorkloadCertificateIssuer
}

// NewWorkerServer 构造工作进程服务；智能代理身份兼容只可由调用方显式开启。
func NewWorkerServer(pool *pgxpool.Pool, key ed25519.PrivateKey, allowAgentCompat bool, issuers ...kernel.WorkloadCertificateIssuer) *WorkerServer {
	server := &WorkerServer{
		pool: pool, key: key, accessTTL: kernel.AccessTokenTTL, refreshTTL: kernel.RefreshTokenTTL,
		allowAgentCompat: allowAgentCompat,
	}
	if len(issuers) > 0 {
		server.workloadIssuer = issuers[0]
	}
	return server
}

// Handler 返回 Connect 服务端处理器。
func (s *WorkerServer) Handler() (string, http.Handler) {
	return workerv1connect.NewWorkerServiceHandler(s, handlerOptions()...)
}

// RegisterWorker 校验工作负载身份并建立执行工作进程会话。
func (s *WorkerServer) RegisterWorker(ctx context.Context, req *connect.Request[workerv1.RegisterWorkerRequest]) (*connect.Response[workerv1.RegisterWorkerResponse], error) {
	principal, identityDomain, err := s.authenticateWorker(ctx, req)
	if err != nil {
		return nil, err
	}
	workerID := strings.TrimSpace(req.Msg.WorkerId)
	if workerID == "" {
		workerID = principal.ID
	}
	if workerID != principal.ID {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("worker_id must equal access token subject"))
	}
	kind := req.Msg.GetWorkerKind()
	if kind == workerv1.WorkerKind_WORKER_KIND_UNSPECIFIED {
		kind = principal.Kind
	}
	if kind != principal.Kind {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("worker_kind must equal access token kind"))
	}
	labels, err := json.Marshal(req.Msg.CapabilityLabels)
	if err != nil {
		return nil, err
	}
	// 请求自报的 Bindings 一律丢弃。档案只取对应身份域的当前有效授予；
	// 授予已撤销或过期时必须清空旧档案。
	trusted, err := loadWorkerIdentityBindings(ctx, s.pool, workerID, identityDomain)
	if err != nil {
		return nil, err
	}
	bindingsJSON := "[]"
	if len(trusted) > 0 {
		raw, err := json.Marshal(trusted)
		if err != nil {
			return nil, err
		}
		bindingsJSON = string(raw)
	}
	profiles, profilesJSON, maxConcurrency, err := normalizeAnalyzerProfiles(kind, req.Msg.GetAnalyzerProfiles(), req.Msg.GetMaxConcurrency())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	var enrolledLimit int32
	err = s.pool.QueryRow(ctx, `SELECT max_concurrency FROM worker_enrollments WHERE worker_id=$1 AND state='approved'`, workerID).Scan(&enrolledLimit)
	if err == nil && maxConcurrency > enrolledLimit {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("max_concurrency exceeds approved enrollment"))
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if req.Msg.GetOperatingSystem() != "" && !supportedWorkerPlatform(req.Msg.GetOperatingSystem(), req.Msg.GetArchitecture()) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("worker platform is not supported"))
	}
	sandboxes, err := json.Marshal(req.Msg.GetSandboxCapabilities())
	if err != nil {
		return nil, err
	}
	approvedDigest, attestedAt, err := s.validateApprovedWorkerRegistration(ctx, workerID, req.Msg)
	if err != nil {
		return nil, err
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO workers(
		worker_id, capability_labels, version, bindings, worker_kind, identity_domain, analyzer_profiles, max_concurrency,
		operating_system, architecture, sandbox_capabilities, memory_capacity_bytes, logical_cpu_capacity,
		approved_manifest_digest,sandbox_attested_at,updated_at)
		VALUES($1,$2::jsonb,$3,$4::jsonb,$5,$6,$7::jsonb,$8,$9,$10,$11::jsonb,$12,$13,$14,$15,now())
		ON CONFLICT (worker_id) DO UPDATE SET
			capability_labels=EXCLUDED.capability_labels,
			version=EXCLUDED.version,
			bindings=EXCLUDED.bindings,
			worker_kind=EXCLUDED.worker_kind,
			identity_domain=EXCLUDED.identity_domain,
			analyzer_profiles=EXCLUDED.analyzer_profiles,
			max_concurrency=CASE WHEN EXCLUDED.worker_kind='RUN_SUPERVISOR' THEN workers.max_concurrency ELSE EXCLUDED.max_concurrency END,
			operating_system=EXCLUDED.operating_system,
			architecture=EXCLUDED.architecture,
			sandbox_capabilities=EXCLUDED.sandbox_capabilities,
			memory_capacity_bytes=EXCLUDED.memory_capacity_bytes,
			logical_cpu_capacity=EXCLUDED.logical_cpu_capacity,
			approved_manifest_digest=EXCLUDED.approved_manifest_digest,
			sandbox_attested_at=EXCLUDED.sandbox_attested_at,
			updated_at=now()`,
		workerID, string(labels), req.Msg.Version, bindingsJSON, workerKindName(kind), identityDomain, profilesJSON, maxConcurrency,
		strings.ToLower(req.Msg.GetOperatingSystem()), strings.ToLower(req.Msg.GetArchitecture()), sandboxes,
		req.Msg.GetMemoryCapacityBytes(), req.Msg.GetLogicalCpuCapacity(), approvedDigest, attestedAt); err != nil {
		return nil, err
	}
	if kind == workerv1.WorkerKind_WORKER_KIND_RUN_SUPERVISOR {
		if err := s.pool.QueryRow(ctx, `SELECT max_concurrency FROM workers WHERE worker_id=$1`, workerID).Scan(&maxConcurrency); err != nil {
			return nil, err
		}
	}
	return connect.NewResponse(&workerv1.RegisterWorkerResponse{
		WorkerId: workerID, WorkerKind: kind, AnalyzerProfiles: profiles, MaxConcurrency: maxConcurrency,
	}), nil
}

func (s *WorkerServer) validateApprovedWorkerRegistration(ctx context.Context, workerID string, request *workerv1.RegisterWorkerRequest) (string, *time.Time, error) {
	var operatingSystem, architecture, version, manifestDigest, challengeID, publicKey string
	var sandboxRaw []byte
	var maxConcurrency, logicalCPU int32
	var memoryCapacity int64
	err := s.pool.QueryRow(ctx, `SELECT operating_system,architecture,version,sandbox_capabilities,max_concurrency,
		memory_capacity_bytes,logical_cpu_capacity,approved_manifest_digest,sandbox_challenge_id,public_key
		FROM worker_enrollments WHERE worker_id=$1 AND state='approved' ORDER BY decided_at DESC LIMIT 1`, workerID).
		Scan(&operatingSystem, &architecture, &version, &sandboxRaw, &maxConcurrency, &memoryCapacity, &logicalCPU,
			&manifestDigest, &challengeID, &publicKey)
	if errors.Is(err, pgx.ErrNoRows) {
		// 标准 Compose 的中央 worker 由部署侧固定引导，不走外部客户端审批。
		return "", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	var approvedSandbox []string
	if err := json.Unmarshal(sandboxRaw, &approvedSandbox); err != nil {
		return "", nil, err
	}
	actualSandbox := append([]string(nil), request.GetSandboxCapabilities()...)
	slices.Sort(approvedSandbox)
	slices.Sort(actualSandbox)
	if operatingSystem != strings.ToLower(request.GetOperatingSystem()) || architecture != strings.ToLower(request.GetArchitecture()) ||
		version != request.GetVersion() || maxConcurrency != request.GetMaxConcurrency() || memoryCapacity != request.GetMemoryCapacityBytes() ||
		logicalCPU != request.GetLogicalCpuCapacity() || !slices.Equal(approvedSandbox, actualSandbox) ||
		manifestDigest == "" || manifestDigest != request.GetApprovedManifestDigest() {
		return "", nil, connect.NewError(connect.CodePermissionDenied, errors.New("worker registration differs from approved manifest"))
	}
	attestation := request.GetSandboxAttestation()
	if attestation == nil || attestation.GetChallengeId() != challengeID || attestation.GetManifestDigest() != manifestDigest {
		return "", nil, connect.NewError(connect.CodePermissionDenied, errors.New("worker sandbox attestation binding is invalid"))
	}
	passed := append([]string(nil), attestation.GetPassedProbes()...)
	slices.Sort(passed)
	required := []string{"child_escape_denied", "filesystem_read_denied", "filesystem_write_denied", "network_denied"}
	if !slices.Equal(passed, required) {
		return "", nil, connect.NewError(connect.CodePermissionDenied, errors.New("worker sandbox challenge is incomplete"))
	}
	block, _ := pem.Decode([]byte(publicKey))
	if block == nil {
		return "", nil, connect.NewError(connect.CodePermissionDenied, errors.New("worker enrollment public key is invalid"))
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return "", nil, err
	}
	verificationKey, ok := parsed.(ed25519.PublicKey)
	if !ok {
		return "", nil, connect.NewError(connect.CodePermissionDenied, errors.New("worker enrollment key cannot verify sandbox challenge"))
	}
	signature, err := base64.RawURLEncoding.DecodeString(attestation.GetSignature())
	if err != nil || !ed25519.Verify(verificationKey,
		[]byte(challengeID+"\x00"+manifestDigest+"\x00"+strings.Join(passed, "\n")), signature) {
		return "", nil, connect.NewError(connect.CodePermissionDenied, errors.New("worker sandbox challenge signature is invalid"))
	}
	attestedAt := time.Now()
	passedRaw, err := json.Marshal(passed)
	if err != nil {
		return "", nil, err
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO worker_sandbox_attestations(worker_id,challenge_id,manifest_digest,passed_probes,signature,verified_at)
		VALUES($1,$2,$3,$4::jsonb,$5,$6) ON CONFLICT(worker_id) DO UPDATE SET challenge_id=EXCLUDED.challenge_id,
		manifest_digest=EXCLUDED.manifest_digest,passed_probes=EXCLUDED.passed_probes,signature=EXCLUDED.signature,verified_at=EXCLUDED.verified_at`,
		workerID, challengeID, manifestDigest, passedRaw, attestation.GetSignature(), attestedAt); err != nil {
		return "", nil, err
	}
	return manifestDigest, &attestedAt, nil
}

// PollWork 为已认证工作进程长轮询并租赁一个待执行工作项。
func (s *WorkerServer) PollWork(ctx context.Context, req *connect.Request[workerv1.PollWorkRequest]) (*connect.Response[workerv1.PollWorkResponse], error) {
	principal, err := s.requireRunWorker(ctx, req)
	if err != nil {
		return nil, err
	}
	if req.Msg.WorkerId != "" && req.Msg.WorkerId != principal.ID {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("access token subject must match worker_id"))
	}
	if _, err := pauseExpiredRunBudgetLeases(ctx, s.pool, time.Now()); err != nil {
		return nil, err
	}
	if _, err := expireDueRuns(ctx, s.pool, time.Now()); err != nil {
		return nil, err
	}
	wait := time.Duration(req.Msg.LongPollSeconds) * time.Second
	if wait <= 0 {
		wait = 20 * time.Second
	}
	if wait > pollMaxWait {
		wait = pollMaxWait
	}
	deadline := time.Now().Add(wait)
	for {
		item, err := s.leaseWork(ctx, principal.ID)
		if err != nil {
			return nil, err
		}
		if item != nil {
			return connect.NewResponse(&workerv1.PollWorkResponse{Work: item}), nil
		}
		if time.Now().After(deadline) {
			return connect.NewResponse(&workerv1.PollWorkResponse{}), nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pollTick):
		}
	}
}

// ExtendLease 延长当前工作项租约并轮换同一预算账户与所有权代次的能力令牌。
// 正常续租保持 lease_id / lease_epoch / budget_id；旧令牌可在原 exp 前完成同代次在途请求，不得因续租制造假失败。
func (s *WorkerServer) ExtendLease(ctx context.Context, req *connect.Request[workerv1.ExtendLeaseRequest]) (*connect.Response[workerv1.ExtendLeaseResponse], error) {
	principal, err := s.requireRunWorker(ctx, req)
	if err != nil {
		return nil, err
	}
	var runID, role, budget, budgetID, oldToken string
	var leaseEpoch int64
	var toolsetJSON, bindingsJSON []byte
	var executionDeadline *time.Time
	err = s.pool.QueryRow(ctx, `SELECT r.run_id, r.role, r.toolset, r.bindings, r.budget, w.budget_id, w.lease_epoch, w.capability_token, r.deadline
		FROM work_items w JOIN runs r USING(run_id)
		WHERE w.work_id=$1 AND w.lease_id=$2 AND w.worker_id=$3 AND w.lease_epoch=$4 AND w.status='leased' AND w.lease_deadline > now()`,
		req.Msg.WorkId, req.Msg.LeaseId, principal.ID, req.Msg.LeaseEpoch).
		Scan(&runID, &role, &toolsetJSON, &bindingsJSON, &budget, &budgetID, &leaseEpoch, &oldToken, &executionDeadline)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("work item lease is expired or not held"))
	}
	if err != nil {
		return nil, err
	}
	if budgetID == "" {
		budgetID = "work:" + req.Msg.WorkId
	}
	now := time.Now()
	if executionDeadline != nil && !now.Before(*executionDeadline) {
		if _, expireErr := expireRunDeadline(ctx, s.pool, runID, now); expireErr != nil {
			return nil, expireErr
		}
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("run execution deadline exceeded"))
	}
	until := now.Add(instrLeaseTTL)
	tokenUntil := until
	if executionDeadline != nil && executionDeadline.Before(tokenUntil) {
		tokenUntil = *executionDeadline
	}
	var tools, bindings []string
	if err := json.Unmarshal(toolsetJSON, &tools); err != nil {
		return nil, fmt.Errorf("work %s toolset: %w", req.Msg.WorkId, err)
	}
	if err := json.Unmarshal(bindingsJSON, &bindings); err != nil {
		return nil, fmt.Errorf("work %s bindings: %w", req.Msg.WorkId, err)
	}
	jti, err := newID("jti")
	if err != nil {
		return nil, err
	}
	token, err := kernel.SignCapabilityToken(kernel.Claims{
		Subject: runID, AuthorizedParty: principal.ID, Role: role, Audience: "tools", TokenID: jti,
		BudgetID: budgetID, LeaseEpoch: leaseEpoch,
		ExpiresAt: tokenUntil.Unix(), IssuedAt: now.Unix(), NotBefore: now.Unix() - 1,
		Tools: tools, Bindings: bindings, MaxCalls: clipRunMaxCalls(budget),
	}, s.key)
	if err != nil {
		return nil, err
	}
	cancelRequested := false
	err = withTx(ctx, s.pool, func(tx pgx.Tx) error {
		var lockedToken string
		if err := tx.QueryRow(ctx, `SELECT capability_token FROM work_items
			WHERE work_id=$1 AND lease_id=$2 AND worker_id=$3 AND lease_epoch=$4
			  AND status='leased' AND lease_deadline > now() FOR UPDATE`,
			req.Msg.WorkId, req.Msg.LeaseId, principal.ID, req.Msg.LeaseEpoch).Scan(&lockedToken); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return connect.NewError(connect.CodeFailedPrecondition, errors.New("work item lease is expired or not held"))
			}
			return err
		}
		if lockedToken != oldToken {
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("work item capability token changed"))
		}
		if _, err := tx.Exec(ctx, `INSERT INTO run_sagas(run_id, work_id) VALUES($1,$2) ON CONFLICT (run_id) DO NOTHING`, runID, req.Msg.WorkId); err != nil {
			return err
		}
		var lockedDeadline *time.Time
		if err := tx.QueryRow(ctx, `SELECT deadline FROM runs WHERE run_id=$1 FOR UPDATE`, runID).Scan(&lockedDeadline); err != nil {
			return err
		}
		if lockedDeadline != nil && !time.Now().Before(*lockedDeadline) {
			return connect.NewError(connect.CodeResourceExhausted, errors.New("run execution deadline exceeded"))
		}
		if err := tx.QueryRow(ctx, `SELECT cancel_requested FROM run_sagas WHERE run_id=$1 FOR UPDATE`, runID).Scan(&cancelRequested); err != nil {
			return err
		}
		if err := startRunBudgetActive(ctx, tx, budgetID, now); err != nil {
			return err
		}
		if err := seedCapabilityBudget(ctx, tx, budgetID, runID, principal.ID, clipRunMaxCalls(budget), tokenUntil); err != nil {
			return err
		}
		if err := registerCapabilityToken(ctx, tx, jti, budgetID, req.Msg.LeaseId, leaseEpoch, tokenUntil); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE work_items SET lease_deadline=$1, capability_token=$2, budget_id=$3, lease_epoch=$4, updated_at=now()
			WHERE work_id=$5 AND lease_id=$6 AND worker_id=$7 AND status='leased' AND lease_deadline > now() AND capability_token=$8`,
			until, token, budgetID, leaseEpoch, req.Msg.WorkId, req.Msg.LeaseId, principal.ID, oldToken)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("work item lease is expired or not held"))
		}
		return appendRunEvent(ctx, tx, runID, "lease_extended", req.Msg.LeaseId)
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&workerv1.ExtendLeaseResponse{
		LeaseDeadline: timestamppb.New(until), CapabilityToken: token, LeaseId: req.Msg.LeaseId,
		BudgetId: budgetID, LeaseEpoch: leaseEpoch, CancelRequested: cancelRequested,
	}), nil
}

// ReportProgress 验证当前租约后追加进度或补偿事务回执，并返回权威恢复快照。
func (s *WorkerServer) ReportProgress(ctx context.Context, req *connect.Request[workerv1.ReportProgressRequest]) (*connect.Response[workerv1.ReportProgressResponse], error) {
	principal, err := s.requireRunWorker(ctx, req)
	if err != nil {
		return nil, err
	}
	if req.Msg.GetSagaPlan() != nil && req.Msg.GetSagaReceipt() != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("report progress accepts one saga operation"))
	}
	if (req.Msg.GetSagaPlan() != nil || req.Msg.GetSagaReceipt() != nil) &&
		(strings.TrimSpace(req.Msg.GetStage()) != "" || strings.TrimSpace(req.Msg.GetPayloadRef()) != "") {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("typed saga progress cannot include generic progress fields"))
	}
	if stage := strings.TrimSpace(req.Msg.GetStage()); stage != "" && !validRunProgressStage(stage) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("progress stage is invalid"))
	}
	var snapshot *workerv1.RunSagaSnapshot
	err = withTx(ctx, s.pool, func(tx pgx.Tx) error {
		var live bool
		if err := tx.QueryRow(ctx, `SELECT true FROM work_items WHERE work_id=$1 AND lease_id=$2 AND lease_epoch=$3
			AND worker_id=$4 AND status='leased' AND lease_deadline > now() FOR UPDATE`, req.Msg.WorkId, req.Msg.LeaseId, req.Msg.LeaseEpoch, principal.ID).Scan(&live); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return connect.NewError(connect.CodeFailedPrecondition, errors.New("work item is not leased to this lease"))
			}
			return err
		}
		runID, err := workRunID(ctx, tx, req.Msg.WorkId)
		if err != nil {
			return err
		}
		if req.Msg.GetSagaPlan() != nil {
			snapshot, err = bindRunSaga(ctx, tx, runID, req.Msg.GetSagaPlan())
			return err
		}
		if req.Msg.GetSagaReceipt() != nil {
			snapshot, err = recordRunSaga(ctx, tx, runID, req.Msg.GetSagaReceipt())
			return err
		}
		stage := strings.TrimSpace(req.Msg.GetStage())
		if stage == "" {
			snapshot, err = loadRunSagaSnapshot(ctx, tx, runID)
			return err
		}
		if err := appendRunEvent(ctx, tx, runID, stage, req.Msg.GetPayloadRef()); err != nil {
			return err
		}
		snapshot, err = loadRunSagaSnapshot(ctx, tx, runID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&workerv1.ReportProgressResponse{SagaSnapshot: snapshot}), nil
}

// CompleteWork 结算预算并把与当前租约匹配的工作项推进到成功终态。
func (s *WorkerServer) CompleteWork(ctx context.Context, req *connect.Request[workerv1.CompleteWorkRequest]) (*connect.Response[workerv1.CompleteWorkResponse], error) {
	principal, err := s.requireRunWorker(ctx, req)
	if err != nil {
		return nil, err
	}
	runID, err := workRunID(ctx, s.pool, req.Msg.WorkId)
	if err != nil {
		return nil, err
	}
	if expired, err := expireRunDeadline(ctx, s.pool, runID, time.Now()); err != nil {
		return nil, err
	} else if expired {
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("run execution deadline exceeded"))
	}
	err = withTx(ctx, s.pool, func(tx pgx.Tx) error {
		var budgetID, turnID string
		if err := tx.QueryRow(ctx, `SELECT budget_id, turn_id FROM work_items WHERE work_id=$1 FOR UPDATE`, req.Msg.WorkId).
			Scan(&budgetID, &turnID); err != nil {
			return err
		}
		var lockedDeadline *time.Time
		if err := tx.QueryRow(ctx, `SELECT deadline FROM runs WHERE run_id=$1 FOR UPDATE`, runID).Scan(&lockedDeadline); err != nil {
			return err
		}
		if lockedDeadline != nil && !time.Now().Before(*lockedDeadline) {
			return connect.NewError(connect.CodeResourceExhausted, errors.New("run execution deadline exceeded"))
		}
		// 只允许持有有效租约的领取者完成：过期租约或错误 lease_id 不得把执行实例标成成功。
		tag, err := tx.Exec(ctx, `UPDATE work_items SET status='succeeded', result_ref=$1, receipt=$2, updated_at=now()
	WHERE work_id=$3 AND lease_id=$4 AND worker_id=$5 AND lease_epoch=$6 AND status='leased' AND lease_deadline > now()`,
			req.Msg.ResultRef, req.Msg.Receipt, req.Msg.WorkId, req.Msg.LeaseId, principal.ID, req.Msg.LeaseEpoch)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("work item is not leased to this lease"))
		}
		investigation, err := validateInvestigationCompletion(ctx, tx, req.Msg.WorkId, req.Msg.GetResultRef(), req.Msg.GetReceipt())
		if err != nil {
			return err
		}
		if !investigation {
			if _, _, err := ensureRunSagaTerminal(ctx, tx, runID, true); err != nil {
				return err
			}
		} else if _, err := tx.Exec(ctx, `UPDATE run_sagas SET state='ready', updated_at=now() WHERE run_id=$1`, runID); err != nil {
			return err
		}
		if err := settleRunBudgetByKey(ctx, tx, budgetID, "step", req.Msg.WorkId, "settled", runBudgetAmount{Steps: 1}); err != nil {
			return err
		}
		if err := closeRunBudget(ctx, tx, budgetID, "completed", true, time.Now()); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE runs SET state='succeeded', updated_at=now() WHERE run_id=$1`, runID); err != nil {
			return err
		}
		if turnID != "" {
			if _, err := tx.Exec(ctx, `UPDATE agent_turns SET state='completed', updated_at=now()
				WHERE turn_id=$1 AND state NOT IN ('completed','failed','cancelled','outcome_unknown')`, turnID); err != nil {
				return err
			}
		}
		return appendRunEvent(ctx, tx, runID, "complete", req.Msg.Receipt)
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&workerv1.CompleteWorkResponse{}), nil
}

// FailWork 结算预算并把与当前租约匹配的工作项推进到失败终态。
func (s *WorkerServer) FailWork(ctx context.Context, req *connect.Request[workerv1.FailWorkRequest]) (*connect.Response[workerv1.FailWorkResponse], error) {
	principal, err := s.requireRunWorker(ctx, req)
	if err != nil {
		return nil, err
	}
	runID, err := workRunID(ctx, s.pool, req.Msg.WorkId)
	if err != nil {
		return nil, err
	}
	if expired, expireErr := expireRunDeadline(ctx, s.pool, runID, time.Now()); expireErr != nil {
		return nil, expireErr
	} else if expired {
		return connect.NewResponse(&workerv1.FailWorkResponse{}), nil
	}
	err = withTx(ctx, s.pool, func(tx pgx.Tx) error {
		var budgetID, turnID string
		if err := tx.QueryRow(ctx, `SELECT budget_id, turn_id FROM work_items WHERE work_id=$1 FOR UPDATE`, req.Msg.WorkId).
			Scan(&budgetID, &turnID); err != nil {
			return err
		}
		var lockedDeadline *time.Time
		if err := tx.QueryRow(ctx, `SELECT deadline FROM runs WHERE run_id=$1 FOR UPDATE`, runID).Scan(&lockedDeadline); err != nil {
			return err
		}
		if lockedDeadline != nil && !time.Now().Before(*lockedDeadline) {
			return connect.NewError(connect.CodeResourceExhausted, errors.New("run execution deadline exceeded"))
		}
		tag, err := tx.Exec(ctx, `UPDATE work_items SET status='failed', updated_at=now()
	WHERE work_id=$1 AND lease_id=$2 AND worker_id=$3 AND lease_epoch=$4 AND status='leased' AND lease_deadline > now()`,
			req.Msg.WorkId, req.Msg.LeaseId, principal.ID, req.Msg.LeaseEpoch)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("work item is not leased to this lease"))
		}
		_, investigationInput, err := investigationInputForWork(ctx, tx, req.Msg.WorkId)
		if err != nil {
			return err
		}
		sagaState, cancelRequested := "compensated", false
		if investigationInput == nil {
			sagaState, cancelRequested, err = ensureRunSagaTerminal(ctx, tx, runID, false)
			if err != nil {
				return err
			}
		} else {
			if err := tx.QueryRow(ctx, `SELECT cancel_requested FROM run_sagas WHERE run_id=$1 FOR UPDATE`, runID).Scan(&cancelRequested); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE run_sagas SET state='compensated', updated_at=now() WHERE run_id=$1`, runID); err != nil {
				return err
			}
		}
		workState, runState, budgetState, turnState, eventKind := "failed", "failed", "failed", "failed", "fail"
		if sagaState == "outcome_unknown" {
			runState, budgetState, turnState, eventKind = "outcome_unknown", "outcome_unknown", "outcome_unknown", "outcome_unknown"
		} else if cancelRequested {
			workState, runState, budgetState, turnState, eventKind = "cancelled", "cancelled", "cancelled", "cancelled", "cancel"
		}
		if workState != "failed" {
			if _, err := tx.Exec(ctx, `UPDATE work_items SET status=$2 WHERE work_id=$1`, req.Msg.WorkId, workState); err != nil {
				return err
			}
		}
		investigationStatus := "failed"
		if workState == "cancelled" {
			investigationStatus = "cancelled"
		}
		if err := recordInvestigationTerminal(ctx, tx, req.Msg.WorkId, investigationStatus, req.Msg.GetErrorCode(), req.Msg.GetMessage()); err != nil {
			return err
		}
		if err := settleRunBudgetByKey(ctx, tx, budgetID, "step", req.Msg.WorkId, "settled", runBudgetAmount{Steps: 1}); err != nil {
			return err
		}
		if err := closeRunBudget(ctx, tx, budgetID, budgetState, false, time.Now()); err != nil {
			return err
		}
		runError := req.Msg.GetMessage()
		if investigationInput != nil {
			runError = auditPayloadDigest(runError)
		}
		if _, err := tx.Exec(ctx, `UPDATE runs SET state=$1, error=$2, updated_at=now() WHERE run_id=$3`, runState, runError, runID); err != nil {
			return err
		}
		if turnID != "" {
			if _, err := tx.Exec(ctx, `UPDATE agent_turns SET state=$2, updated_at=now()
				WHERE turn_id=$1 AND state NOT IN ('completed','failed','cancelled','outcome_unknown')`, turnID, turnState); err != nil {
				return err
			}
		}
		return appendRunEvent(ctx, tx, runID, eventKind, req.Msg.Message)
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&workerv1.FailWorkResponse{}), nil
}

func (s *WorkerServer) leaseWork(ctx context.Context, workerID string) (*workerv1.WorkItem, error) {
	exists, profile, err := loadWorkerProfile(ctx, s.pool, workerID)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}
	leaseID, err := newID("lease")
	if err != nil {
		return nil, err
	}
	excluded := []string{}
	for {
		item, skipID, err := s.tryLeaseWork(ctx, workerID, leaseID, profile, excluded)
		if err != nil {
			return nil, err
		}
		if skipID != "" {
			excluded = append(excluded, skipID)
			continue
		}
		return item, nil
	}
}

func (s *WorkerServer) tryLeaseWork(ctx context.Context, workerID, leaseID string, profile, excluded []string) (*workerv1.WorkItem, string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var operatingSystem string
	var approvedManifestDigest string
	var sandboxAttestedAt *time.Time
	var sandboxRaw []byte
	var maxConcurrency, activeLeases int
	if err := tx.QueryRow(ctx, `SELECT operating_system, sandbox_capabilities, max_concurrency,
		approved_manifest_digest,sandbox_attested_at
		FROM workers WHERE worker_id=$1 FOR UPDATE`, workerID).
		Scan(&operatingSystem, &sandboxRaw, &maxConcurrency, &approvedManifestDigest, &sandboxAttestedAt); err != nil {
		return nil, "", err
	}
	var expiredChangeID string
	var previousCapacity int
	err = tx.QueryRow(ctx, `SELECT change_id, previous_capacity FROM worker_capacity_changes
		WHERE worker_id=$1 AND state='approved' AND expires_at<=now()
		ORDER BY created_at DESC LIMIT 1 FOR UPDATE`, workerID).Scan(&expiredChangeID, &previousCapacity)
	if err == nil {
		maxConcurrency = previousCapacity
		if _, err := tx.Exec(ctx, `UPDATE workers SET max_concurrency=$2, updated_at=now() WHERE worker_id=$1`, workerID, maxConcurrency); err != nil {
			return nil, "", err
		}
		if _, err := tx.Exec(ctx, `UPDATE worker_capacity_changes SET state='expired' WHERE change_id=$1`, expiredChangeID); err != nil {
			return nil, "", err
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, "", err
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM work_items
		WHERE worker_id=$1 AND status='leased' AND lease_deadline>now()`, workerID).Scan(&activeLeases); err != nil {
		return nil, "", err
	}
	if activeLeases >= maxConcurrency {
		if err := tx.Commit(ctx); err != nil {
			return nil, "", err
		}
		return nil, "", nil
	}
	var workID, runID, role, planRef, budgetID, turnID string
	var investigationEventID, investigationTicketDigest, investigationClusterID string
	var investigationCaseID, reviewCandidateID, sensitiveContentRef string
	var workAgentID, workAgentConfigDigest string
	var leaseEpoch int64
	var toolsetJSON, bindingsJSON []byte
	var budget, ttl string
	var executionDeadline, previousLeaseDeadline *time.Time
	var previousStatus string
	err = tx.QueryRow(ctx, `SELECT work_id, run_id, role, plan_ref, toolset::text, budget, ttl, bindings::text,
		work_items.budget_id, work_items.lease_epoch, work_items.turn_id,
		work_items.investigation_event_id, work_items.investigation_ticket_digest, work_items.investigation_cluster_id, runs.deadline,
		work_items.investigation_case_id, work_items.review_candidate_id, work_items.sensitive_content_ref,
		work_items.agent_id,work_items.agent_config_digest,work_items.status, work_items.lease_deadline
		FROM work_items JOIN runs USING(run_id)
		WHERE (work_items.status='pending' OR (work_items.status='leased' AND work_items.lease_deadline < now()))
		  AND (runs.deadline IS NULL OR runs.deadline > now())
		  AND NOT (work_id = ANY($1))
		ORDER BY work_items.created_at LIMIT 1 FOR UPDATE OF work_items SKIP LOCKED`, excluded).
		Scan(&workID, &runID, &role, &planRef, &toolsetJSON, &budget, &ttl, &bindingsJSON,
			&budgetID, &leaseEpoch, &turnID, &investigationEventID, &investigationTicketDigest, &investigationClusterID,
			&executionDeadline, &investigationCaseID, &reviewCandidateID, &sensitiveContentRef,
			&workAgentID, &workAgentConfigDigest, &previousStatus, &previousLeaseDeadline)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	if investigationEventID != "" || investigationCaseID != "" {
		var capabilities []string
		if err := json.Unmarshal(sandboxRaw, &capabilities); err != nil {
			return nil, "", err
		}
		if !hasVerifiedSandbox(operatingSystem, capabilities) || (approvedManifestDigest != "" && sandboxAttestedAt == nil) {
			return nil, workID, nil
		}
	} else if operatingSystem != "" && operatingSystem != "linux" {
		// 新字段保持协议向后兼容：未上报平台的旧工作进程只可继续领取既有非调查工作；
		// 流量调查仍由上面的可验证沙箱门禁失败关闭。
		return nil, workID, nil
	}
	now := time.Now()
	if err := tx.QueryRow(ctx, `SELECT deadline FROM runs
		WHERE run_id=$1 AND state NOT IN ('succeeded','failed','cancelled','outcome_unknown') FOR UPDATE`, runID).Scan(&executionDeadline); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, workID, nil
		}
		return nil, "", err
	}
	if executionDeadline != nil && !now.Before(*executionDeadline) {
		return nil, workID, nil
	}
	if _, err := tx.Exec(ctx, `INSERT INTO run_sagas(run_id, work_id) VALUES($1,$2) ON CONFLICT (run_id) DO NOTHING`, runID, workID); err != nil {
		return nil, "", err
	}
	until := now.Add(instrLeaseTTL)
	var tools, bindings []string
	var prevCap string
	// 解码失败必须中止领取：损坏的工具集会让能力令牌按空白名单签发，
	// 权限静默收缩比报错更难排查。
	if err := json.Unmarshal(toolsetJSON, &tools); err != nil {
		return nil, "", fmt.Errorf("work %s toolset: %w", workID, err)
	}
	if err := json.Unmarshal(bindingsJSON, &bindings); err != nil {
		return nil, "", fmt.Errorf("work %s bindings: %w", workID, err)
	}
	if !bindingsSubset(bindings, profile) {
		return nil, workID, nil
	}
	if err := tx.QueryRow(ctx, `SELECT COALESCE(capability_token,'') FROM work_items WHERE work_id=$1`, workID).Scan(&prevCap); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, "", err
	}
	if err := revokeStoredCapability(ctx, tx, prevCap, s.key.Public().(ed25519.PublicKey)); err != nil {
		return nil, "", err
	}
	if budgetID == "" {
		budgetID, err = newID("budget")
		if err != nil {
			return nil, "", err
		}
	}
	if _, err := reserveRunBudget(ctx, tx, budgetID, "step", workID, runBudgetAmount{Steps: 1}); err != nil {
		return nil, "", err
	}
	if previousStatus == "leased" && previousLeaseDeadline != nil && previousLeaseDeadline.Before(now) {
		if err := stopRunBudgetActive(ctx, tx, budgetID, *previousLeaseDeadline); err != nil {
			return nil, "", err
		}
	}
	if err := startRunBudgetActive(ctx, tx, budgetID, now); err != nil {
		return nil, "", err
	}
	leaseEpoch++
	jti, err := newID("jti")
	if err != nil {
		return nil, "", err
	}
	tokenUntil := until
	if executionDeadline != nil && executionDeadline.Before(tokenUntil) {
		tokenUntil = *executionDeadline
	}
	claims := kernel.Claims{Subject: runID, AuthorizedParty: workerID, Role: role, Audience: "tools", TokenID: jti,
		BudgetID: budgetID, LeaseEpoch: leaseEpoch,
		ExpiresAt: tokenUntil.Unix(), IssuedAt: now.Unix(), NotBefore: now.Unix() - 1,
		Tools: tools, Bindings: bindings, MaxCalls: clipRunMaxCalls(budget)}
	token, err := kernel.SignCapabilityToken(claims, s.key)
	if err != nil {
		return nil, "", err
	}
	if err := seedCapabilityBudget(ctx, tx, budgetID, runID, workerID, claims.MaxCalls, tokenUntil); err != nil {
		return nil, "", err
	}
	if err := registerCapabilityToken(ctx, tx, jti, budgetID, leaseID, leaseEpoch, tokenUntil); err != nil {
		return nil, "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE work_items SET status='leased', worker_id=$1, lease_id=$2, lease_deadline=$3,
	capability_token=$4, budget_id=$5, lease_epoch=$6, updated_at=now() WHERE work_id=$7`,
		workerID, leaseID, until, token, budgetID, leaseEpoch, workID); err != nil {
		return nil, "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE runs SET state='running', updated_at=now() WHERE run_id=$1 AND state<>'cancelling'`, runID); err != nil {
		return nil, "", err
	}
	if investigationCaseID != "" {
		tag, err := tx.Exec(ctx, `UPDATE investigation_cases SET state='investigating', updated_at=now()
			WHERE case_id=$1 AND state='queued'`, investigationCaseID)
		if err != nil {
			return nil, "", err
		}
		if tag.RowsAffected() == 1 {
			if _, err := tx.Exec(ctx, `INSERT INTO case_activities(case_id, kind, ref_id, summary)
				VALUES($1,'run_progress',$2,'短命只读调查执行实例已开始')`, investigationCaseID, runID); err != nil {
				return nil, "", err
			}
		}
	}
	threadID, resumeGenerationID, stepID := "", "", ""
	var investigationInput *workerv1.InvestigationInput
	if investigationEventID != "" {
		var raw []byte
		var storedDigest, status string
		if err := tx.QueryRow(ctx, `SELECT ticket, ticket_digest, status FROM check_tickets WHERE event_id=$1`, investigationEventID).
			Scan(&raw, &storedDigest, &status); err != nil {
			return nil, "", err
		}
		if status != checkTicketReady || storedDigest == "" || storedDigest != investigationTicketDigest {
			return nil, "", errors.New("investigation work ticket is not ready")
		}
		var ticket eventv1.CheckTicket
		if err := protojson.Unmarshal(raw, &ticket); err != nil {
			return nil, "", err
		}
		digest, err := kernel.CheckTicketDigest(&ticket)
		if err != nil || digest != storedDigest {
			return nil, "", errors.New("investigation work ticket digest mismatch")
		}
		investigationInput = &workerv1.InvestigationInput{Ticket: &ticket, TicketDigest: storedDigest, ClusterId: investigationClusterID}
	} else if investigationCaseID != "" {
		var approvalID, contentDigest string
		var maxBytes int64
		var expires time.Time
		if err := tx.QueryRow(ctx, `SELECT a.approval_id, a.max_bytes, a.expires_at
			FROM evidence_approvals a WHERE a.case_id=$1 AND a.state='approved' AND a.expires_at>now()
			AND EXISTS (SELECT 1 FROM evidence_requests r WHERE r.approval_id=a.approval_id
				AND r.sensitive_content_ref=$2 AND r.state='submitted')`,
			investigationCaseID, sensitiveContentRef).Scan(&approvalID, &maxBytes, &expires); err != nil {
			return nil, "", err
		}
		if s.sensitiveRelay == nil {
			return nil, "", errors.New("sensitive relay is unavailable")
		}
		entry, ok := s.sensitiveRelay.get(sensitiveContentRef)
		if !ok {
			return nil, workID, nil
		}
		contentDigest = sensitiveEntryDigest(entry.fragments)
		investigationInput = &workerv1.InvestigationInput{CaseId: investigationCaseID, ReviewCandidateId: reviewCandidateID,
			SensitiveContentRef: sensitiveContentRef, EvidenceApprovalId: approvalID, SensitiveContentDigest: contentDigest,
			SensitiveMaxBytes: maxBytes, SensitiveExpiresAt: timestamppb.New(expires), AgentId: workAgentID,
			AgentConfigDigest: workAgentConfigDigest}
	}
	var checkpoint []byte
	var expectedItemSequence int64
	if turnID != "" {
		if err := tx.QueryRow(ctx, `SELECT thread_id, next_item_sequence, checkpoint FROM agent_turns WHERE turn_id=$1`, turnID).
			Scan(&threadID, &expectedItemSequence, &checkpoint); err != nil {
			return nil, "", err
		}
		resumeErr := tx.QueryRow(ctx, `SELECT generation_id FROM model_generations
			WHERE turn_id=$1 AND state IN ('pending','running') ORDER BY created_at DESC LIMIT 1`, turnID).
			Scan(&resumeGenerationID)
		if resumeErr != nil && !errors.Is(resumeErr, pgx.ErrNoRows) {
			return nil, "", resumeErr
		}
		if err := tx.QueryRow(ctx, `SELECT step_id FROM agent_steps
			WHERE turn_id=$1 ORDER BY step_sequence DESC LIMIT 1`, turnID).Scan(&stepID); err != nil {
			return nil, "", err
		}
		if _, err := tx.Exec(ctx, `UPDATE agent_turns SET state='running', budget_id=$2, updated_at=now()
			WHERE turn_id=$1 AND state IN ('pending','running')`, turnID, budgetID); err != nil {
			return nil, "", err
		}
	}
	leaseKind := "lease_acquired"
	if previousStatus == "leased" {
		leaseKind = "lease_reclaimed"
	}
	if err := appendRunEvent(ctx, tx, runID, leaseKind, leaseID); err != nil {
		return nil, "", err
	}
	budgetSnapshot, err := loadRunBudgetSnapshot(ctx, tx, budgetID)
	if err != nil {
		return nil, "", err
	}
	sagaSnapshot, err := loadRunSagaSnapshot(ctx, tx, runID)
	if err != nil {
		return nil, "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, "", err
	}
	if executionDeadline != nil {
		remaining := time.Until(*executionDeadline)
		if remaining < 0 {
			remaining = 0
		}
		ttl = remaining.String()
	}
	var executionDeadlineProto *timestamppb.Timestamp
	if executionDeadline != nil {
		executionDeadlineProto = timestamppb.New(*executionDeadline)
	}
	return &workerv1.WorkItem{WorkId: workID, RunId: runID, AgentRole: role, PlanRef: planRef,
		Toolset: tools, Budget: budget, Ttl: ttl, Bindings: bindings, CapabilityToken: token,
		LeaseDeadline: timestamppb.New(until), LeaseId: leaseID, BudgetId: budgetID, LeaseEpoch: leaseEpoch,
		TurnId: turnID, ThreadId: threadID, ExpectedItemSequence: expectedItemSequence,
		ResumeGenerationId: resumeGenerationID, StepId: stepID, CheckpointJson: string(checkpoint),
		BudgetSnapshot: budgetSnapshot, ExecutionDeadline: executionDeadlineProto, SagaSnapshot: sagaSnapshot,
		InvestigationInput: investigationInput, AgentId: workAgentID, AgentConfigDigest: workAgentConfigDigest}, "", nil
}

type clippedRun struct {
	role     string
	tools    []string
	bindings []string
	budget   string
	ttl      string
}

func clipCreateRun(ctx context.Context, pool *pgxpool.Pool, user *authv1.User, req *runv1.CreateRunRequest) (clippedRun, error) {
	access, err := loadEffectiveAccess(ctx, pool, user)
	if err != nil {
		return clippedRun{}, err
	}
	scope := scopeFromAccess(access)
	if !scope.hasTool("run.create") {
		return clippedRun{}, grantMissingError()
	}
	if len(req.Bindings) == 0 {
		return clippedRun{}, objectDenied()
	}
	for _, b := range req.Bindings {
		id, kind := parseBindingString(b)
		ok := false
		switch kind {
		case "unit":
			ok = scope.units[id]
		case "release":
			ok = scope.releases[id]
		default:
			ok = scope.coversAsset(id)
		}
		if !ok {
			return clippedRun{}, objectDenied()
		}
	}
	var tools []string
	for _, t := range req.Toolset {
		if isRunPrimitive(t) {
			tools = append(tools, t)
		}
	}
	if tools == nil {
		tools = []string{}
	}
	return clippedRun{
		role:     "worker",
		tools:    tools,
		bindings: append([]string(nil), req.Bindings...),
		budget:   clipRunBudget(req.Budget),
		ttl:      clipRunTTL(req.Ttl),
	}, nil
}

func parseBindingString(b string) (id, kind string) {
	switch {
	case strings.HasPrefix(b, "asset:"):
		return strings.TrimPrefix(b, "asset:"), "asset"
	case strings.HasPrefix(b, "unit:"):
		return strings.TrimPrefix(b, "unit:"), "unit"
	case strings.HasPrefix(b, "release:"):
		return strings.TrimPrefix(b, "release:"), "release"
	case strings.HasPrefix(b, "cluster:"):
		return strings.TrimPrefix(b, "cluster:"), "cluster"
	case strings.HasPrefix(b, "event:"):
		return strings.TrimPrefix(b, "event:"), "event"
	case strings.HasPrefix(b, "case:"):
		return strings.TrimPrefix(b, "case:"), "case"
	default:
		return b, "asset"
	}
}

func isRunPrimitive(name string) bool {
	switch name {
	case "model.generate", "ping", "exec",
		"file.read", "file.write", "file.delete",
		"pkg.install", "pkg.remove",
		"service.start", "service.stop", "service.restart",
		"net.probe", "sys.probe", "artifact.apply", "self.update":
		return true
	default:
		return false
	}
}

func clipRunBudget(raw string) string {
	n := clipRunMaxCalls(raw)
	return strconv.FormatInt(n, 10)
}

func clipRunMaxCalls(raw string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || n <= 0 {
		return 1
	}
	if n > 100 {
		return 100
	}
	return n
}

func clipRunTTL(raw string) string {
	d, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil || d <= 0 {
		d = kernel.CapabilityTokenMaxTTL
	}
	if d > kernel.CapabilityTokenMaxTTL {
		d = kernel.CapabilityTokenMaxTTL
	}
	return d.String()
}

func loadWorkerProfile(ctx context.Context, pool *pgxpool.Pool, workerID string) (bool, []string, error) {
	var identityDomain, kind string
	err := pool.QueryRow(ctx, `SELECT identity_domain, worker_kind FROM workers WHERE worker_id=$1`, workerID).Scan(&identityDomain, &kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, err
	}
	if kind != "RUN_SUPERVISOR" {
		return false, nil, nil
	}
	bindings, err := loadWorkerIdentityBindings(ctx, pool, workerID, identityDomain)
	if err != nil {
		return false, nil, err
	}
	return true, bindings, nil
}

func bindingsSubset(work, profile []string) bool {
	if len(profile) == 0 || len(work) == 0 {
		return false
	}
	have := map[string]bool{}
	for _, b := range profile {
		id, kind := parseBindingString(b)
		have[kind+":"+id] = true
	}
	checked := 0
	for _, b := range work {
		id, kind := parseBindingString(b)
		if kind == "case" {
			// 案件引用是 Brain 从资产关系派生的 run 对象边界；
			// worker 档案只被授予资产，不复制案件授权。
			continue
		}
		checked++
		if !have[kind+":"+id] {
			return false
		}
	}
	return checked > 0
}

func loadWorkerIdentityBindings(ctx context.Context, db dbTX, workerID, identityDomain string) ([]string, error) {
	if identityDomain == "agent_compat" {
		return loadSubjectGrantBindings(ctx, db, "agent", workerID)
	}
	return loadSubjectGrantBindings(ctx, db, "worker", workerID)
}

func loadSubjectGrantBindings(ctx context.Context, db dbTX, subjectKind, subjectID string) ([]string, error) {
	rows, err := db.Query(ctx, `SELECT bindings FROM grants
		WHERE subject_kind=$1 AND subject_id=$2 AND (expires_at IS NULL OR expires_at > now())`, subjectKind, subjectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	var out []string
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var refs []struct {
			Kind string `json:"kind"`
			ID   string `json:"id"`
		}
		if json.Unmarshal(raw, &refs) != nil {
			continue
		}
		for _, r := range refs {
			if r.ID == "" || r.ID == "*" || r.ID == "bootstrap" {
				continue
			}
			kind := r.Kind
			if kind == "" {
				kind = "asset"
			}
			key := kind + ":" + r.ID
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, key)
		}
	}
	return out, rows.Err()
}

func appendRunEvent(ctx context.Context, db dbTX, runID, kind, payload string) error {
	if runID == "" {
		return nil
	}
	coordinates, workerID, err := runAuditCoordinates(ctx, db, runID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if workerID == "" {
		workerID = "brain"
	}
	digest := auditPayloadDigest(payload)
	coordinates.PayloadDigest = digest
	return appendAgentAuditTx(ctx, db, "worker", workerID, "run."+strings.TrimSpace(kind), "run", runID,
		coordinates,
		map[string]any{"payload_digest": digest})
}

func validRunProgressStage(stage string) bool {
	if stage == "" || len(stage) > 128 {
		return false
	}
	for _, r := range stage {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == ':' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func runAuditCoordinates(ctx context.Context, db dbTX, runID string) (auditCoordinates, string, error) {
	coordinates := auditCoordinates{RunID: runID}
	var workerID string
	err := db.QueryRow(ctx, `SELECT turn_id, budget_id, worker_id, lease_epoch FROM work_items
		WHERE run_id=$1 ORDER BY created_at, work_id LIMIT 1`, runID).
		Scan(&coordinates.TurnID, &coordinates.BudgetID, &workerID, &coordinates.LeaseEpoch)
	return coordinates, workerID, err
}

func workRunID(ctx context.Context, db dbTX, workID string) (string, error) {
	var id string
	if err := db.QueryRow(ctx, `SELECT run_id FROM work_items WHERE work_id=$1`, workID).Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", connect.NewError(connect.CodeNotFound, errors.New("work item not found"))
		}
		return "", err
	}
	return id, nil
}

// ReconstructRunEvents 按 run_id 从权威审计链重建事件序列。
func ReconstructRunEvents(ctx context.Context, pool *pgxpool.Pool, runID string) ([]string, error) {
	rows, err := pool.Query(ctx, `SELECT action FROM audit_entries WHERE run_id=$1 ORDER BY sequence`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out = append(out, strings.TrimPrefix(k, "run."))
	}
	return out, rows.Err()
}

type runView struct {
	run      *runv1.RunRecord
	bindings []string
}

func (s *RunServer) authorizedRun(ctx context.Context, user *authv1.User, runID, tool string) (*runView, error) {
	access, err := loadEffectiveAccess(ctx, s.pool, user)
	if err != nil {
		return nil, err
	}
	scope := scopeFromAccess(access)
	if !scope.hasTool(tool) {
		return nil, grantMissingError()
	}
	if _, err := pauseExpiredRunBudgetLeases(ctx, s.pool, time.Now()); err != nil {
		return nil, err
	}
	if _, err := expireRunDeadline(ctx, s.pool, runID, time.Now()); err != nil {
		return nil, err
	}
	view, err := scanRunView(s.pool.QueryRow(ctx, `SELECT `+runViewSelectColumns+`
		FROM runs WHERE run_id=$1`, runID))
	if err != nil {
		return nil, err
	}
	if !scopeCoversRun(scope, view.bindings) {
		return nil, objectDenied()
	}
	view.run.BudgetSnapshot, err = loadRunBudgetForRun(ctx, s.pool, runID)
	if err != nil {
		return nil, err
	}
	return view, nil
}

func scopeCoversRun(scope accessScope, bindings []string) bool {
	if len(bindings) == 0 {
		return false
	}
	authoritative := 0
	for _, binding := range bindings {
		id, kind := parseBindingString(binding)
		if strings.TrimSpace(id) == "" {
			return false
		}
		var covered bool
		switch kind {
		case "unit":
			authoritative++
			covered = scope.units[id]
		case "release":
			authoritative++
			covered = scope.releases[id]
		case "cluster", "event":
			// 聚类和事件只是已由中台绑定到同一资产的调查选择器；
			// 用户可见性仍由同一执行实例中至少一个资产、单元或发布绑定决定。
			covered = true
		default:
			authoritative++
			covered = scope.coversAsset(id)
		}
		if !covered {
			return false
		}
	}
	return authoritative > 0
}

func isRunTerminal(state string) bool {
	switch state {
	case "succeeded", "failed", "cancelled", "outcome_unknown":
		return true
	default:
		return false
	}
}

func scanRunView(row pgx.Row) (*runView, error) {
	var r runv1.RunRecord
	var at time.Time
	var deadline *time.Time
	var bindingsRaw []byte
	if err := row.Scan(&r.RunId, &r.State, &r.Role, &r.PlanRef, &at, &deadline, &r.Error, &bindingsRaw,
		&r.AgentId, &r.AgentConfigDigest, &r.CaseId); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("run not found"))
		}
		return nil, err
	}
	var bindings []string
	if err := json.Unmarshal(bindingsRaw, &bindings); err != nil {
		return nil, fmt.Errorf("run %s bindings: %w", r.RunId, err)
	}
	r.CreatedAt = timestamppb.New(at)
	if deadline != nil {
		r.Deadline = timestamppb.New(*deadline)
	}
	return &runView{run: &r, bindings: bindings}, nil
}
