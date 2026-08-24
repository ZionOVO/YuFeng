package brain

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	artifactv1 "yufeng/proto/gen/artifactv1"
	authv1 "yufeng/proto/gen/authv1"
	commonv1 "yufeng/proto/gen/commonv1"
	governv1 "yufeng/proto/gen/governv1"
	"yufeng/proto/gen/governv1/governv1connect"
	grantv1 "yufeng/proto/gen/grantv1"

	"yufeng/lib/kernel"
)

// GovernServer 实现发布生命周期治理。
type GovernServer struct {
	pool              *pgxpool.Pool
	signingKey        ed25519.PrivateKey
	shadowMinDuration time.Duration
	shadowMinRequests uint64
	canaryMinDuration time.Duration
	canaryMinRequests uint64
	demoTriage        bool
	artifactSigner    kernel.Signer
}

// NewGovernServer 构造治理服务。
func NewGovernServer(pool *pgxpool.Pool, key ed25519.PrivateKey, shadowMinDuration time.Duration, shadowMinRequests uint64, canaryMinDuration time.Duration, canaryMinRequests uint64) *GovernServer {
	return &GovernServer{
		pool:              pool,
		signingKey:        key,
		shadowMinDuration: shadowMinDuration,
		shadowMinRequests: shadowMinRequests,
		canaryMinDuration: canaryMinDuration,
		canaryMinRequests: canaryMinRequests,
	}
}

// Handler 返回 Connect 服务端处理器。
func (s *GovernServer) Handler() (string, http.Handler) {
	return governv1connect.NewGovernServiceHandler(s, handlerOptions()...)
}

