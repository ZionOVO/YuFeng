package brain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/types/known/timestamppb"

	"yufeng/lib/kernel"
	commonv1 "yufeng/proto/gen/commonv1"
	workerv1 "yufeng/proto/gen/workerv1"
)

// RequestWorkerEnrollment 记录外部客户端公钥与可验证平台能力，等待管理员核对指纹。
func (s *WorkerServer) RequestWorkerEnrollment(ctx context.Context, req *connect.Request[workerv1.RequestWorkerEnrollmentRequest]) (*connect.Response[workerv1.RequestWorkerEnrollmentResponse], error) {
	workerID := strings.TrimSpace(req.Msg.GetWorkerId())
	publicKey := strings.TrimSpace(req.Msg.GetWorkerPublicKey())
	version := strings.TrimSpace(req.Msg.GetVersion())
	if workerID == "" || publicKey == "" || !validWorkerKind(req.Msg.GetWorkerKind()) ||
		!supportedWorkerPlatform(req.Msg.GetOperatingSystem(), req.Msg.GetArchitecture()) || version == "" ||
		req.Msg.GetMemoryCapacityBytes() < 0 || req.Msg.GetLogicalCpuCapacity() < 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("worker enrollment identity or platform is invalid"))
	}
	maxConcurrency := req.Msg.GetMaxConcurrency()
	if maxConcurrency <= 0 {
		maxConcurrency = 1
	}
	if maxConcurrency > 4 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("max_concurrency exceeds central pool limit"))
	}
	fingerprint, err := kernel.ValidateWorkloadCertificateRequest(workerID, req.Msg.GetCertificateRequest(), publicKey)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	activationFingerprint, err := validateActivationPublicKey(req.Msg.GetActivationPublicKey())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	enrollmentID, err := newID("enroll")
	if err != nil {
		return nil, err
	}
	var enrollmentState string
	sandboxes, err := json.Marshal(req.Msg.GetSandboxCapabilities())
	if err != nil {
		return nil, err
	}
	err = withTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO worker_enrollments(enrollment_id, worker_id, worker_kind, public_key,
			public_key_fingerprint, hostname, operating_system, architecture, sandbox_capabilities, certificate_request,
			activation_public_key, activation_public_key_fingerprint, version, max_concurrency,
			memory_capacity_bytes, logical_cpu_capacity)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb,$10,$11,$12,$13,$14,$15,$16)
			ON CONFLICT(worker_id, public_key_fingerprint) DO UPDATE SET requested_at=now()
			RETURNING enrollment_id,state`, enrollmentID, workerID, workerKindName(req.Msg.GetWorkerKind()), publicKey, fingerprint,
			req.Msg.GetHostname(), strings.ToLower(req.Msg.GetOperatingSystem()), strings.ToLower(req.Msg.GetArchitecture()), sandboxes,
			req.Msg.GetCertificateRequest(), req.Msg.GetActivationPublicKey(), activationFingerprint, version, maxConcurrency,
			req.Msg.GetMemoryCapacityBytes(), req.Msg.GetLogicalCpuCapacity()).Scan(&enrollmentID, &enrollmentState); err != nil {
			return err
		}
		var storedActivation string
		if err := tx.QueryRow(ctx, `SELECT activation_public_key FROM worker_enrollments WHERE enrollment_id=$1`, enrollmentID).Scan(&storedActivation); err != nil {
			return err
		}
		if storedActivation != req.Msg.GetActivationPublicKey() {
			return connect.NewError(connect.CodeAlreadyExists, errors.New("worker enrollment activation key changed"))
		}
		return appendAuditTx(ctx, tx, "worker", workerID, "worker_enrollment.request", "worker_enrollment", enrollmentID,
			map[string]any{"worker_id": workerID, "public_key_fingerprint": fingerprint,
				"activation_public_key_fingerprint": activationFingerprint, "operating_system": strings.ToLower(req.Msg.GetOperatingSystem()),
				"architecture": strings.ToLower(req.Msg.GetArchitecture()), "version": version})
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&workerv1.RequestWorkerEnrollmentResponse{
		EnrollmentId: enrollmentID, PublicKeyFingerprint: fingerprint, State: enrollmentState,
	}), nil
}

// DecideWorkerEnrollment 仅允许管理员批准指纹已核对的外部工作进程。
func (s *WorkerServer) DecideWorkerEnrollment(ctx context.Context, req *connect.Request[workerv1.DecideWorkerEnrollmentRequest]) (*connect.Response[workerv1.DecideWorkerEnrollmentResponse], error) {
	user, err := requireUser(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	if user.GetRole() != commonv1.UserRole_USER_ROLE_ADMIN {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("worker enrollment requires administrator"))
	}
	if err := authorizeWrite(ctx, s.pool, user, "worker.enroll", "", "", false); err != nil {
		return nil, err
	}
	maxConcurrency := req.Msg.GetMaxConcurrency()
	if maxConcurrency <= 0 {
		maxConcurrency = 1
	}
	if maxConcurrency > 4 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("max_concurrency exceeds central pool limit"))
	}
	bindings, refs, err := workerBindingGrant(req.Msg.GetBindings())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := checkGrantScope(ctx, s.pool, user, refs); err != nil {
		return nil, err
	}
	assetIDs := make([]string, 0, len(refs))
	for _, ref := range refs {
		if ref != nil {
			assetIDs = append(assetIDs, ref.GetId())
		}
	}
	bindingsRaw, err := json.Marshal(bindings)
	if err != nil {
		return nil, err
	}
	response := &workerv1.DecideWorkerEnrollmentResponse{EnrollmentId: req.Msg.GetEnrollmentId(), State: "denied"}
	err = idempotentProto(ctx, s.pool, "worker.enrollment.decide:"+user.GetUserId(), idempotencyKey(req.Header()), req.Msg, response, func(tx pgx.Tx) error {
		var workerID, kind, publicKey, certificateRequest, state, activationPublicKey string
		var hostname, operatingSystem, architecture, version string
		var sandboxRaw []byte
		var requestedMax int32
		var memoryCapacity int64
		var logicalCPU int32
		if err := tx.QueryRow(ctx, `SELECT worker_id, worker_kind, public_key, certificate_request, state
			,activation_public_key,hostname,operating_system,architecture,version,sandbox_capabilities,
			max_concurrency,memory_capacity_bytes,logical_cpu_capacity
			FROM worker_enrollments WHERE enrollment_id=$1 FOR UPDATE`, req.Msg.GetEnrollmentId()).
			Scan(&workerID, &kind, &publicKey, &certificateRequest, &state, &activationPublicKey, &hostname,
				&operatingSystem, &architecture, &version, &sandboxRaw, &requestedMax, &memoryCapacity, &logicalCPU); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return connect.NewError(connect.CodeNotFound, errors.New("worker enrollment not found"))
			}
			return err
		}
		if state != "pending" {
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("worker enrollment was already decided"))
		}
		if maxConcurrency > requestedMax {
			return connect.NewError(connect.CodeInvalidArgument, errors.New("approved concurrency exceeds worker request"))
		}
		if !req.Msg.GetApproved() {
			if _, err := tx.Exec(ctx, `UPDATE worker_enrollments SET state='denied', bindings=$2::jsonb,
				max_concurrency=$3, decided_at=now(), decided_by=$4 WHERE enrollment_id=$1`,
				req.Msg.GetEnrollmentId(), bindingsRaw, maxConcurrency, user.GetUserId()); err != nil {
				return err
			}
			return appendAuditTx(ctx, tx, "user", user.GetUserId(), "worker_enrollment.decide", "worker_enrollment", req.Msg.GetEnrollmentId(),
				map[string]any{"worker_id": workerID, "state": "denied", "asset_ids": assetIDs, "max_concurrency": maxConcurrency})
		}
		if s.workloadIssuer == nil {
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("independent workload certificate issuer is not configured"))
		}
		certificate, err := s.workloadIssuer.Issue(workerID, certificateRequest, 24*time.Hour)
		if err != nil {
			return err
		}
		certificateHash, err := kernel.WorkloadCertificateSHA256(certificate.Certificate)
		if err != nil {
			return err
		}
		bootstrap, bootstrapHash, err := newSessionToken()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO worker_bootstrap(token_hash, worker_id, worker_kind, public_key, client_cert_sha256, expires_at)
			VALUES($1,$2,$3,$4,$5,$6)`, bootstrapHash, workerID, kind, publicKey, certificateHash, time.Now().Add(workerBootstrapDefaultTTL)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM grants WHERE subject_kind='worker' AND subject_id=$1`, workerID); err != nil {
			return err
		}
		grantID, err := newID("gr")
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO grants(grant_id, subject_kind, subject_id, tools, bindings, created_by)
			VALUES($1,'worker',$2,'[]',$3::jsonb,$4)`, grantID, workerID, bindingsRaw, user.GetUserId()); err != nil {
			return err
		}
		manifestDigest, err := approvedWorkerManifestDigest(workerID, kind, hostname, operatingSystem, architecture,
			version, sandboxRaw, maxConcurrency, memoryCapacity, logicalCPU, bindingsRaw)
		if err != nil {
			return err
		}
		challengeID, err := newID("sandbox-challenge")
		if err != nil {
			return err
		}
		bundleRef, err := newID("worker-activation")
		if err != nil {
			return err
		}
		expiresAt := time.Now().Add(workerActivationBundleTTL)
		plaintext, err := json.Marshal(workerActivationPackage{
			EnrollmentID: req.Msg.GetEnrollmentId(), ActivationBundleRef: bundleRef, ApprovedManifestDigest: manifestDigest,
			SandboxChallengeID: challengeID, BootstrapToken: bootstrap, ClientCertificate: certificate.Certificate,
			CertificateChain: certificate.Chain, CertificateExpiresAt: certificate.ExpiresAt.UTC().Format(time.RFC3339Nano),
		})
		if err != nil {
			return err
		}
		ciphertext, err := encryptWorkerActivation(activationPublicKey, req.Msg.GetEnrollmentId(), plaintext)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO worker_activation_bundles(bundle_ref,enrollment_id,ciphertext,manifest_digest,expires_at)
			VALUES($1,$2,$3,$4,$5)`, bundleRef, req.Msg.GetEnrollmentId(), ciphertext, manifestDigest, expiresAt); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE worker_enrollments SET state='approved', bindings=$2::jsonb,
			max_concurrency=$3, decided_at=now(), decided_by=$4, approved_manifest_digest=$5, sandbox_challenge_id=$6
			WHERE enrollment_id=$1`, req.Msg.GetEnrollmentId(), bindingsRaw, maxConcurrency, user.GetUserId(), manifestDigest, challengeID); err != nil {
			return err
		}
		response.State, response.ActivationBundleRef, response.ApprovedManifestDigest = "approved", bundleRef, manifestDigest
		response.CertificateExpiresAt = timestamppb.New(certificate.ExpiresAt)
		return appendAuditTx(ctx, tx, "user", user.GetUserId(), "worker_enrollment.decide", "worker_enrollment", req.Msg.GetEnrollmentId(),
			map[string]any{"worker_id": workerID, "state": "approved", "asset_ids": assetIDs, "max_concurrency": maxConcurrency,
				"approved_manifest_digest": manifestDigest, "certificate_expires_at": certificate.ExpiresAt.UTC().Format(time.RFC3339)})
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(response), nil
}

