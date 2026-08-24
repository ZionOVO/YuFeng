package brain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"yufeng/lib/edgecore"
	"yufeng/lib/kernel"
	artifactv1 "yufeng/proto/gen/artifactv1"
	telemetryv1 "yufeng/proto/gen/telemetryv1"
)

// UploadTrafficWindows 优先接收不含原文的有界统计窗，并逐项报告去重与拒绝结果。
func (s *TelemetryServer) UploadTrafficWindows(ctx context.Context, req *connect.Request[telemetryv1.UploadTrafficWindowsRequest]) (*connect.Response[telemetryv1.UploadTrafficWindowsResponse], error) {
	unitID, err := requireUnitRPC(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	if len(req.Msg.GetWindows()) > 100 {
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("at most 100 traffic windows per batch"))
	}
	resp := &telemetryv1.UploadTrafficWindowsResponse{}
	for _, window := range req.Msg.GetWindows() {
		id := ""
		if window != nil {
			id = window.GetWindowId()
		}
		outcome, code, message, err := s.ingestTrafficWindow(ctx, unitID, window)
		if err != nil {
			return nil, err
		}
		switch outcome {
		case "accepted":
			resp.Accepted++
		case "deduped":
			resp.Deduped++
		default:
			resp.Rejected = append(resp.Rejected, &telemetryv1.RejectedEvent{
				EventId: id, Code: code, Message: message, Retryable: reviewRejectionRetryable(code),
			})
		}
	}
	return connect.NewResponse(resp), nil
}