// ProposeArtifact 从服务端可信提案意图构造草稿制品，拒绝客户端注入治理字段。
func (s *GovernServer) ProposeArtifact(ctx context.Context, req *connect.Request[governv1.ProposeArtifactRequest]) (*connect.Response[governv1.ProposeArtifactResponse], error) {
	user, err := requireUser(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	var assetIDs []string
	if req.Msg.Scope != nil {
		assetIDs = req.Msg.Scope.AssetIds
	}
	if err := authorizeWriteAssets(ctx, s.pool, user, "govern.propose", assetIDs, s.demoTriage); err != nil {
		return nil, err
	}
	if err := rejectProductionRegexProposal(s.demoTriage, req.Msg); err != nil {
		return nil, err
	}
	idem, err := requireIdempotencyKey(req.Header())
	if err != nil {
		return nil, err
	}
	rawReq, err := protojson.Marshal(req.Msg)
	if err != nil {
		return nil, err
	}
	digest := requestDigest("ProposeArtifact", string(rawReq), idem)
	hit, _, body, err := reserveIdempotency(ctx, s.pool, "govern:"+user.UserId+":ProposeArtifact", idem, digest)
	if err != nil {
		return nil, err
	}
	if hit {
		var cached governv1.ProposeArtifactResponse
		if err := protojson.Unmarshal([]byte(unwrapIdemBody(body)), &cached); err != nil {
			return nil, err
		}
		return connect.NewResponse(&cached), nil
	}
	// 归属取认证身份而非请求字段：发布归属可被审计追溯，客户端字段不可信。
	scope := "govern:" + user.UserId + ":ProposeArtifact"
	resp, err := writePropose(ctx, s.pool, user.UserId, req.Msg, func(tx pgx.Tx, out *governv1.ProposeArtifactResponse) error {
		return storeIdempotentProtoDB(ctx, tx, scope, idem, digest, out)
	})
	if err != nil {
		_ = abortIdempotency(ctx, s.pool, scope, idem)
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

// GateArtifact 校验回放报告并在通过门禁后签名制品。
func (s *GovernServer) GateArtifact(ctx context.Context, req *connect.Request[governv1.GateArtifactRequest]) (*connect.Response[governv1.GateArtifactResponse], error) {
	user, err := requireUser(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	if err := s.authorizeReleaseWrite(ctx, user, "govern.gate", req.Msg.ReleaseId); err != nil {
		return nil, err
	}
	idem, digest, cached, err := s.beginWriteIdem(ctx, user.UserId, "GateArtifact", req.Header(), req.Msg)
	if err != nil {
		return nil, err
	}
	if cached != nil {
		var out governv1.GateArtifactResponse
		if err := protojson.Unmarshal(cached, &out); err != nil {
			return nil, err
		}
		return connect.NewResponse(&out), nil
	}
	resp, err := writeGate(ctx, s.pool, s.signingKey, "user", user.UserId, req.Msg.ReleaseId, s.artifactSigner, func(tx pgx.Tx, out *governv1.GateArtifactResponse) error {
		return storeIdempotentProtoDB(ctx, tx, idemScope(user.UserId, req.Msg.ReleaseId), idem, digest, out)
	})
	if err != nil {
		s.abortWriteIdem(ctx, user.UserId, idem, req.Msg)
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

// StartShadow 把已签名发布推进到只观察、不执行处置的状态。
func (s *GovernServer) StartShadow(ctx context.Context, req *connect.Request[governv1.StartShadowRequest]) (*connect.Response[governv1.StartShadowResponse], error) {
	user, err := requireUser(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	if err := s.authorizeReleaseWrite(ctx, user, "govern.start_shadow", req.Msg.ReleaseId); err != nil {
		return nil, err
	}
	idem, digest, cached, err := s.beginWriteIdem(ctx, user.UserId, "StartShadow", req.Header(), req.Msg)
	if err != nil {
		return nil, err
	}
	if cached != nil {
		var out governv1.StartShadowResponse
		if err := protojson.Unmarshal(cached, &out); err != nil {
			return nil, err
		}
		return connect.NewResponse(&out), nil
	}
	var resp *governv1.StartShadowResponse
	_, err = writeStartShadow(ctx, s.pool, "user", user.UserId, req.Msg.ReleaseId, s.signingKey, s.artifactSigner, func(tx pgx.Tx, shadow *kernel.Shadow) error {
		view, err := loadReleaseView(ctx, tx, shadow.ReleaseID())
		if err != nil {
			return err
		}
		resp = &governv1.StartShadowResponse{Release: releaseProto(view)}
		return storeIdempotentProtoDB(ctx, tx, idemScope(user.UserId, req.Msg.ReleaseId), idem, digest, resp)
	})
	if err != nil {
		s.abortWriteIdem(ctx, user.UserId, idem, req.Msg)
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

// PromoteCanary 在样本量和风险门禁通过后把观察发布推进到小比例生效。
func (s *GovernServer) PromoteCanary(ctx context.Context, req *connect.Request[governv1.PromoteCanaryRequest]) (*connect.Response[governv1.PromoteCanaryResponse], error) {
	user, err := requireUser(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	if err := s.authorizePromote(ctx, user, "govern.promote_canary", req.Msg.ReleaseId); err != nil {
		return nil, err
	}
	idem, digest, cached, err := s.beginWriteIdem(ctx, user.UserId, "PromoteCanary", req.Header(), req.Msg)
	if err != nil {
		return nil, err
	}
	if cached != nil {
		var out governv1.PromoteCanaryResponse
		if err := protojson.Unmarshal(cached, &out); err != nil {
			return nil, err
		}
		return connect.NewResponse(&out), nil
	}
	rel, err := loadRelease(ctx, s.pool, req.Msg.ReleaseId)
	if err != nil {
		s.abortWriteIdem(ctx, user.UserId, idem, req.Msg)
		return nil, err
	}
	shadow, ok := rel.(*kernel.Shadow)
	if !ok {
		s.abortWriteIdem(ctx, user.UserId, idem, req.Msg)
		return nil, gateConflict(rel.State())
	}
	percent := req.Msg.CanaryPercent
	if percent == 0 {
		percent = kernel.CanaryPercentDefault
	}
	units, err := releaseBoundUnitCount(ctx, s.pool, req.Msg.ReleaseId)
	if err != nil {
		s.abortWriteIdem(ctx, user.UserId, idem, req.Msg)
		return nil, err
	}
	if units < kernel.CanaryMinUnits(percent) {
		s.abortWriteIdem(ctx, user.UserId, idem, req.Msg)
		return nil, canaryCohortError()
	}
	shadowStartedAt, err := releaseTime(ctx, s.pool, req.Msg.ReleaseId, "shadow_started_at")
	if err != nil {
		s.abortWriteIdem(ctx, user.UserId, idem, req.Msg)
		return nil, err
	}
	shadowAge := time.Since(shadowStartedAt)
	shadowRequests, err := sumCounters(ctx, s.pool, req.Msg.ReleaseId, "shadow")
	if err != nil {
		s.abortWriteIdem(ctx, user.UserId, idem, req.Msg)
		return nil, err
	}
	var gates []*commonv1.GateCheck
	gates = append(gates, checkGate("shadow_min_duration", durationThresholdMet(shadowAge, s.shadowMinDuration), s.shadowMinDuration.String(), shadowAge.String()))
	gates = append(gates, checkGate("shadow_min_requests", shadowRequests >= s.shadowMinRequests, strconv.FormatUint(s.shadowMinRequests, 10), strconv.FormatUint(shadowRequests, 10)))
	gates = append(gates, checkGate("replay_report", replayReportPassed(shadow.Envelope), "true", replayReportActual(shadow.Envelope)))
	if !gatesPassed(gates) {
		s.abortWriteIdem(ctx, user.UserId, idem, req.Msg)
		return nil, gateError(gates)
	}
	canary, err := shadow.PromoteCanary(percent)
	if err != nil {
		s.abortWriteIdem(ctx, user.UserId, idem, req.Msg)
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	var resp *governv1.PromoteCanaryResponse
	if err := commitReleaseChange(ctx, s.pool, releaseWrite{
		rel: canary, feed: true,
		actorType: "user", actorID: user.UserId, action: "release.promote_canary",
		details: map[string]any{"canary_percent": percent},
		key:     s.signingKey, signer: s.artifactSigner,
		complete: func(tx pgx.Tx) error {
			view, err := loadReleaseView(ctx, tx, canary.ReleaseID())
			if err != nil {
				return err
			}
			resp = &governv1.PromoteCanaryResponse{Release: releaseProto(view)}
			return storeIdempotentProtoDB(ctx, tx, idemScope(user.UserId, req.Msg.ReleaseId), idem, digest, resp)
		},
	}); err != nil {
		s.abortWriteIdem(ctx, user.UserId, idem, req.Msg)
		return nil, auditFailedError(err)
	}
	return connect.NewResponse(resp), nil
}

// PromoteEnforce 在授权与统计门禁通过后把发布推进到全量生效。
func (s *GovernServer) PromoteEnforce(ctx context.Context, req *connect.Request[governv1.PromoteEnforceRequest]) (*connect.Response[governv1.PromoteEnforceResponse], error) {
	user, err := requireUser(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	if err := s.authorizePromote(ctx, user, "govern.promote_enforce", req.Msg.ReleaseId); err != nil {
		return nil, err
	}
	idem, digest, cached, err := s.beginWriteIdem(ctx, user.UserId, "PromoteEnforce", req.Header(), req.Msg)
	if err != nil {
		return nil, err
	}
	if cached != nil {
		var out governv1.PromoteEnforceResponse
		if err := protojson.Unmarshal(cached, &out); err != nil {
			return nil, err
		}
		return connect.NewResponse(&out), nil
	}
	rel, err := loadRelease(ctx, s.pool, req.Msg.ReleaseId)
	if err != nil {
		s.abortWriteIdem(ctx, user.UserId, idem, req.Msg)
		return nil, err
	}
	var enforce *kernel.Enforce
	var supersedes string
	switch typed := rel.(type) {
	case *kernel.Shadow:
		units, err := releaseBoundUnitCount(ctx, s.pool, req.Msg.ReleaseId)
		if err != nil {
			s.abortWriteIdem(ctx, user.UserId, idem, req.Msg)
			return nil, err
		}
		if units >= kernel.CanaryMinUnits(kernel.CanaryPercentMax) {
			s.abortWriteIdem(ctx, user.UserId, idem, req.Msg)
			return nil, gateConflict(rel.State())
		}
		if !replayReportPassed(typed.Envelope) {
			s.abortWriteIdem(ctx, user.UserId, idem, req.Msg)
			return nil, gateError([]*commonv1.GateCheck{checkGate("replay_report", false, "true", replayReportActual(typed.Envelope))})
		}
		enforce = typed.PromoteEnforce()
		if typed.Envelope != nil {
			supersedes = typed.Envelope.Supersedes
		}
	case *kernel.Canary:
		canaryStartedAt, err := releaseTime(ctx, s.pool, req.Msg.ReleaseId, "canary_started_at")
		if err != nil {
			s.abortWriteIdem(ctx, user.UserId, idem, req.Msg)
			return nil, err
		}
		canaryAge := time.Since(canaryStartedAt)
		canaryRequests, err := sumCounters(ctx, s.pool, req.Msg.ReleaseId, "canary")
		if err != nil {
			s.abortWriteIdem(ctx, user.UserId, idem, req.Msg)
			return nil, err
		}
		denies, err := countDenies(ctx, s.pool, req.Msg.ReleaseId)
		if err != nil {
			s.abortWriteIdem(ctx, user.UserId, idem, req.Msg)
			return nil, err
		}
		var gates []*commonv1.GateCheck
		gates = append(gates, checkGate("canary_min_duration", durationThresholdMet(canaryAge, s.canaryMinDuration), s.canaryMinDuration.String(), canaryAge.String()))
		gates = append(gates, checkGate("canary_min_requests", canaryRequests >= s.canaryMinRequests, strconv.FormatUint(s.canaryMinRequests, 10), strconv.FormatUint(canaryRequests, 10)))
		gates = append(gates, checkGate("deny_feedback_zero", denies == 0, "0", strconv.FormatUint(denies, 10)))
		if !gatesPassed(gates) {
			s.abortWriteIdem(ctx, user.UserId, idem, req.Msg)
			return nil, gateError(gates)
		}
		enforce = typed.PromoteEnforce()
		if typed.Envelope != nil {
			supersedes = typed.Envelope.Supersedes
		}
	default:
		s.abortWriteIdem(ctx, user.UserId, idem, req.Msg)
		return nil, gateConflict(rel.State())
	}
	var resp *governv1.PromoteEnforceResponse
	if err := commitReleaseChange(ctx, s.pool, releaseWrite{
		rel: enforce, feed: true, supersedeArtifact: supersedes,
		actorType: "user", actorID: user.UserId, action: "release.promote_enforce",
		key: s.signingKey, signer: s.artifactSigner,
		complete: func(tx pgx.Tx) error {
			view, err := loadReleaseView(ctx, tx, enforce.ReleaseID())
			if err != nil {
				return err
			}
			resp = &governv1.PromoteEnforceResponse{Release: releaseProto(view)}
			return storeIdempotentProtoDB(ctx, tx, idemScope(user.UserId, req.Msg.ReleaseId), idem, digest, resp)
		},
	}); err != nil {
		s.abortWriteIdem(ctx, user.UserId, idem, req.Msg)
		return nil, auditFailedError(err)
	}
	return connect.NewResponse(resp), nil
}

// RollbackRelease 退役目标发布，并恢复该资产上一个可用世代。
func (s *GovernServer) RollbackRelease(ctx context.Context, req *connect.Request[governv1.RollbackReleaseRequest]) (*connect.Response[governv1.RollbackReleaseResponse], error) {
	user, err := requireUser(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	if err := s.authorizeReleaseWrite(ctx, user, "govern.rollback", req.Msg.ReleaseId); err != nil {
		return nil, err
	}
	idem, digest, cached, err := s.beginWriteIdem(ctx, user.UserId, "RollbackRelease", req.Header(), req.Msg)
	if err != nil {
		return nil, err
	}
	if cached != nil {
		var out governv1.RollbackReleaseResponse
		if err := protojson.Unmarshal(cached, &out); err != nil {
			return nil, err
		}
		return connect.NewResponse(&out), nil
	}
	var resp *governv1.RollbackReleaseResponse
	_, err = s.retire(ctx, req.Msg.ReleaseId, commonv1.RetireReason_RETIRE_REASON_ROLLBACK, user.UserId, "release.rollback", func(tx pgx.Tx, retired *kernel.Retired) error {
		view, err := loadReleaseView(ctx, tx, retired.ReleaseID())
		if err != nil {
			return err
		}
		resp = &governv1.RollbackReleaseResponse{Release: releaseProto(view)}
		return storeIdempotentProtoDB(ctx, tx, idemScope(user.UserId, req.Msg.ReleaseId), idem, digest, resp)
	})
	if err != nil {
		_ = abortIdempotency(ctx, s.pool, idemScope(user.UserId, req.Msg.ReleaseId), idem)
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

// RetireRelease 把指定发布推进到不可再次激活的退役终态。
func (s *GovernServer) RetireRelease(ctx context.Context, req *connect.Request[governv1.RetireReleaseRequest]) (*connect.Response[governv1.RetireReleaseResponse], error) {
	user, err := requireUser(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	if err := s.authorizeReleaseWrite(ctx, user, "govern.retire", req.Msg.ReleaseId); err != nil {
		return nil, err
	}
	idem, digest, cached, err := s.beginWriteIdem(ctx, user.UserId, "RetireRelease", req.Header(), req.Msg)
	if err != nil {
		return nil, err
	}
	if cached != nil {
		var out governv1.RetireReleaseResponse
		if err := protojson.Unmarshal(cached, &out); err != nil {
			return nil, err
		}
		return connect.NewResponse(&out), nil
	}
	var resp *governv1.RetireReleaseResponse
	_, err = s.retire(ctx, req.Msg.ReleaseId, commonv1.RetireReason_RETIRE_REASON_MANUAL, user.UserId, "release.retire", func(tx pgx.Tx, retired *kernel.Retired) error {
		view, err := loadReleaseView(ctx, tx, retired.ReleaseID())
		if err != nil {
			return err
		}
		resp = &governv1.RetireReleaseResponse{Release: releaseProto(view)}
		return storeIdempotentProtoDB(ctx, tx, idemScope(user.UserId, req.Msg.ReleaseId), idem, digest, resp)
	})
	if err != nil {
		_ = abortIdempotency(ctx, s.pool, idemScope(user.UserId, req.Msg.ReleaseId), idem)
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

// DenyFeedback 记录人工拒绝提案的原因，供治理审计与后续研判使用。
func (s *GovernServer) DenyFeedback(ctx context.Context, req *connect.Request[governv1.DenyFeedbackRequest]) (*connect.Response[governv1.DenyFeedbackResponse], error) {
	user, err := requireUser(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	if err := s.authorizeReleaseWrite(ctx, user, "govern.deny_feedback", req.Msg.ReleaseId); err != nil {
		return nil, err
	}
	idem, digest, cached, err := s.beginWriteIdem(ctx, user.UserId, "DenyFeedback", req.Header(), req.Msg)
	if err != nil {
		return nil, err
	}
	if cached != nil {
		var out governv1.DenyFeedbackResponse
		if err := protojson.Unmarshal(cached, &out); err != nil {
			return nil, err
		}
		return connect.NewResponse(&out), nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		s.abortWriteIdem(ctx, user.UserId, idem, req.Msg)
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var state string
	if err := tx.QueryRow(ctx, `SELECT state FROM releases WHERE release_id=$1 FOR UPDATE`, req.Msg.ReleaseId).Scan(&state); err != nil {
		s.abortWriteIdem(ctx, user.UserId, idem, req.Msg)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("release not found"))
		}
		return nil, err
	}
	if state != "canary" && state != "enforce" {
		s.abortWriteIdem(ctx, user.UserId, idem, req.Msg)
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("release is not receiving traffic"))
	}
	if err := ensureGuardBaseline(ctx, tx, req.Msg.ReleaseId); err != nil {
		s.abortWriteIdem(ctx, user.UserId, idem, req.Msg)
		return nil, err
	}
	if err := func() error {
		if _, err := tx.Exec(ctx, `INSERT INTO deny_feedback(release_id, event_id, actor, note) VALUES($1,$2,$3,$4)
	ON CONFLICT DO NOTHING`, req.Msg.ReleaseId, req.Msg.EventId, user.UserId, req.Msg.Note); err != nil {
			return err
		}
		return appendAuditTx(ctx, tx, "user", user.UserId, "release.deny_feedback", "release", req.Msg.ReleaseId, map[string]any{"event_id": req.Msg.EventId})
	}(); err != nil {
		_ = abortIdempotency(ctx, s.pool, idemScope(user.UserId, req.Msg.ReleaseId), idem)
		return nil, err
	}
	view, err := loadReleaseView(ctx, tx, req.Msg.ReleaseId)
	if err != nil {
		_ = abortIdempotency(ctx, s.pool, idemScope(user.UserId, req.Msg.ReleaseId), idem)
		return nil, err
	}
	resp := &governv1.DenyFeedbackResponse{Release: releaseProto(view)}
	if err := storeIdempotentProtoDB(ctx, tx, idemScope(user.UserId, req.Msg.ReleaseId), idem, digest, resp); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

// GetRelease 返回调用者有权查看的单个发布及其制品信息。
func (s *GovernServer) GetRelease(ctx context.Context, req *connect.Request[governv1.GetReleaseRequest]) (*connect.Response[governv1.GetReleaseResponse], error) {
	user, err := requireUser(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	if err := requireCompletedOnboarding(ctx, s.pool); err != nil {
		return nil, err
	}
	access, err := loadEffectiveAccess(ctx, s.pool, user)
	if err != nil {
		return nil, err
	}
	view, err := s.authorizedReleaseView(ctx, access, req.Msg.ReleaseId)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&governv1.GetReleaseResponse{Release: releaseProto(view)}), nil
}

// ListReleases 按授权资产范围和查询条件分页列出治理发布。
func (s *GovernServer) ListReleases(ctx context.Context, req *connect.Request[governv1.ListReleasesRequest]) (*connect.Response[governv1.ListReleasesResponse], error) {
	user, err := requireUser(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	if err := requireCompletedOnboarding(ctx, s.pool); err != nil {
		return nil, err
	}
	access, err := loadEffectiveAccess(ctx, s.pool, user)
	if err != nil {
		return nil, err
	}
	scope := scopeFromAccess(access)
	if !scope.hasTool("console.read") || scope.emptyObjects() {
		return connect.NewResponse(&governv1.ListReleasesResponse{}), nil
	}
	if req.Msg.GetAssetId() != "" && !scope.coversAsset(req.Msg.GetAssetId()) {
		return nil, objectDenied()
	}
	limit := ClampPageSize(req.Msg.GetPageSize())
	offset, err := decodePageOffset(req.Msg.GetPageToken())
	if err != nil {
		return nil, err
	}
	stateFilter := releaseStateFilter(req.Msg.GetStates())
	query := strings.TrimSpace(req.Msg.GetQuery())
	rows, err := s.pool.Query(ctx, `SELECT r.release_id, r.state, r.artifact, r.canary_percent, r.retire_reason,
		r.created_by, r.proposed_at, r.signed_at, r.shadow_started_at, r.canary_started_at, r.enforced_at, r.retired_at
	FROM releases r
	WHERE (r.release_id = ANY($4)
	    OR (
	      EXISTS (SELECT 1 FROM release_assets ra0 WHERE ra0.release_id=r.release_id)
	      AND NOT EXISTS (
	        SELECT 1 FROM release_assets ra
	        WHERE ra.release_id=r.release_id AND NOT (ra.asset_id = ANY($3))
	      )
	    ))
	  AND ($5='' OR EXISTS (SELECT 1 FROM release_assets ra2 WHERE ra2.release_id=r.release_id AND ra2.asset_id=$5))
	  AND (cardinality($6::text[])=0 OR r.state = ANY($6))
	  AND ($7='' OR r.release_id ILIKE '%'||$7||'%' OR COALESCE(r.created_by,'') ILIKE '%'||$7||'%')
	ORDER BY r.proposed_at DESC LIMIT $1 OFFSET $2`, limit+1, offset, scope.assetIDs(), mapKeys(scope.releases), req.Msg.GetAssetId(), stateFilter, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	resp := &governv1.ListReleasesResponse{}
	for rows.Next() {
		view, err := scanReleaseView(rows)
		if err != nil {
			return nil, err
		}
		resp.Releases = append(resp.Releases, releaseProto(view))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(resp.Releases) > limit {
		resp.Releases = resp.Releases[:limit]
		resp.NextPageToken = encodePageOffset(offset + limit)
	}
	return connect.NewResponse(resp), nil
}

// GetReleaseTimeline 按时间顺序返回指定发布的状态转换记录。
func (s *GovernServer) GetReleaseTimeline(ctx context.Context, req *connect.Request[governv1.GetReleaseTimelineRequest]) (*connect.Response[governv1.GetReleaseTimelineResponse], error) {
	user, err := requireUser(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	if err := requireCompletedOnboarding(ctx, s.pool); err != nil {
		return nil, err
	}
	access, err := loadEffectiveAccess(ctx, s.pool, user)
	if err != nil {
		return nil, err
	}
	if _, err := s.authorizedReleaseView(ctx, access, req.Msg.ReleaseId); err != nil {
		return nil, err
	}
	limit := ClampPageSize(req.Msg.GetPageSize())
	offset, err := decodePageOffset(req.Msg.GetPageToken())
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT sequence, release_id, from_state, to_state, actor, reason, gate_report_ref, occurred_at
	FROM release_timeline WHERE release_id=$1 ORDER BY sequence LIMIT $2 OFFSET $3`, req.Msg.ReleaseId, limit+1, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	resp := &governv1.GetReleaseTimelineResponse{}
	for rows.Next() {
		var e governv1.TimelineEntry
		var fromState, toState string
		var at time.Time
		if err := rows.Scan(&e.Sequence, &e.ReleaseId, &fromState, &toState, &e.Actor, &e.Reason, &e.GateReportRef, &at); err != nil {
			return nil, err
		}
		e.FromState = releaseStateEnum(fromState)
		e.ToState = releaseStateEnum(toState)
		e.OccurredAt = timestamppb.New(at)
		resp.Entries = append(resp.Entries, &e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(resp.Entries) > limit {
		resp.Entries = resp.Entries[:limit]
		resp.NextPageToken = encodePageOffset(offset + limit)
	}
	return connect.NewResponse(resp), nil
}

// GetReleaseStats 汇总指定发布的观察窗口、拦截量、误报与延迟统计。
func (s *GovernServer) GetReleaseStats(ctx context.Context, req *connect.Request[governv1.GetReleaseStatsRequest]) (*connect.Response[governv1.GetReleaseStatsResponse], error) {
	user, err := requireUser(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	if err := requireCompletedOnboarding(ctx, s.pool); err != nil {
		return nil, err
	}
	access, err := loadEffectiveAccess(ctx, s.pool, user)
	if err != nil {
		return nil, err
	}
	view, err := s.authorizedReleaseView(ctx, access, req.Msg.ReleaseId)
	if err != nil {
		return nil, err
	}
	rel := view.rel
	resp := &governv1.GetReleaseStatsResponse{ReleaseId: req.Msg.ReleaseId, State: rel.State(), ComputedAt: timestamppb.Now()}
	fetch := func(mode string) (*governv1.ReleaseWindowStats, error) {
		stats, err := s.windowStats(ctx, req.Msg.ReleaseId, mode)
		if err != nil {
			return nil, err
		}
		return stats, nil
	}
	switch rel.State() {
	case commonv1.ReleaseState_RELEASE_STATE_SHADOW:
		if resp.Shadow, err = fetch("shadow"); err != nil {
			return nil, err
		}
	case commonv1.ReleaseState_RELEASE_STATE_CANARY:
		if resp.Shadow, err = fetch("shadow"); err != nil {
			return nil, err
		}
		if resp.Canary, err = fetch("canary"); err != nil {
			return nil, err
		}
	case commonv1.ReleaseState_RELEASE_STATE_ENFORCE:
		if resp.Enforce, err = fetch("enforce"); err != nil {
			return nil, err
		}
	}
	return connect.NewResponse(resp), nil
}

func (s *GovernServer) authorizedReleaseView(ctx context.Context, access *grantv1.EffectiveAccess, releaseID string) (releaseView, error) {
	view, err := loadReleaseView(ctx, s.pool, releaseID)
	if err != nil {
		if connect.CodeOf(err) == connect.CodeNotFound {
			return releaseView{}, objectDenied()
		}
		return releaseView{}, err
	}
	ids, err := releaseAssetIDs(ctx, s.pool, view.rel.ReleaseID())
	if err != nil {
		return releaseView{}, err
	}
	if !scopeFromAccess(access).coversRelease(view.rel.ReleaseID(), ids) {
		return releaseView{}, objectDenied()
	}
	return view, nil
}

func (s *GovernServer) retire(ctx context.Context, releaseID string, reason commonv1.RetireReason, actorID, action string, complete func(pgx.Tx, *kernel.Retired) error) (*kernel.Retired, error) {
	rel, err := loadRelease(ctx, s.pool, releaseID)
	if err != nil {
		return nil, err
	}
	if _, err := kernel.ActiveOf(rel); err != nil {
		return nil, gateConflict(rel.State())
	}
	return retireReleaseAudit(ctx, s.pool, releaseID, reason, s.signingKey, s.artifactSigner, "user", actorID, action, map[string]any{"reason": reason.String()}, complete)
}

// windowStats 汇总一个发布在指定生效模式下的窗口计数。
// 查询失败必须上抛：门禁与控制台都消费本数据，静默清零会让
// deny_feedback_zero 之类的门禁在数据库故障时"通过"。
func (s *GovernServer) windowStats(ctx context.Context, releaseID, mode string) (*governv1.ReleaseWindowStats, error) {
	var req, blocks, obs, selected, five, lat, samples, p99 uint64
	if err := s.pool.QueryRow(ctx, `SELECT COALESCE(SUM(requests_total),0), COALESCE(SUM(blocks_total),0),
	 COALESCE(SUM(observe_total),0), COALESCE(SUM(canary_selected_total),0),
	 COALESCE(SUM(upstream_5xx_total),0), COALESCE(SUM(latency_micros_total),0),
	 COALESCE(SUM(latency_samples),0), COALESCE(MAX(latency_p99_micros),0)
	FROM release_counters WHERE release_id=$1 AND mode=$2`, releaseID, mode).
		Scan(&req, &blocks, &obs, &selected, &five, &lat, &samples, &p99); err != nil {
		return nil, err
	}
	return &governv1.ReleaseWindowStats{Requests: req, Blocks: blocks, Observes: obs, CanarySelected: selected, Upstream_5Xx: five, P99Micros: p99}, nil
}

func loadRelease(ctx context.Context, db dbTX, releaseID string) (kernel.Release, error) {
	row := db.QueryRow(ctx, `SELECT release_id, state, artifact, canary_percent, retire_reason
	FROM releases WHERE release_id=$1`, releaseID)
	rel, err := scanRelease(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("release not found"))
	}
	return rel, err
}

func loadDraft(ctx context.Context, pool *pgxpool.Pool, releaseID string) (*kernel.Draft, error) {
	rel, err := loadRelease(ctx, pool, releaseID)
	if err != nil {
		return nil, err
	}
	d, ok := rel.(*kernel.Draft)
	if !ok {
		return nil, gateConflict(rel.State())
	}
	return d, nil
}

func scanRelease(row pgx.Row) (kernel.Release, error) {
	var id, state string
	var raw []byte
	var canaryPercent int32
	var retireReason string
	if err := row.Scan(&id, &state, &raw, &canaryPercent, &retireReason); err != nil {
		return nil, err
	}
	return releaseFromParts(id, state, raw, canaryPercent, retireReason)
}

func releaseFromParts(id, state string, raw []byte, canaryPercent int32, retireReason string) (kernel.Release, error) {
	var a artifactv1.Artifact
	if len(raw) > 0 {
		if err := protojson.Unmarshal(raw, &a); err != nil {
			return nil, err
		}
	}
	switch state {
	case "draft":
		return &kernel.Draft{ID: id, Envelope: &a, CreatedBy: a.CreatedBy}, nil
	case "signed":
		return &kernel.Signed{ID: id, Envelope: &a}, nil
	case "shadow":
		return &kernel.Shadow{ID: id, Envelope: &a}, nil
	case "canary":
		return &kernel.Canary{ID: id, Envelope: &a, CanaryPercent: canaryPercent}, nil
	case "enforce":
		return &kernel.Enforce{ID: id, Envelope: &a}, nil
	case "retired":
		return &kernel.Retired{ID: id, Envelope: &a, Reason: retireReasonEnum(retireReason)}, nil
	default:
		return nil, fmt.Errorf("unknown release state %q", state)
	}
}

func persistRelease(ctx context.Context, db dbTX, rel kernel.Release) error {
	raw, err := protojson.Marshal(rel.Artifact())
	if err != nil {
		return err
	}
	state := releaseStateString(rel.State())
	// timeline 的 from_state 取更新前的真实状态：发布历史是审计数据，
	// 恒写 'draft' 会把 draft→shadow→canary 的轨迹记成多条 draft→X。
	var fromState string
	if err := db.QueryRow(ctx, `SELECT state FROM releases WHERE release_id=$1 FOR UPDATE`, rel.ReleaseID()).Scan(&fromState); err != nil {
		return err
	}
	if !releaseTransitionAllowed(fromState, state) {
		return connect.NewError(connect.CodeAborted, fmt.Errorf("release state changed from expected predecessor: %s to %s", fromState, state))
	}
	switch r := rel.(type) {
	case *kernel.Draft:
		_, err = db.Exec(ctx, `UPDATE releases SET state=$1, artifact=$2::jsonb, updated_at=now() WHERE release_id=$3`, state, string(raw), r.ID)
	case *kernel.Signed:
		_, err = db.Exec(ctx, `UPDATE releases SET state=$1, artifact_id=$2, artifact=$3::jsonb, signed_at=now(), updated_at=now() WHERE release_id=$4`, state, r.Envelope.Id, string(raw), r.ID)
	case *kernel.Shadow:
		_, err = db.Exec(ctx, `UPDATE releases SET state=$1, artifact_id=$2, artifact=$3::jsonb, shadow_started_at=COALESCE(shadow_started_at,now()), updated_at=now() WHERE release_id=$4`, state, r.Envelope.Id, string(raw), r.ID)
	case *kernel.Canary:
		_, err = db.Exec(ctx, `UPDATE releases SET state=$1, artifact_id=$2, artifact=$3::jsonb, canary_percent=$4, canary_started_at=COALESCE(canary_started_at,now()), updated_at=now() WHERE release_id=$5`, state, r.Envelope.Id, string(raw), r.CanaryPercent, r.ID)
		if err == nil {
			err = ensureGuardBaseline(ctx, db, r.ID)
		}
	case *kernel.Enforce:
		_, err = db.Exec(ctx, `UPDATE releases SET state=$1, artifact_id=$2, artifact=$3::jsonb, enforced_at=COALESCE(enforced_at,now()), updated_at=now() WHERE release_id=$4`, state, r.Envelope.Id, string(raw), r.ID)
		if err == nil {
			err = ensureGuardBaseline(ctx, db, r.ID)
		}
	case *kernel.Retired:
		reason, reasonErr := retireReasonString(r.Reason)
		if reasonErr != nil {
			return reasonErr
		}
		_, err = db.Exec(ctx, `UPDATE releases SET state=$1, artifact=$2::jsonb, retired_at=now(), retire_reason=$3, updated_at=now() WHERE release_id=$4`, state, string(raw), reason, r.ID)
	default:
		err = errors.New("unknown release type")
	}
	if err != nil {
		return err
	}
	// NOT EXISTS 去重保证同一状态转换只记一次（重复投递幂等）。
	_, err = db.Exec(ctx, `INSERT INTO release_timeline(release_id, from_state, to_state, actor, reason)
	SELECT release_id, $1, $2, 'system', '' FROM releases WHERE release_id=$3 AND NOT EXISTS (
	  SELECT 1 FROM release_timeline WHERE release_id=$3 AND to_state=$2)`, fromState, state, rel.ReleaseID())
	return err
}

func releaseTransitionAllowed(from, to string) bool {
	if from == to {
		return true
	}
	switch to {
	case "signed":
		return from == "draft"
	case "shadow":
		return from == "signed"
	case "canary":
		return from == "shadow"
	case "enforce":
		return from == "shadow" || from == "canary"
	case "retired":
		return from != "retired"
	default:
		return false
	}
}

// backfillUnitLiveFeed 在单元刚绑上资产时补写该资产已有在役发布。
// 发布早于绑定（先签发基线再 Register）时，按资产 JOIN 单元的写入当时是空的。
func backfillUnitLiveFeed(ctx context.Context, db dbTX, unitID, assetID string) error {
	_, err := db.Exec(ctx, `INSERT INTO release_feed(unit_id, asset_id, release_id, mode, canary_percent, retired, retire_reason, artifact)
	SELECT $1, ra.asset_id, r.release_id, r.state, COALESCE(r.canary_percent,0), false, '', r.artifact
	FROM releases r
	JOIN release_assets ra ON ra.release_id = r.release_id
	WHERE ra.asset_id=$2 AND r.state IN ('shadow','canary','enforce')
	  AND NOT EXISTS (
	    SELECT 1 FROM release_feed f
	    WHERE f.unit_id=$1 AND f.release_id=r.release_id AND f.mode=r.state AND NOT f.retired
	  )`, unitID, assetID)
	return err
}

func publishFeed(ctx context.Context, db dbTX, rel kernel.Release, retired bool, reason commonv1.RetireReason) error {
	raw, err := protojson.Marshal(rel.Artifact())
	if err != nil {
		return err
	}
	mode := releaseModeForState(rel.State())
	if retired {
		mode = "retired"
	}
	reasonStr, _ := retireReasonString(reason)
	_, err = db.Exec(ctx, `INSERT INTO release_feed(unit_id, asset_id, release_id, mode, canary_percent, retired, retire_reason, artifact)
	SELECT ua.unit_id, ua.asset_id, $1, $2, $3, $4, $5, $6::jsonb
	FROM unit_assets ua JOIN release_assets ra ON ra.asset_id = ua.asset_id
	WHERE ra.release_id=$1`, rel.ReleaseID(), mode, canaryPercent(rel), retired, reasonStr, string(raw))
	return err
}

func supersede(ctx context.Context, db dbTX, oldArtifactID, actor string) error {
	var oldReleaseID, oldState string
	err := db.QueryRow(ctx, `SELECT release_id, state FROM releases WHERE artifact_id=$1 AND state IN ('shadow','canary','enforce') ORDER BY updated_at DESC LIMIT 1`, oldArtifactID).Scan(&oldReleaseID, &oldState)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err = db.Exec(ctx, `UPDATE releases SET state='retired', retired_at=now(), retire_reason='superseded', updated_at=now() WHERE release_id=$1`, oldReleaseID); err != nil {
		return err
	}
	_, err = db.Exec(ctx, `INSERT INTO release_timeline(release_id, from_state, to_state, actor, reason) VALUES($1,$2,'retired',$3,'superseded')`, oldReleaseID, oldState, actor)
	if err != nil {
		return err
	}
	rel, err := loadRelease(ctx, db, oldReleaseID)
	if err != nil {
		return err
	}
	return publishFeed(ctx, db, rel, true, commonv1.RetireReason_RETIRE_REASON_SUPERSEDED)
}

// releaseWrite 是一次治理写的事务载荷。
type releaseWrite struct {
	rel               kernel.Release
	feed              bool
	retired           bool
	reason            commonv1.RetireReason
	supersedeArtifact string
	actorType         string
	actorID           string
	action            string
	details           map[string]any
	key               ed25519.PrivateKey
	signer            kernel.Signer
	complete          func(pgx.Tx) error
}

// commitReleaseChange 把状态、timeline、feed 与审计放进同一事务。
func commitReleaseChange(ctx context.Context, pool *pgxpool.Pool, w releaseWrite) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := persistRelease(ctx, tx, w.rel); err != nil {
		return err
	}
	if w.feed {
		if err := publishFeed(ctx, tx, w.rel, w.retired, w.reason); err != nil {
			return err
		}
	}
	if w.supersedeArtifact != "" {
		if err := supersede(ctx, tx, w.supersedeArtifact, w.actorID); err != nil {
			return err
		}
	}
	if w.action != "" {
		if err := appendAuditTx(ctx, tx, w.actorType, w.actorID, w.action, "release", w.rel.ReleaseID(), w.details); err != nil {
			return err
		}
	}
	if w.feed {
		ids, err := releaseAssetIDsTx(ctx, tx, w.rel.ReleaseID())
		if err != nil {
			return err
		}
		for _, id := range ids {
			rollback := w.retired && w.reason == commonv1.RetireReason_RETIRE_REASON_ROLLBACK
			if err := publishAssetGeneration(ctx, tx, id, w.key, w.signer, rollback); err != nil {
				return err
			}
		}
	}
	if w.complete != nil {
		if err := w.complete(tx); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func releaseAssetIDsTx(ctx context.Context, db dbTX, releaseID string) ([]string, error) {
	rows, err := db.Query(ctx, `SELECT asset_id FROM release_assets WHERE release_id=$1`, releaseID)
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
		if id != "" {
			ids = append(ids, id)
		}
	}
	return ids, rows.Err()
}

func publishAssetGeneration(ctx context.Context, db dbTX, assetID string, key ed25519.PrivateKey, signer kernel.Signer, rollback bool) error {
	if assetID == "" || (len(key) == 0 && signer == nil) {
		return nil
	}
	if _, err := db.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, assetID); err != nil {
		return err
	}
	rows, err := db.Query(ctx, `SELECT r.release_id, r.artifact, r.state, COALESCE(r.canary_percent,0)
		FROM releases r JOIN release_assets ra ON ra.release_id=r.release_id
		WHERE ra.asset_id=$1 AND r.state IN ('shadow','canary','enforce')
		ORDER BY r.release_id`, assetID)
	if err != nil {
		return err
	}
	var members []*artifactv1.ReleaseItem
	for rows.Next() {
		var id, raw, state string
		var percent int32
		if err := rows.Scan(&id, &raw, &state, &percent); err != nil {
			return err
		}
		var a artifactv1.Artifact
		if err := protojson.Unmarshal([]byte(raw), &a); err != nil {
			return err
		}
		members = append(members, &artifactv1.ReleaseItem{
			ReleaseId: id, Artifact: &a, AssetId: assetID,
			Mode: releaseModeEnum(state), CanaryPercent: percent,
		})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	settings, err := db.Query(ctx, `SELECT kind,payload FROM asset_generation_settings WHERE asset_id=$1 ORDER BY kind`, assetID)
	if err != nil {
		return err
	}
	for settings.Next() {
		var kind string
		var raw []byte
		if err := settings.Scan(&kind, &raw); err != nil {
			settings.Close()
			return err
		}
		var artifact artifactv1.Artifact
		if err := protojson.Unmarshal(raw, &artifact); err != nil {
			settings.Close()
			return err
		}
		members = append(members, &artifactv1.ReleaseItem{
			ReleaseId: "setting:" + kind, Artifact: &artifact, AssetId: assetID,
			Mode: commonv1.ReleaseMode_RELEASE_MODE_ENFORCE,
		})
	}
	if err := settings.Err(); err != nil {
		settings.Close()
		return err
	}
	settings.Close()
	var seq int64
	var parent string
	if err := db.QueryRow(ctx, `SELECT generation_seq, generation_id FROM asset_generations WHERE asset_id=$1
		ORDER BY generation_seq DESC LIMIT 1`, assetID).Scan(&seq, &parent); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	genID, err := newID("gen")
	if err != nil {
		return err
	}
	gen := &artifactv1.AssetGeneration{
		GenerationId: genID, AssetId: assetID, GenerationSeq: seq + 1, ParentGenerationId: parent,
		Members: members, MinEdgeVersion: kernel.MinimumEdgeVersion, NotBefore: timestamppb.Now(),
	}
	if rollback {
		gen.RollbackOf = seq
	}
	if err := signGenerationEnvelope(gen, key, signer); err != nil {
		return err
	}
	env, err := protoJSON(gen)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, `INSERT INTO asset_generations(generation_id, asset_id, generation_seq, envelope, signed)
		VALUES($1,$2,$3,$4::jsonb,true)`, gen.GenerationId, assetID, gen.GenerationSeq, env)
	return err
}

type releaseView struct {
	rel        kernel.Release
	createdBy  string
	proposedAt time.Time
	signedAt   *time.Time
	shadowAt   *time.Time
	canaryAt   *time.Time
	enforceAt  *time.Time
	retiredAt  *time.Time
}

func releaseProto(v releaseView) *governv1.Release {
	if v.rel == nil {
		return &governv1.Release{}
	}
	out := &governv1.Release{
		ReleaseId: v.rel.ReleaseID(),
		State:     v.rel.State(),
		Artifact:  v.rel.Artifact(),
		CreatedBy: v.createdBy,
	}
	if !v.proposedAt.IsZero() {
		out.ProposedAt = timestamppb.New(v.proposedAt)
	}
	if v.signedAt != nil {
		out.SignedAt = timestamppb.New(*v.signedAt)
	}
	if v.shadowAt != nil {
		out.ShadowStartedAt = timestamppb.New(*v.shadowAt)
	}
	if v.canaryAt != nil {
		out.CanaryStartedAt = timestamppb.New(*v.canaryAt)
	}
	if v.enforceAt != nil {
		out.EnforcedAt = timestamppb.New(*v.enforceAt)
	}
	if v.retiredAt != nil {
		out.RetiredAt = timestamppb.New(*v.retiredAt)
	}
	if r, ok := v.rel.(*kernel.Retired); ok {
		out.RetireReason = r.Reason
	}
	return out
}

func loadReleaseView(ctx context.Context, db dbTX, releaseID string) (releaseView, error) {
	row := db.QueryRow(ctx, `SELECT release_id, state, artifact, canary_percent, retire_reason,
		created_by, proposed_at, signed_at, shadow_started_at, canary_started_at, enforced_at, retired_at
		FROM releases WHERE release_id=$1`, releaseID)
	view, err := scanReleaseView(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return releaseView{}, connect.NewError(connect.CodeNotFound, errors.New("release not found"))
	}
	return view, err
}

func scanReleaseView(row pgx.Row) (releaseView, error) {
	var id, state, createdBy string
	var raw []byte
	var canaryPercent int32
	var retireReason string
	var proposed time.Time
	var signed, shadow, canary, enforce, retired *time.Time
	if err := row.Scan(&id, &state, &raw, &canaryPercent, &retireReason, &createdBy, &proposed, &signed, &shadow, &canary, &enforce, &retired); err != nil {
		return releaseView{}, err
	}
	rel, err := releaseFromParts(id, state, raw, canaryPercent, retireReason)
	if err != nil {
		return releaseView{}, err
	}
	return releaseView{
		rel: rel, createdBy: createdBy, proposedAt: proposed,
		signedAt: signed, shadowAt: shadow, canaryAt: canary, enforceAt: enforce, retiredAt: retired,
	}, nil
}

func checkGate(key string, passed bool, required, actual string) *commonv1.GateCheck {
	return &commonv1.GateCheck{GateKey: key, Passed: passed, Required: required, Actual: actual}
}

func gatesPassed(gates []*commonv1.GateCheck) bool {
	for _, g := range gates {
		if !g.Passed {
			return false
		}
	}
	return true
}

func gateError(gates []*commonv1.GateCheck) error {
	connErr := connect.NewError(connect.CodeFailedPrecondition, errors.New("promotion gates not satisfied"))
	if detail, err := connect.NewErrorDetail(&commonv1.GateResult{Gates: gates}); err == nil {
		connErr.AddDetail(detail)
	}
	return connErr
}

func gateConflict(state commonv1.ReleaseState) error {
	return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("release_state_conflict: release state %s does not allow this transition", state))
}

func canaryCohortError() error {
	return connect.NewError(connect.CodeFailedPrecondition, errors.New("canary_cohort_too_small"))
}

func replayReportPassed(a *artifactv1.Artifact) bool {
	if a == nil || a.GetReplayReport() == nil {
		return false
	}
	r := a.GetReplayReport()
	return r.Passed && kernel.GatePassed(r)
}

func replayReportActual(a *artifactv1.Artifact) string {
	if a == nil || a.GetReplayReport() == nil {
		return "missing"
	}
	if a.ReplayReport.Passed {
		return "true"
	}
	return "false"
}

func releaseBoundUnitCount(ctx context.Context, pool *pgxpool.Pool, releaseID string) (int, error) {
	ids, err := releaseAssetIDs(ctx, pool, releaseID)
	if err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	var n int
	err = pool.QueryRow(ctx, `SELECT count(DISTINCT unit_id) FROM unit_assets WHERE asset_id = ANY($1)`, ids).Scan(&n)
	return n, err
}

// rejectProductionRegexProposal 生产路径硬拒无 intent 或 KIND_RULE / rules/v1。
func rejectProductionRegexProposal(demo bool, msg *governv1.ProposeArtifactRequest) error {
	if demo {
		return nil
	}
	if msg.GetKind() == artifactv1.Kind_KIND_RULE || msg.GetPayloadSchema() == "rules/v1" {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("production rejects KIND_RULE and rules/v1"))
	}
	if msg.GetIntent() == nil {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("production requires proposal intent"))
	}
	return nil
}

// auditFailedError 把审计追加失败转成明确的内部错误：动作可能已落库，
// 但账本缺凭据必须让调用方看见；状态与账本写入必须保持事务原子性。
func auditFailedError(err error) error {
	return connect.NewError(connect.CodeInternal, fmt.Errorf("action persisted but audit append failed: %w", err))
}

// sumCounters 汇总发布在指定模式下的请求数。失败上抛：门禁依据此值判定。
func sumCounters(ctx context.Context, pool *pgxpool.Pool, releaseID, mode string) (uint64, error) {
	var sum int64
	err := pool.QueryRow(ctx, `SELECT COALESCE(SUM(requests_total),0) FROM release_counters WHERE release_id=$1 AND mode=$2`, releaseID, mode).Scan(&sum)
	return uint64(sum), err
}

// countDenies 统计误拦举报数。失败上抛：deny_feedback_zero 门禁依据此值判定。
func countDenies(ctx context.Context, pool *pgxpool.Pool, releaseID string) (uint64, error) {
	var n int64
	err := pool.QueryRow(ctx, `SELECT count(*) FROM deny_feedback WHERE release_id=$1`, releaseID).Scan(&n)
	return uint64(n), err
}

// releaseTimeColumns 是状态时间列白名单：列名无法参数化，禁止拼接。
var releaseTimeColumns = map[string]bool{
	"signed_at":         true,
	"shadow_started_at": true,
	"canary_started_at": true,
	"enforced_at":       true,
}

// releaseTime 返回发布进入某状态的时间；列为空（刚进入该状态）取当前时刻，
// 让时长门禁从零起算而不是报错。
func releaseTime(ctx context.Context, pool *pgxpool.Pool, releaseID, column string) (time.Time, error) {
	if !releaseTimeColumns[column] {
		return time.Time{}, fmt.Errorf("unknown release time column %q", column)
	}
	var t time.Time
	err := pool.QueryRow(ctx, `SELECT COALESCE(`+column+`, now()) FROM releases WHERE release_id=$1`, releaseID).Scan(&t)
	return t, err
}

func releaseStateEnum(s string) commonv1.ReleaseState {
	switch s {
	case "signed":
		return commonv1.ReleaseState_RELEASE_STATE_SIGNED
	case "shadow":
		return commonv1.ReleaseState_RELEASE_STATE_SHADOW
	case "canary":
		return commonv1.ReleaseState_RELEASE_STATE_CANARY
	case "enforce":
		return commonv1.ReleaseState_RELEASE_STATE_ENFORCE
	case "retired":
		return commonv1.ReleaseState_RELEASE_STATE_RETIRED
	default:
		return commonv1.ReleaseState_RELEASE_STATE_DRAFT
	}
}

func releaseStateFilter(states []commonv1.ReleaseState) []string {
	var out []string
	for _, st := range states {
		if st == commonv1.ReleaseState_RELEASE_STATE_UNSPECIFIED {
			continue
		}
		out = append(out, releaseStateString(st))
	}
	if out == nil {
		return []string{}
	}
	return out
}

func releaseStateString(s commonv1.ReleaseState) string {
	switch s {
	case commonv1.ReleaseState_RELEASE_STATE_DRAFT:
		return "draft"
	case commonv1.ReleaseState_RELEASE_STATE_SIGNED:
		return "signed"
	case commonv1.ReleaseState_RELEASE_STATE_SHADOW:
		return "shadow"
	case commonv1.ReleaseState_RELEASE_STATE_CANARY:
		return "canary"
	case commonv1.ReleaseState_RELEASE_STATE_ENFORCE:
		return "enforce"
	case commonv1.ReleaseState_RELEASE_STATE_RETIRED:
		return "retired"
	default:
		return "draft"
	}
}

func releaseModeForState(s commonv1.ReleaseState) string {
	switch s {
	case commonv1.ReleaseState_RELEASE_STATE_SHADOW:
		return "shadow"
	case commonv1.ReleaseState_RELEASE_STATE_CANARY:
		return "canary"
	case commonv1.ReleaseState_RELEASE_STATE_ENFORCE:
		return "enforce"
	default:
		return "shadow"
	}
}

func canaryPercent(r kernel.Release) int32 {
	if c, ok := r.(*kernel.Canary); ok {
		return c.CanaryPercent
	}
	return 0
}

func retireReasonEnum(s string) commonv1.RetireReason {
	switch s {
	case "rollback":
		return commonv1.RetireReason_RETIRE_REASON_ROLLBACK
	case "manual":
		return commonv1.RetireReason_RETIRE_REASON_MANUAL
	case "ttl":
		return commonv1.RetireReason_RETIRE_REASON_TTL
	case "superseded":
		return commonv1.RetireReason_RETIRE_REASON_SUPERSEDED
	default:
		return commonv1.RetireReason_RETIRE_REASON_UNSPECIFIED
	}
}

func retireReasonString(r commonv1.RetireReason) (string, error) {
	switch r {
	case commonv1.RetireReason_RETIRE_REASON_ROLLBACK:
		return "rollback", nil
	case commonv1.RetireReason_RETIRE_REASON_MANUAL:
		return "manual", nil
	case commonv1.RetireReason_RETIRE_REASON_TTL:
		return "ttl", nil
	case commonv1.RetireReason_RETIRE_REASON_SUPERSEDED:
		return "superseded", nil
	default:
		return "", errors.New("unknown retire reason")
	}
}

func idemScope(userID, objectID string) string {
	if strings.TrimSpace(objectID) == "" {
		return "govern:" + userID
	}
	return "govern:" + userID + ":" + objectID
}

func (s *GovernServer) beginWriteIdem(ctx context.Context, userID, rpc string, header http.Header, msg proto.Message) (string, string, []byte, error) {
	key, err := requireIdempotencyKey(header)
	if err != nil {
		return "", "", nil, err
	}
	raw, err := protojson.Marshal(msg)
	if err != nil {
		return "", "", nil, err
	}
	objectID := protoObjectID(msg)
	digest := requestDigest(rpc, string(raw), key)
	hit, _, body, err := reserveIdempotency(ctx, s.pool, idemScope(userID, objectID), key, digest)
	if err != nil {
		return "", "", nil, err
	}
	if hit {
		return key, digest, []byte(unwrapIdemBody(body)), nil
	}
	return key, digest, nil, nil
}

func protoObjectID(msg proto.Message) string {
	switch m := msg.(type) {
	case interface{ GetReleaseId() string }:
		return m.GetReleaseId()
	default:
		return ""
	}
}

func (s *GovernServer) abortWriteIdem(ctx context.Context, userID, key string, msg proto.Message) {
	_ = abortIdempotency(ctx, s.pool, idemScope(userID, protoObjectID(msg)), key)
}

func (s *GovernServer) authorizePromote(ctx context.Context, user *authv1.User, tool, releaseID string) error {
	if err := s.authorizeReleaseWrite(ctx, user, tool, releaseID); err != nil {
		return err
	}
	if s.demoTriage {
		return nil
	}
	var createdBy string
	if err := s.pool.QueryRow(ctx, `SELECT created_by FROM releases WHERE release_id=$1`, releaseID).Scan(&createdBy); err != nil {
		return err
	}
	if createdBy != "" && createdBy == user.UserId {
		return connect.NewError(connect.CodePermissionDenied, errors.New("proposer cannot promote the same release"))
	}
	return nil
}

func (s *GovernServer) authorizeReleaseWrite(ctx context.Context, user *authv1.User, tool, releaseID string) error {
	ids, err := releaseAssetIDs(ctx, s.pool, releaseID)
	if err != nil && !s.demoTriage {
		return err
	}
	return authorizeWriteAssets(ctx, s.pool, user, tool, ids, s.demoTriage)
}
