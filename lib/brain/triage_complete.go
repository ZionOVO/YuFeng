package brain

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	agentv1 "yufeng/proto/gen/agentv1"
	artifactv1 "yufeng/proto/gen/artifactv1"
	commonv1 "yufeng/proto/gen/commonv1"
	governv1 "yufeng/proto/gen/governv1"

	"yufeng/lib/kernel"
)

type triageCompleteArgs struct {
	TurnID   string          `json:"turn_id"`
	Decision json.RawMessage `json:"decision"`
}

func (s *ToolGatewayServer) toolTriageGet(ctx context.Context, claims kernel.Claims, argsJSON string) (any, error) {
	args, err := parseArgs(argsJSON)
	if err != nil {
		return nil, err
	}
	turnID := argString(args, "turn_id")
	if turnID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("turn_id is required"))
	}
	if !bindingAllowsTurn(claims.Bindings, turnID) {
		return nil, deniedObject()
	}
	snapshot, agentID, _, err := loadTriageTurnSnapshot(ctx, s.pool, turnID)
	if err != nil {
		return nil, err
	}
	if agentID != claims.Subject || !bindingsCoverAssets(claims.Bindings, []string{snapshot.AssetID}) {
		return nil, deniedObject()
	}
	return map[string]any{"turnId": turnID, "projection": snapshot}, nil
}