func (s *TelemetryServer) ingestTrafficWindow(ctx context.Context, unitID string, window *telemetryv1.TrafficWindow) (string, string, string, error) {
	if window == nil || strings.TrimSpace(window.GetWindowId()) == "" || strings.TrimSpace(window.GetAssetId()) == "" ||
		window.GetWindowStart() == nil || !window.GetWindowStart().IsValid() || window.GetWindowEnd() == nil || !window.GetWindowEnd().IsValid() ||
		window.GetWindowEnd().AsTime().Sub(window.GetWindowStart().AsTime()) != kernel.TrafficReviewWindow ||
		strings.TrimSpace(window.GetPolicyDigest()) == "" || window.GetRequestCount() < 0 || len(window.GetRouteCells()) > kernel.TrafficReviewTopRoutes ||
		window.GetReviewMode() < artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_STATISTICS_ONLY ||
		window.GetReviewMode() > artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_SHADOW_CANDIDATES || window.GetOther() == nil {
		return "", "invalid_window", "traffic window is incomplete or exceeds policy bounds", nil
	}
	requestCount, criticalCount, blockedCount, incompleteCount := window.GetOther().GetRequestCount(), window.GetOther().GetCriticalCount(), window.GetOther().GetBlockedCount(), window.GetOther().GetIncompleteCount()
	if !validTrafficRouteCell(window.GetOther(), true) {
		return "", "invalid_window", "traffic window other cell is invalid", nil
	}
	for _, cell := range window.GetRouteCells() {
		if !validTrafficRouteCell(cell, false) {
			return "", "invalid_window", "traffic window route cell is invalid", nil
		}
		requestCount += cell.GetRequestCount()
		criticalCount += cell.GetCriticalCount()
		blockedCount += cell.GetBlockedCount()
		incompleteCount += cell.GetIncompleteCount()
	}
	if requestCount != window.GetRequestCount() || criticalCount != window.GetCriticalCount() || blockedCount != window.GetBlockedCount() ||
		incompleteCount != window.GetIncompleteCount() || window.GetObservedCount() < 0 || window.GetObservedCount() > window.GetRequestCount() {
		return "", "invalid_window", "traffic window aggregate counts do not match route cells", nil
	}
	allowedDropReasons := map[string]bool{
		"encoding_failed": true, "low_risk_capacity_reserved": true,
		"vault_capacity_exhausted": true, "vault_unavailable": true,
	}
	var dropped int64
	for reason, count := range window.GetEvidenceDropReasons() {
		if !allowedDropReasons[reason] || count < 0 {
			return "", "invalid_window", "traffic window evidence drop reason is invalid", nil
		}
		dropped += count
	}
	if dropped != window.GetEvidenceDroppedCount() {
		return "", "invalid_window", "traffic window evidence drop count does not match reasons", nil
	}
	if window.GetUnitId() != "" && window.GetUnitId() != unitID {
		return "", "permission_denied", "window unit does not match authenticated unit", nil
	}
	bound, err := unitBindsAsset(ctx, s.pool, unitID, window.GetAssetId())
	if err != nil {
		return "", "", "", err
	}
	if !bound {
		return "", "permission_denied", "asset is not bound to unit", nil
	}
	routes, err := json.Marshal(window.GetRouteCells())
	if err != nil {
		return "", "invalid_window", err.Error(), nil
	}
	other, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(window.GetOther())
	if err != nil {
		return "", "invalid_window", err.Error(), nil
	}
	dropReasons, err := json.Marshal(window.GetEvidenceDropReasons())
	if err != nil {
		return "", "invalid_window", err.Error(), nil
	}
	semantic, err := proto.MarshalOptions{Deterministic: true}.Marshal(window)
	if err != nil {
		return "", "invalid_window", err.Error(), nil
	}
	sum := sha256.Sum256(semantic)
	payloadDigest := "sha256:" + hex.EncodeToString(sum[:])
	inserted, changed := false, false
	err = withTx(ctx, s.trafficPool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO traffic.traffic_window_receipts(window_id, window_start, payload_digest)
			VALUES($1,$2,$3) ON CONFLICT(window_id) DO NOTHING`, window.GetWindowId(), window.GetWindowStart().AsTime(), payloadDigest)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			var storedDigest string
			if err := tx.QueryRow(ctx, `SELECT payload_digest FROM traffic.traffic_window_receipts WHERE window_id=$1`, window.GetWindowId()).Scan(&storedDigest); err != nil {
				return err
			}
			changed = storedDigest != payloadDigest
			return nil
		}
		if _, err := tx.Exec(ctx, `INSERT INTO traffic.traffic_windows(
			window_id, unit_id, asset_id, window_start, window_end, generation_id, generation_seq,
			policy_digest, request_count, critical_count, blocked_count, observed_count, incomplete_count,
			route_cells, other_cell, evidence_dropped_count, evidence_drop_reasons, review_mode)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14::jsonb,$15::jsonb,$16,$17::jsonb,$18)`,
			window.GetWindowId(), unitID, window.GetAssetId(), window.GetWindowStart().AsTime(), window.GetWindowEnd().AsTime(),
			window.GetGenerationId(), window.GetGenerationSeq(), window.GetPolicyDigest(), window.GetRequestCount(),
			window.GetCriticalCount(), window.GetBlockedCount(), window.GetObservedCount(), window.GetIncompleteCount(),
			routes, other, window.GetEvidenceDroppedCount(), dropReasons, window.GetReviewMode()); err != nil {
			return err
		}
		inserted = true
		return nil
	})
	if err != nil {
		return "", "", "", err
	}
	if changed {
		return "", "stable_id_changed", "traffic window payload changed for a stable identifier", nil
	}
	if !inserted {
		return "deduped", "", "", nil
	}
	return "accepted", "", "", nil
}

func validTrafficRouteCell(cell *telemetryv1.TrafficRouteCell, other bool) bool {
	if cell == nil || cell.GetRequestCount() < 0 || cell.GetCriticalCount() < 0 || cell.GetBlockedCount() < 0 || cell.GetIncompleteCount() < 0 ||
		cell.GetCriticalCount() > cell.GetRequestCount() || cell.GetBlockedCount() > cell.GetRequestCount() || cell.GetIncompleteCount() > cell.GetRequestCount() {
		return false
	}
	if other {
		return cell.GetMethod() == "" && cell.GetRouteTemplate() == ""
	}
	return strings.TrimSpace(cell.GetMethod()) != "" && len(cell.GetMethod()) <= 32 && strings.TrimSpace(cell.GetRouteTemplate()) != "" && len(cell.GetRouteTemplate()) <= 2048
}

// UploadReviewCandidates 接收不含原文的候选投影，并在 Agent 运行前执行案件配额与重复聚类。
func (s *TelemetryServer) UploadReviewCandidates(ctx context.Context, req *connect.Request[telemetryv1.UploadReviewCandidatesRequest]) (*connect.Response[telemetryv1.UploadReviewCandidatesResponse], error) {
	unitID, err := requireUnitRPC(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	if len(req.Msg.GetCandidates()) > 100 {
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("at most 100 review candidates per batch"))
	}
	resp := &telemetryv1.UploadReviewCandidatesResponse{}
	for _, candidate := range req.Msg.GetCandidates() {
		id := ""
		if candidate != nil {
			id = candidate.GetCandidateId()
		}
		outcome, code, message, err := s.ingestReviewCandidate(ctx, unitID, candidate)
		if err != nil {
			return nil, err
		}
		switch outcome {
		case "accepted":
			resp.Accepted++
		case "deduped":
			resp.Deduped++
		default:
			resp.Rejected = append(resp.Rejected, &telemetryv1.RejectedEvent{
				EventId: id, Code: code, Message: message, Retryable: reviewRejectionRetryable(code),
			})
		}
	}
	return connect.NewResponse(resp), nil
}

func reviewRejectionRetryable(code string) bool {
	return code == "traffic_store_unavailable" || code == "governance_store_unavailable"
}

func (s *TelemetryServer) ingestReviewCandidate(ctx context.Context, unitID string, candidate *telemetryv1.ReviewCandidate) (string, string, string, error) {
	now := time.Now()
	if candidate == nil || strings.TrimSpace(candidate.GetCandidateId()) == "" || strings.TrimSpace(candidate.GetWindowId()) == "" ||
		strings.TrimSpace(candidate.GetAssetId()) == "" || candidate.GetOccurredAt() == nil || !candidate.GetOccurredAt().IsValid() ||
		candidate.GetEvidenceExpiresAt() == nil || !candidate.GetEvidenceExpiresAt().IsValid() || !candidate.GetEvidenceExpiresAt().AsTime().After(now) ||
		candidate.GetEvidenceExpiresAt().AsTime().After(candidate.GetOccurredAt().AsTime().Add(kernel.TrafficReviewEvidenceTTL)) ||
		candidate.GetRiskScore() < 0 || candidate.GetRiskScore() > 100 ||
		candidate.GetReviewMode() < artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_REDACTED_CASES ||
		candidate.GetReviewMode() > artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_SHADOW_CANDIDATES ||
		len(candidate.GetMethod()) > 32 || len(candidate.GetRouteTemplate()) > 2048 || len(candidate.GetRiskReasons()) > 16 ||
		candidate.GetRouteTemplate() != edgecore.TrafficRouteTemplate(candidate.GetRouteTemplate()) ||
		candidate.GetBaseline() != (candidate.GetRiskScore() == 0) || !validReviewCandidateProjection(candidate) || !validReviewRiskReasons(candidate.GetRiskReasons()) {
		return "", "invalid_candidate", "review candidate is incomplete or exceeds policy bounds", nil
	}
	if candidate.GetReviewMode() >= artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_EVIDENCE_ON_APPROVAL &&
		(strings.TrimSpace(candidate.GetEvidenceHandle()) == "" || !validSHA256Reference(candidate.GetEvidenceDigest())) {
		return "", "invalid_candidate", "evidence-enabled candidate requires an opaque evidence handle and digest", nil
	}
	if candidate.GetReviewMode() == artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_REDACTED_CASES &&
		(candidate.GetEvidenceHandle() != "" || candidate.GetEvidenceDigest() != "") {
		return "", "invalid_candidate", "redacted candidate must not carry an evidence reference", nil
	}
	if candidate.GetUnitId() != "" && candidate.GetUnitId() != unitID {
		return "", "permission_denied", "candidate unit does not match authenticated unit", nil
	}
	bound, err := unitBindsAsset(ctx, s.pool, unitID, candidate.GetAssetId())
	if err != nil {
		return "", "", "", err
	}
	if !bound {
		return "", "permission_denied", "asset is not bound to unit", nil
	}
	reasons, err := json.Marshal(candidate.GetRiskReasons())
	if err != nil {
		return "", "invalid_candidate", err.Error(), nil
	}
	evidence, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(candidate.GetEvidence())
	if err != nil {
		return "", "invalid_candidate", err.Error(), nil
	}
	candidateJSON, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(candidate)
	if err != nil {
		return "", "invalid_candidate", err.Error(), nil
	}
	inserted, changed := false, false
	rejectionCode, rejectionMessage := "", ""
	err = withTx(ctx, s.trafficPool, func(tx pgx.Tx) error {
		var same bool
		err := tx.QueryRow(ctx, `SELECT candidate_json=$2::jsonb FROM traffic.review_case_outbox WHERE candidate_id=$1`,
			candidate.GetCandidateId(), candidateJSON).Scan(&same)
		if err == nil {
			changed = !same
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		lockKey := unitID + "\n" + candidate.GetWindowId()
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, lockKey); err != nil {
			return err
		}
		var windowUnit, windowAsset, generationID string
		var windowStart, windowEnd time.Time
		var generationSeq int64
		var reviewMode int32
		err = tx.QueryRow(ctx, `SELECT unit_id, asset_id, window_start, window_end, generation_id, generation_seq, review_mode
			FROM traffic.traffic_windows WHERE window_id=$1 LIMIT 1`, candidate.GetWindowId()).
			Scan(&windowUnit, &windowAsset, &windowStart, &windowEnd, &generationID, &generationSeq, &reviewMode)
		occurredAt := candidate.GetOccurredAt().AsTime()
		if errors.Is(err, pgx.ErrNoRows) || err == nil && (windowUnit != unitID || windowAsset != candidate.GetAssetId() ||
			occurredAt.Before(windowStart) || !occurredAt.Before(windowEnd) || generationID != candidate.GetGenerationId() ||
			generationSeq != candidate.GetGenerationSeq() || reviewMode != int32(candidate.GetReviewMode())) {
			rejectionCode = "window_binding_mismatch"
			rejectionMessage = "review candidate does not match an accepted traffic window"
			return nil
		}
		if err != nil {
			return err
		}
		var windowCandidates int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM traffic.review_candidates WHERE unit_id=$1 AND window_id=$2`,
			unitID, candidate.GetWindowId()).Scan(&windowCandidates); err != nil {
			return err
		}
		if windowCandidates >= kernel.TrafficReviewCandidatesPerWindow {
			rejectionCode = "candidate_limit_exceeded"
			rejectionMessage = "traffic window already contains the maximum review candidates"
			return nil
		}
		tag, err := tx.Exec(ctx, `INSERT INTO traffic.review_case_outbox(candidate_id, candidate_json)
			VALUES($1,$2::jsonb) ON CONFLICT(candidate_id) DO NOTHING`, candidate.GetCandidateId(), candidateJSON)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			if err := tx.QueryRow(ctx, `SELECT candidate_json=$2::jsonb FROM traffic.review_case_outbox WHERE candidate_id=$1`,
				candidate.GetCandidateId(), candidateJSON).Scan(&same); err != nil {
				return err
			}
			changed = !same
			return nil
		}
		if _, err := tx.Exec(ctx, `INSERT INTO traffic.review_candidates(
			candidate_id, window_id, unit_id, asset_id, occurred_at, request_id, method, route_template,
			risk_score, risk_reasons, evidence_projection, evidence_handle, evidence_digest,
			evidence_expires_at, generation_id, generation_seq, baseline, review_mode)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb,$11::jsonb,$12,$13,$14,$15,$16,$17,$18)`, candidate.GetCandidateId(), candidate.GetWindowId(), unitID,
			candidate.GetAssetId(), candidate.GetOccurredAt().AsTime(), candidate.GetRequestId(), candidate.GetMethod(),
			candidate.GetRouteTemplate(), candidate.GetRiskScore(), reasons, evidence, candidate.GetEvidenceHandle(),
			candidate.GetEvidenceDigest(), candidate.GetEvidenceExpiresAt().AsTime(), candidate.GetGenerationId(),
			candidate.GetGenerationSeq(), candidate.GetBaseline(), candidate.GetReviewMode()); err != nil {
			return err
		}
		inserted = true
		return nil
	})
	if err != nil {
		return "", "traffic_store_unavailable", "review candidate was not accepted", nil
	}
	if changed {
		return "", "stable_id_changed", "review candidate payload changed for a stable identifier", nil
	}
	if rejectionCode != "" {
		return "", rejectionCode, rejectionMessage, nil
	}
	if !inserted {
		return "deduped", "", "", nil
	}
	if err := s.processReviewCaseOutbox(ctx, candidate.GetCandidateId()); err != nil {
		return "", "", "", err
	}
	return "accepted", "", "", nil
}