// ListWorkerEnrollments 返回管理员待核对的外部 worker 公钥指纹与平台能力，不返回证书请求正文。
func (s *WorkerServer) ListWorkerEnrollments(ctx context.Context, req *connect.Request[workerv1.ListWorkerEnrollmentsRequest]) (*connect.Response[workerv1.ListWorkerEnrollmentsResponse], error) {
	user, err := requireUser(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	if user.GetRole() != commonv1.UserRole_USER_ROLE_ADMIN {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("worker enrollment list requires administrator"))
	}
	if err := authorizeWrite(ctx, s.pool, user, "worker.enroll", "", "", false); err != nil {
		return nil, err
	}
	state := strings.ToLower(strings.TrimSpace(req.Msg.GetState()))
	if state != "" && state != "pending" && state != "approved" && state != "denied" && state != "expired" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("worker enrollment state is invalid"))
	}
	offset, err := decodePageOffset(req.Msg.GetPageToken())
	if err != nil {
		return nil, err
	}
	limit := ClampPageSize(req.Msg.GetPageSize())
	rows, err := s.pool.Query(ctx, `SELECT enrollment_id, worker_id, worker_kind, public_key_fingerprint, hostname,
		operating_system, architecture, sandbox_capabilities, state, bindings, max_concurrency, requested_at, decided_at,
		activation_public_key_fingerprint,approved_manifest_digest,version,memory_capacity_bytes,logical_cpu_capacity,sandbox_challenge_id
		FROM worker_enrollments WHERE ($1='' OR state=$1) ORDER BY requested_at DESC LIMIT $2 OFFSET $3`, state, limit+1, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	response := &workerv1.ListWorkerEnrollmentsResponse{}
	for rows.Next() {
		item := &workerv1.WorkerEnrollmentRecord{}
		var kind string
		var sandboxesRaw, bindingsRaw []byte
		var requested time.Time
		var decided *time.Time
		if err := rows.Scan(&item.EnrollmentId, &item.WorkerId, &kind, &item.PublicKeyFingerprint, &item.Hostname,
			&item.OperatingSystem, &item.Architecture, &sandboxesRaw, &item.State, &bindingsRaw, &item.MaxConcurrency,
			&requested, &decided, &item.ActivationPublicKeyFingerprint, &item.ApprovedManifestDigest, &item.Version,
			&item.MemoryCapacityBytes, &item.LogicalCpuCapacity, &item.SandboxChallengeId); err != nil {
			return nil, err
		}
		item.WorkerKind, _ = parseWorkerKind(kind)
		if err := json.Unmarshal(sandboxesRaw, &item.SandboxCapabilities); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(bindingsRaw, &item.Bindings); err != nil {
			return nil, err
		}
		item.RequestedAt = timestamppb.New(requested)
		if decided != nil {
			item.DecidedAt = timestamppb.New(*decided)
		}
		response.Enrollments = append(response.Enrollments, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(response.Enrollments) > limit {
		response.Enrollments = response.Enrollments[:limit]
		response.NextPageToken = encodePageOffset(offset + limit)
	}
	return connect.NewResponse(response), nil
}

type approvedWorkerManifest struct {
	WorkerID            string   `json:"worker_id"`
	WorkerKind          string   `json:"worker_kind"`
	Hostname            string   `json:"hostname"`
	OperatingSystem     string   `json:"operating_system"`
	Architecture        string   `json:"architecture"`
	Version             string   `json:"version"`
	SandboxCapabilities []string `json:"sandbox_capabilities"`
	MaxConcurrency      int32    `json:"max_concurrency"`
	MemoryCapacityBytes int64    `json:"memory_capacity_bytes"`
	LogicalCPUCapacity  int32    `json:"logical_cpu_capacity"`
	Bindings            []string `json:"bindings"`
}

func approvedWorkerManifestDigest(workerID, kind, hostname, operatingSystem, architecture, version string,
	sandboxRaw []byte, maxConcurrency int32, memoryCapacity int64, logicalCPU int32, bindingsRaw []byte) (string, error) {
	var sandboxes []string
	var bindingObjects []map[string]string
	if err := json.Unmarshal(sandboxRaw, &sandboxes); err != nil {
		return "", err
	}
	if err := json.Unmarshal(bindingsRaw, &bindingObjects); err != nil {
		return "", err
	}
	bindings := make([]string, 0, len(bindingObjects))
	for _, binding := range bindingObjects {
		bindings = append(bindings, binding["kind"]+":"+binding["id"])
	}
	slices.Sort(sandboxes)
	sandboxes = slices.Compact(sandboxes)
	slices.Sort(bindings)
	bindings = slices.Compact(bindings)
	raw, err := json.Marshal(approvedWorkerManifest{WorkerID: workerID, WorkerKind: kind, Hostname: hostname,
		OperatingSystem: operatingSystem, Architecture: architecture, Version: version, SandboxCapabilities: sandboxes,
		MaxConcurrency: maxConcurrency, MemoryCapacityBytes: memoryCapacity, LogicalCPUCapacity: logicalCPU, Bindings: bindings})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

// RenewWorkerCertificate 通过独立工作负载签发器轮换 24 小时客户端证书。
func (s *WorkerServer) RenewWorkerCertificate(ctx context.Context, req *connect.Request[workerv1.RenewWorkerCertificateRequest]) (*connect.Response[workerv1.RenewWorkerCertificateResponse], error) {
	if strings.TrimSpace(req.Msg.GetWorkerId()) == "" || strings.TrimSpace(req.Msg.GetCertificateRequest()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("worker_id and certificate_request are required"))
	}
	principal, _, err := s.authenticateWorker(ctx, req)
	if err != nil {
		return nil, err
	}
	if principal.ID != req.Msg.GetWorkerId() {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("worker_id must equal access token subject"))
	}
	if s.workloadIssuer == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("independent workload certificate issuer is not configured"))
	}
	certificate, err := s.workloadIssuer.Issue(principal.ID, req.Msg.GetCertificateRequest(), 24*time.Hour)
	if err != nil {
		return nil, err
	}
	certificateHash, err := kernel.WorkloadCertificateSHA256(certificate.Certificate)
	if err != nil {
		return nil, err
	}
	if err := withTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE worker_identities SET client_cert_sha256=$2, updated_at=now()
			WHERE worker_id=$1 AND revoked_at IS NULL`, principal.ID, certificateHash); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM worker_access_tokens WHERE worker_id=$1`, principal.ID); err != nil {
			return err
		}
		return appendAuditTx(ctx, tx, "worker", principal.ID, "worker_certificate.renew", "worker", principal.ID,
			map[string]any{"certificate_expires_at": certificate.ExpiresAt.UTC().Format(time.RFC3339)})
	}); err != nil {
		return nil, err
	}
	return connect.NewResponse(&workerv1.RenewWorkerCertificateResponse{ClientCertificate: certificate.Certificate,
		CertificateChain: certificate.Chain, ExpiresAt: timestamppb.New(certificate.ExpiresAt)}), nil
}