func (s *ToolGatewayServer) toolTriageComplete(ctx context.Context, claims kernel.Claims, argsJSON string) (any, error) {
	var args triageCompleteArgs
	if err := decodeStrictJSON(argsJSON, &args); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("args_json: %w", err))
	}
	args.TurnID = strings.TrimSpace(args.TurnID)
	if args.TurnID == "" || len(args.Decision) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("turn_id and decision are required"))
	}
	if !bindingAllowsTurn(claims.Bindings, args.TurnID) {
		return nil, deniedObject()
	}
	var decision agentv1.TriageDecision
	if err := protojson.Unmarshal(args.Decision, &decision); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("decision: %w", err))
	}
	if err := validateTriageDecision(&decision); err != nil {
		return nil, err
	}
	snapshot, agentID, _, err := loadTriageTurnSnapshot(ctx, s.pool, args.TurnID)
	if err != nil {
		return nil, err
	}
	if agentID != claims.Subject || decision.GetClusterId() != snapshot.ClusterID || !bindingsCoverAssets(claims.Bindings, []string{snapshot.AssetID}) {
		return nil, deniedObject()
	}
	digest, rawDecision, err := triageDecisionDigest(&decision)
	if err != nil {
		return nil, err
	}
	if existing, found, err := s.existingTriageDecision(ctx, args.TurnID, digest); err != nil || found {
		return existing, err
	}

	switch decision.GetDisposition() {
	case agentv1.TriageDisposition_TRIAGE_DISPOSITION_REPORT_ONLY,
		agentv1.TriageDisposition_TRIAGE_DISPOSITION_ESCALATE_HUMAN,
		agentv1.TriageDisposition_TRIAGE_DISPOSITION_INSUFFICIENT_EVIDENCE:
		result := map[string]any{"turnId": args.TurnID, "state": "completed", "disposition": decision.GetDisposition().String()}
		if err := s.storeTriageDecisionWithoutRelease(ctx, args.TurnID, claims.Subject, digest, rawDecision, result); err != nil {
			if isUniqueViolation(err) {
				return s.existingTriageDecisionResult(ctx, args.TurnID, digest)
			}
			return nil, err
		}
		return result, nil
	}

	trusted, err := trustedProposalFromTurn(ctx, s.pool, snapshot, &decision)
	if err != nil {
		return nil, err
	}
	decisionID, err := newID("decision")
	if err != nil {
		return nil, err
	}
	proposed, err := writeTrustedProposal(ctx, s.pool, claims.Subject, trusted, nil, func(tx pgx.Tx, out *governv1.ProposeArtifactResponse) error {
		if _, err := tx.Exec(ctx, `INSERT INTO triage_decisions(decision_id, turn_id, agent_id, decision_digest, decision, release_id)
			VALUES($1,$2,$3,$4,$5::jsonb,$6)`, decisionID, args.TurnID, claims.Subject, digest, rawDecision, out.GetReleaseId()); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE agent_turns SET state='running', output_ref=$1, updated_at=now() WHERE turn_id=$2`, out.GetReleaseId(), args.TurnID); err != nil {
			return err
		}
		return appendAuditTx(ctx, tx, "agent", claims.Subject, "triage.complete", "turn", args.TurnID,
			map[string]any{"release_id": out.GetReleaseId(), "disposition": decision.GetDisposition().String()})
	})
	if err != nil {
		if isUniqueViolation(err) {
			return s.existingTriageDecisionResult(ctx, args.TurnID, digest)
		}
		return nil, err
	}
	result, err := s.finishTriageRelease(ctx, claims.Subject, args.TurnID, proposed.GetReleaseId(), decision.GetDisposition())
	if err != nil {
		return nil, err
	}
	return result, nil
}

func decodeStrictJSON(raw string, dst any) error {
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func validateTriageDecision(decision *agentv1.TriageDecision) error {
	if decision == nil || strings.TrimSpace(decision.GetClusterId()) == "" {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("decision.cluster_id is required"))
	}
	if decision.GetDisposition() == agentv1.TriageDisposition_TRIAGE_DISPOSITION_UNSPECIFIED {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("decision.disposition is required"))
	}
	rationale := strings.TrimSpace(decision.GetRationale())
	if rationale == "" || len(rationale) > 2000 {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("decision.rationale must be between 1 and 2000 bytes"))
	}
	if decision.GetDisposition() == agentv1.TriageDisposition_TRIAGE_DISPOSITION_PROPOSE_SHAPE {
		if decision.GetOptionalShapeDraft() == nil {
			return connect.NewError(connect.CodeInvalidArgument, errors.New("optional_shape_draft is required for shape disposition"))
		}
	} else if decision.GetOptionalShapeDraft() != nil {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("optional_shape_draft is only allowed for shape disposition"))
	}
	return nil
}

func triageDecisionDigest(decision *agentv1.TriageDecision) (string, []byte, error) {
	stable, err := proto.MarshalOptions{Deterministic: true}.Marshal(decision)
	if err != nil {
		return "", nil, err
	}
	raw, err := protojson.Marshal(decision)
	if err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256(stable)
	return hex.EncodeToString(sum[:]), raw, nil
}

func trustedProposalFromTurn(ctx context.Context, db dbTX, snapshot triageTurnSnapshot, decision *agentv1.TriageDecision) (trustedProposal, error) {
	events, err := loadPinnedTriageEvents(ctx, db, snapshot.EventIDs, snapshot.AssetID)
	if err != nil {
		return trustedProposal{}, err
	}
	cluster := proposalCluster{
		assetID: snapshot.AssetID, route: snapshot.RouteTemplate, method: snapshot.Method,
		reason: snapshot.Reason, eventIDs: append([]string(nil), snapshot.EventIDs...), events: events,
	}
	scope := &artifactv1.Scope{AssetIds: []string{snapshot.AssetID}}
	intent := &governv1.ProposalIntent{ClusterId: snapshot.ClusterID}
	switch decision.GetDisposition() {
	case agentv1.TriageDisposition_TRIAGE_DISPOSITION_PROPOSE_POLICY:
		if snapshot.Reason != commonv1.TriageReason_TRIAGE_REASON_DETECTED_UNMITIGATED.String() &&
			snapshot.Reason != commonv1.TriageReason_TRIAGE_REASON_DETECTED_UNMAPPED.String() {
			return trustedProposal{}, connect.NewError(connect.CodeFailedPrecondition, errors.New("policy disposition is incompatible with triage reason"))
		}
		intent.Kind = commonv1.ProposalKind_PROPOSAL_KIND_POLICY
		for _, raw := range snapshot.DetectionKeys {
			var key commonv1.DetectionKey
			if err := protojson.Unmarshal(raw, &key); err != nil || strings.TrimSpace(key.GetRuleId()) == "" {
				return trustedProposal{}, connect.NewError(connect.CodeFailedPrecondition, errors.New("triage snapshot has invalid detection key"))
			}
			intent.DetectionKeys = append(intent.DetectionKeys, &key)
		}
		if len(intent.DetectionKeys) == 0 {
			return trustedProposal{}, connect.NewError(connect.CodeFailedPrecondition, errors.New("policy disposition requires pinned detection keys"))
		}
		if err := deriveClusterRoute(intent, cluster); err != nil {
			return trustedProposal{}, err
		}
		risk := commonv1.ScopeRisk_SCOPE_RISK_ROUTE
		for _, key := range intent.DetectionKeys {
			if key.GetTargetSelector() != "" {
				risk = commonv1.ScopeRisk_SCOPE_RISK_EXACT
				break
			}
		}
		evidence := commonv1.EvidenceClass_EVIDENCE_CLASS_CRS_MAPPED
		if snapshot.Reason == commonv1.TriageReason_TRIAGE_REASON_DETECTED_UNMAPPED.String() {
			evidence = commonv1.EvidenceClass_EVIDENCE_CLASS_CRS_UNMAPPED
		}
		return trustedProposal{intent: intent, scope: scope, evidenceRefs: cluster.eventIDs, scopeRisk: risk, evidence: evidence}, nil
	case agentv1.TriageDisposition_TRIAGE_DISPOSITION_PROPOSE_SHAPE:
		if snapshot.Reason != commonv1.TriageReason_TRIAGE_REASON_SUSPECTED_MISS.String() {
			return trustedProposal{}, connect.NewError(connect.CodeFailedPrecondition, errors.New("shape disposition is incompatible with triage reason"))
		}
		intent.Kind = commonv1.ProposalKind_PROPOSAL_KIND_SHAPE
		intent.ShapeSource = proto.Clone(decision.GetOptionalShapeDraft()).(*artifactv1.ShapeSource)
		if err := validateShapeAgainstCluster(intent.GetShapeSource(), cluster); err != nil {
			return trustedProposal{}, err
		}
		return trustedProposal{
			intent: intent, scope: scope, evidenceRefs: cluster.eventIDs,
			scopeRisk: commonv1.ScopeRisk_SCOPE_RISK_EXACT, evidence: commonv1.EvidenceClass_EVIDENCE_CLASS_INTEL,
		}, nil
	default:
		return trustedProposal{}, connect.NewError(connect.CodeInvalidArgument, errors.New("disposition does not create a proposal"))
	}
}

func (s *ToolGatewayServer) existingTriageDecision(ctx context.Context, turnID, digest string) (map[string]any, bool, error) {
	var storedDigest string
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT decision_digest, result FROM triage_decisions WHERE turn_id=$1`, turnID).Scan(&storedDigest, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if storedDigest != digest {
		return nil, true, connect.NewError(connect.CodeAlreadyExists, errors.New("triage turn already has a different decision"))
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, true, err
	}
	if len(result) == 0 {
		var releaseID, agentID string
		if err := s.pool.QueryRow(ctx, `SELECT release_id, agent_id FROM triage_decisions WHERE turn_id=$1`, turnID).Scan(&releaseID, &agentID); err != nil {
			return nil, true, err
		}
		if releaseID != "" {
			resumed, err := s.finishTriageRelease(ctx, agentID, turnID, releaseID, agentv1.TriageDisposition_TRIAGE_DISPOSITION_UNSPECIFIED)
			return resumed, true, err
		}
	}
	return result, true, nil
}

func (s *ToolGatewayServer) existingTriageDecisionResult(ctx context.Context, turnID, digest string) (map[string]any, error) {
	result, found, err := s.existingTriageDecision(ctx, turnID, digest)
	if !found && err == nil {
		return nil, connect.NewError(connect.CodeAborted, errors.New("triage decision conflict was not committed"))
	}
	return result, err
}

func (s *ToolGatewayServer) storeTriageDecisionWithoutRelease(ctx context.Context, turnID, agentID, digest string, rawDecision []byte, result map[string]any) error {
	decisionID, err := newID("decision")
	if err != nil {
		return err
	}
	rawResult, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return withTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO triage_decisions(decision_id, turn_id, agent_id, decision_digest, decision, result, completed_at)
			VALUES($1,$2,$3,$4,$5::jsonb,$6::jsonb,now())`, decisionID, turnID, agentID, digest, rawDecision, rawResult); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE agent_turns SET state='completed', completed_at=now() WHERE turn_id=$1`, turnID); err != nil {
			return err
		}
		return appendAuditTx(ctx, tx, "agent", agentID, "triage.complete", "turn", turnID, result)
	})
}