func validReviewCandidateProjection(candidate *telemetryv1.ReviewCandidate) bool {
	projection := candidate.GetEvidence()
	if projection == nil || projection.GetAlgorithm() != 0 || projection.GetMaxSpanBytes() != 0 || len(projection.GetFields()) != 2 {
		return false
	}
	return projection.GetFields()["method"] == candidate.GetMethod() && projection.GetFields()["route_template"] == candidate.GetRouteTemplate()
}

func validReviewRiskReasons(reasons []string) bool {
	allowed := map[string]bool{
		"sync_detection": true, "suspected_miss": true, "unmitigated": true, "blocked": true,
		"inspection_incomplete": true, "unmapped_detection": true, "anomaly_score": true, "critical": true,
	}
	seen := make(map[string]bool, len(reasons))
	for _, reason := range reasons {
		if !allowed[reason] || seen[reason] {
			return false
		}
		seen[reason] = true
	}
	return true
}

func validSHA256Reference(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func (s *TelemetryServer) processReviewCaseOutbox(ctx context.Context, candidateID string) error {
	err := withTx(ctx, s.pool, func(tx pgx.Tx) error {
		var raw []byte
		err := tx.QueryRow(ctx, `SELECT candidate_json FROM traffic.review_case_outbox
			WHERE candidate_id=$1 AND state='pending' AND next_attempt_at<=now() FOR UPDATE`, candidateID).Scan(&raw)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		var candidate telemetryv1.ReviewCandidate
		if err := protojson.Unmarshal(raw, &candidate); err != nil {
			return err
		}
		if err := s.attachCandidateToCase(ctx, tx, &candidate); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE traffic.review_case_outbox SET state='processed', processed_at=now(),
			attempts=attempts+1, last_error='' WHERE candidate_id=$1`, candidateID)
		return err
	})
	if err == nil {
		return nil
	}
	_, recordErr := s.pool.Exec(ctx, `UPDATE traffic.review_case_outbox SET attempts=attempts+1,
		next_attempt_at=now()+LEAST(interval '15 minutes', interval '5 seconds' * power(2, LEAST(attempts,8))),
		last_error=$2 WHERE candidate_id=$1 AND state='pending'`, candidateID, truncateTrafficError(err.Error()))
	if recordErr != nil {
		return errors.Join(err, recordErr)
	}
	return nil
}

func truncateTrafficError(message string) string {
	if len(message) > 512 {
		return message[:512]
	}
	return message
}

func unitBindsAsset(ctx context.Context, db dbTX, unitID, assetID string) (bool, error) {
	var bound bool
	err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM unit_assets WHERE unit_id=$1 AND asset_id=$2)`, unitID, assetID).Scan(&bound)
	return bound, err
}

func (s *TelemetryServer) attachCandidateToCase(ctx context.Context, tx pgx.Tx, candidate *telemetryv1.ReviewCandidate) error {
	clusterID := trafficClusterID(candidate)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1), 34)`, "traffic-case:"+candidate.GetAssetId()+":"+clusterID); err != nil {
		return err
	}
	var caseID, caseState string
	var representativesRaw []byte
	err := tx.QueryRow(ctx, `SELECT case_id, representatives, state FROM investigation_cases
		WHERE asset_id=$1 AND module_id='traffic-interception' AND cluster_id=$2
		  AND state NOT IN ('resolved','failed','evidence_expired')
		ORDER BY created_at LIMIT 1 FOR UPDATE`, candidate.GetAssetId(), clusterID).Scan(&caseID, &representativesRaw, &caseState)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	candidateJSON, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(candidate)
	if err != nil {
		return err
	}
	priority, err := trafficCasePriority(ctx, tx, candidate, clusterID)
	if err != nil {
		return err
	}
	if caseID == "" {
		if candidate.GetBaseline() {
			return nil
		}
		suppressedReason := ""
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('traffic-case-daily-quota'), 34)`); err != nil {
			return err
		}
		var globalToday, assetToday int
		if err := tx.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE asset_id=$1)
			FROM investigation_cases WHERE module_id='traffic-interception'
			  AND automation_suppressed_reason='' AND created_at >= date_trunc('day', now())`, candidate.GetAssetId()).Scan(&globalToday, &assetToday); err != nil {
			return err
		}
		if globalToday >= kernel.TrafficReviewDailyCases || assetToday >= kernel.TrafficReviewDailyCasesPerAsset {
			suppressedReason = "daily_investigation_quota"
		}
		caseID, err = newID("case")
		if err != nil {
			return err
		}
		title := fmt.Sprintf("%s %s 流量审查", strings.ToUpper(candidate.GetMethod()), candidate.GetRouteTemplate())
		representatives, err := s.initialCaseRepresentatives(ctx, tx, candidate, candidateJSON)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO investigation_cases(
			case_id, module_id, asset_id, cluster_id, state, priority, title, summary, representatives, automation_suppressed_reason)
			VALUES($1,'traffic-interception',$2,$3,'open',$4,$5,$6,$7::jsonb,$8)`,
			caseID, candidate.GetAssetId(), clusterID, priority, title, "边缘筛选出的高价值脱敏候选", representatives, suppressedReason); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO case_activities(case_id, kind, ref_id, summary)
			VALUES($1,'created',$2,'案件由有界流量候选聚类创建')`, caseID, candidate.GetCandidateId())
		if err != nil {
			return err
		}
		if priority >= 80 {
			if err := notifyCaseSessions(ctx, tx, s.jarvisID, candidate.GetAssetId(),
				"SESSION_ATTACHMENT_KIND_CASE", caseID, "发现高优先级流量案件，请在案件工作台核验。"); err != nil {
				return err
			}
		}
		if suppressedReason != "" {
			_, err = tx.Exec(ctx, `INSERT INTO case_activities(case_id, kind, ref_id, summary)
				VALUES($1,'state_changed',$2,'案件已创建，但达到每日自动调查额度，等待后续重新排队')`, caseID, suppressedReason)
			return err
		}
		_, err = assignCaseAgentProfile(ctx, tx, caseID)
		return err
	}
	if caseState != "open" {
		// 代表样本在证据申请之前冻结；审批或调查开始后的新候选仍保留于候选表，
		// 但不再改写该案件的授权样本集。
		return nil
	}
	representatives, err := boundedCaseRepresentatives(representativesRaw, candidateJSON)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE investigation_cases SET priority=GREATEST(priority,$2),
		representatives=$3::jsonb, updated_at=now() WHERE case_id=$1`, caseID, priority, representatives)
	return err
}