// ListWorkers 返回管理员可见的已登记执行池，不返回凭据或自报 Bindings。
func (s *WorkerServer) ListWorkers(ctx context.Context, req *connect.Request[workerv1.ListWorkersRequest]) (*connect.Response[workerv1.ListWorkersResponse], error) {
	user, err := requireUser(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	if user.GetRole() != commonv1.UserRole_USER_ROLE_ADMIN {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("worker list requires administrator"))
	}
	if err := authorizeWrite(ctx, s.pool, user, "worker.enroll", "", "", false); err != nil {
		return nil, err
	}
	access, err := loadEffectiveAccess(ctx, s.pool, user)
	if err != nil {
		return nil, err
	}
	scope := scopeFromAccess(access)
	offset, err := decodePageOffset(req.Msg.GetPageToken())
	if err != nil {
		return nil, err
	}
	limit := ClampPageSize(req.Msg.GetPageSize())
	rows, err := s.pool.Query(ctx, `SELECT w.worker_id, w.worker_kind, w.version, w.operating_system, w.architecture,
		w.sandbox_capabilities, w.max_concurrency, w.updated_at
		FROM workers w
		WHERE EXISTS (
			SELECT 1 FROM grants g
			CROSS JOIN LATERAL jsonb_array_elements(g.bindings) AS binding(value)
			WHERE g.subject_kind=CASE WHEN w.identity_domain='agent_compat' THEN 'agent' ELSE 'worker' END
			  AND g.subject_id=w.worker_id AND (g.expires_at IS NULL OR g.expires_at>now())
			  AND COALESCE(NULLIF(binding.value->>'kind',''),'asset')='asset'
			  AND binding.value->>'id'=ANY($1::text[])
		)
		ORDER BY w.updated_at DESC, w.worker_id LIMIT $2 OFFSET $3`, scope.assetIDs(), limit+1, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	resp := &workerv1.ListWorkersResponse{}
	for rows.Next() {
		var item workerv1.WorkerRecord
		var kind string
		var sandboxesRaw []byte
		var updated time.Time
		if err := rows.Scan(&item.WorkerId, &kind, &item.Version, &item.OperatingSystem, &item.Architecture,
			&sandboxesRaw, &item.MaxConcurrency, &updated); err != nil {
			return nil, err
		}
		item.WorkerKind, _ = parseWorkerKind(kind)
		if err := json.Unmarshal(sandboxesRaw, &item.SandboxCapabilities); err != nil {
			return nil, err
		}
		item.MissingSandboxCapabilities = missingSandboxCapabilities(item.OperatingSystem, item.SandboxCapabilities)
		item.InvestigationEligible = supportedWorkerPlatform(item.OperatingSystem, item.Architecture) && len(item.MissingSandboxCapabilities) == 0
		item.LastSeenAt = timestamppb.New(updated)
		resp.Workers = append(resp.Workers, &item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(resp.Workers) > limit {
		resp.Workers = resp.Workers[:limit]
		resp.NextPageToken = encodePageOffset(offset + limit)
	}
	return connect.NewResponse(resp), nil
}

func supportedWorkerPlatform(goos, arch string) bool {
	switch strings.ToLower(goos) + "/" + strings.ToLower(arch) {
	case "linux/amd64", "linux/arm64", "windows/amd64", "darwin/amd64", "darwin/arm64":
		return true
	default:
		return false
	}
}

func hasVerifiedSandbox(goos string, capabilities []string) bool {
	return len(missingSandboxCapabilities(goos, capabilities)) == 0
}

func missingSandboxCapabilities(goos string, capabilities []string) []string {
	have := map[string]bool{}
	for _, capability := range capabilities {
		have[strings.ToLower(capability)] = true
	}
	var required []string
	switch strings.ToLower(goos) {
	case "linux":
		required = []string{"landlock", "seccomp", "resource_limits"}
	case "windows":
		required = []string{"restricted_token", "appcontainer", "job_object"}
	case "darwin":
		required = []string{"sandbox_profile", "resource_limits"}
	default:
		return []string{"supported_platform_sandbox"}
	}
	missing := make([]string, 0, len(required))
	for _, capability := range required {
		if !have[capability] {
			missing = append(missing, capability)
		}
	}
	return missing
}