func (s *ToolGatewayServer) finishTriageRelease(ctx context.Context, agentID, turnID, releaseID string, disposition agentv1.TriageDisposition) (map[string]any, error) {
	release, err := loadRelease(ctx, s.pool, releaseID)
	if err != nil {
		return nil, err
	}
	if release.State() == commonv1.ReleaseState_RELEASE_STATE_DRAFT {
		gated, err := writeGate(ctx, s.pool, s.key, "agent", agentID, releaseID, s.artifactSigner, nil)
		if err != nil {
			return nil, err
		}
		if gated.GetState() != commonv1.ReleaseState_RELEASE_STATE_SIGNED {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("triage proposal did not pass replay gate"))
		}
		release, err = loadRelease(ctx, s.pool, releaseID)
		if err != nil {
			return nil, err
		}
	}
	if release.State() == commonv1.ReleaseState_RELEASE_STATE_SIGNED {
		if _, err := writeStartShadow(ctx, s.pool, "agent", agentID, releaseID, s.key, s.artifactSigner, nil); err != nil {
			return nil, err
		}
	}
	result := map[string]any{"turnId": turnID, "releaseId": releaseID, "state": "SHADOW"}
	if disposition != agentv1.TriageDisposition_TRIAGE_DISPOSITION_UNSPECIFIED {
		result["disposition"] = disposition.String()
	}
	rawResult, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	if _, err := s.pool.Exec(ctx, `UPDATE triage_decisions SET result=$1::jsonb, completed_at=now() WHERE turn_id=$2`, rawResult, turnID); err != nil {
		return nil, err
	}
	if _, err := s.pool.Exec(ctx, `UPDATE agent_turns SET state='completed', output_ref=$1, completed_at=now() WHERE turn_id=$2`, releaseID, turnID); err != nil {
		return nil, err
	}
	return result, nil
}