func boundedCaseRepresentatives(existing, incoming []byte) ([]byte, error) {
	var values []json.RawMessage
	if len(existing) > 0 {
		if err := json.Unmarshal(existing, &values); err != nil {
			return nil, err
		}
	}
	values = append(values, append(json.RawMessage(nil), incoming...))
	type representative struct {
		candidate *telemetryv1.ReviewCandidate
		raw       json.RawMessage
	}
	seen := map[string]bool{}
	var high, baseline []representative
	for _, raw := range values {
		var candidate telemetryv1.ReviewCandidate
		if err := protojson.Unmarshal(raw, &candidate); err != nil {
			return nil, err
		}
		if candidate.GetCandidateId() == "" || seen[candidate.GetCandidateId()] {
			continue
		}
		seen[candidate.GetCandidateId()] = true
		value := representative{candidate: &candidate, raw: append(json.RawMessage(nil), raw...)}
		if candidate.GetBaseline() {
			baseline = append(baseline, value)
		} else {
			high = append(high, value)
		}
	}
	sort.Slice(high, func(i, j int) bool {
		if high[i].candidate.GetRiskScore() != high[j].candidate.GetRiskScore() {
			return high[i].candidate.GetRiskScore() > high[j].candidate.GetRiskScore()
		}
		return high[i].candidate.GetCandidateId() < high[j].candidate.GetCandidateId()
	})
	sort.Slice(baseline, func(i, j int) bool {
		left, right := highResolutionTimestamp(baseline[i].candidate.GetOccurredAt()), highResolutionTimestamp(baseline[j].candidate.GetOccurredAt())
		if !left.Equal(right) {
			return left.After(right)
		}
		return baseline[i].candidate.GetCandidateId() < baseline[j].candidate.GetCandidateId()
	})
	if len(high) > 3 {
		high = high[:3]
	}
	if len(baseline) > 2 {
		baseline = baseline[:2]
	}
	out := make([]json.RawMessage, 0, len(high)+len(baseline))
	for _, value := range append(high, baseline...) {
		out = append(out, value.raw)
	}
	return json.Marshal(out)
}

func highResolutionTimestamp(value *timestamppb.Timestamp) time.Time {
	if value == nil || !value.IsValid() {
		return time.Time{}
	}
	return value.AsTime()
}

func trafficCasePriority(ctx context.Context, db dbTX, candidate *telemetryv1.ReviewCandidate, clusterID string) (int, error) {
	var criticality string
	if err := db.QueryRow(ctx, `SELECT criticality FROM assets WHERE asset_id=$1`, candidate.GetAssetId()).Scan(&criticality); err != nil {
		return 0, err
	}
	var novel bool
	if err := db.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM traffic.review_candidates
		WHERE candidate_id<>$1 AND asset_id=$2 AND upper(method)=upper($3::text) AND route_template=$4
		  AND occurred_at>=now()-interval '30 days')`, candidate.GetCandidateId(), candidate.GetAssetId(),
		candidate.GetMethod(), candidate.GetRouteTemplate()).Scan(&novel); err != nil {
		return 0, err
	}
	var feedback string
	err := db.QueryRow(ctx, `SELECT f.resolution FROM case_feedback f
		JOIN investigation_cases c USING(case_id)
		WHERE f.asset_id=$1 AND c.cluster_id=$2
		ORDER BY f.created_at DESC LIMIT 1`, candidate.GetAssetId(), clusterID).Scan(&feedback)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, err
	}
	return priorityFromTrafficSignals(candidate.GetRiskScore(), criticality, candidate.GetRiskReasons(), novel, feedback), nil
}

func priorityFromTrafficSignals(score float64, criticality string, reasons []string, novel bool, feedback string) int {
	priority := int(math.Round(score))
	switch strings.ToLower(criticality) {
	case "p0":
		priority += 12
	case "p1":
		priority += 7
	case "p2":
		priority += 2
	}
	for _, reason := range reasons {
		switch reason {
		case "suspected_miss":
			priority += 8
		case "unmapped_detection":
			priority += 6
		case "inspection_incomplete":
			priority += 5
		case "unmitigated":
			priority += 4
		case "anomaly_score":
			priority += 3
		}
	}
	if novel {
		priority += 5
	}
	switch feedback {
	case "confirmed_malicious", "shadow_published":
		priority += 5
	case "false_positive", "benign":
		priority -= 10
	}
	if priority < 0 {
		return 0
	}
	if priority > 100 {
		return 100
	}
	return priority
}

func (s *TelemetryServer) initialCaseRepresentatives(ctx context.Context, tx pgx.Tx, candidate *telemetryv1.ReviewCandidate, candidateJSON []byte) ([]byte, error) {
	representatives := []json.RawMessage{append(json.RawMessage(nil), candidateJSON...)}
	rows, err := tx.Query(ctx, `SELECT candidate_json FROM traffic.review_case_outbox
		WHERE candidate_id<>$1 AND candidate_json->>'asset_id'=$2
		  AND upper(candidate_json->>'method')=upper($3::text) AND candidate_json->>'route_template'=$4
		  AND COALESCE((candidate_json->>'baseline')::boolean,false)
		ORDER BY created_at DESC LIMIT 2`, candidate.GetCandidateId(), candidate.GetAssetId(), candidate.GetMethod(), candidate.GetRouteTemplate())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw json.RawMessage
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		representatives = append(representatives, raw)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return json.Marshal(representatives)
}

func trafficClusterID(candidate *telemetryv1.ReviewCandidate) string {
	raw := strings.Join([]string{candidate.GetAssetId(), strings.ToUpper(candidate.GetMethod()), candidate.GetRouteTemplate()}, "\x00")
	sum := sha256.Sum256([]byte(raw))
	return "traffic:" + hex.EncodeToString(sum[:16])
}
