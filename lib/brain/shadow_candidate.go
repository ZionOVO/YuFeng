package brain

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/encoding/protojson"

	"yufeng/lib/kernel"
	artifactv1 "yufeng/proto/gen/artifactv1"
	commonv1 "yufeng/proto/gen/commonv1"
	governv1 "yufeng/proto/gen/governv1"
	modelv1 "yufeng/proto/gen/modelv1"
	telemetryv1 "yufeng/proto/gen/telemetryv1"
)

const trafficShadowCoordinatorID = "traffic-shadow-coordinator"

// StartShadowCandidateCoordinator 持续把已验证的疑似漏报持久任务推进到真实 Shadow 发布。
func StartShadowCandidateCoordinator(ctx context.Context, pool *pgxpool.Pool, key ed25519.PrivateKey, signer kernel.Signer) {
	go func() {
		run := func() {
			if err := ProcessShadowCandidateJobs(ctx, pool, key, signer); err != nil {
				log.Printf("流量 Shadow 协调任务失败: %v", err)
			}
		}
		run()
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				run()
			}
		}
	}()
}

// ProcessShadowCandidateJobs 幂等推进当前到期的 Shadow 协调任务；任何失败都保留重试证据。
func ProcessShadowCandidateJobs(ctx context.Context, pool *pgxpool.Pool, key ed25519.PrivateKey, signer kernel.Signer) error {
	rows, err := pool.Query(ctx, `SELECT case_id FROM shadow_candidate_jobs
		WHERE state IN ('pending','proposed','gated') AND next_attempt_at<=now()
		ORDER BY created_at LIMIT 20`)
	if err != nil {
		return err
	}
	var caseIDs []string
	for rows.Next() {
		var caseID string
		if err := rows.Scan(&caseID); err != nil {
			rows.Close()
			return err
		}
		caseIDs = append(caseIDs, caseID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, caseID := range caseIDs {
		if err := processShadowCandidateJob(ctx, pool, key, signer, caseID); err != nil {
			if _, recordErr := pool.Exec(ctx, `UPDATE shadow_candidate_jobs SET attempts=attempts+1,
				next_attempt_at=now()+LEAST(interval '15 minutes',interval '5 seconds'*power(2,LEAST(attempts,8))),
				last_error=$2,updated_at=now() WHERE case_id=$1 AND state IN ('pending','proposed','gated')`,
				caseID, truncateTrafficError(err.Error())); recordErr != nil {
				return errors.Join(err, recordErr)
			}
		}
	}
	return nil
}

func processShadowCandidateJob(ctx context.Context, pool *pgxpool.Pool, key ed25519.PrivateKey, signer kernel.Signer, caseID string) error {
	var jobState, releaseID, findingDigest, assetID, caseState string
	var findingRaw, representativesRaw []byte
	err := pool.QueryRow(ctx, `SELECT j.state,j.release_id,j.finding_digest,c.asset_id,c.state,c.finding,c.representatives
		FROM shadow_candidate_jobs j JOIN investigation_cases c USING(case_id) WHERE j.case_id=$1`, caseID).
		Scan(&jobState, &releaseID, &findingDigest, &assetID, &caseState, &findingRaw, &representativesRaw)
	if err != nil {
		return err
	}
	if caseState == "resolved" || caseState == "failed" || caseState == "evidence_expired" {
		_, err := pool.Exec(ctx, `UPDATE shadow_candidate_jobs SET state='failed',last_error='case is terminal',updated_at=now() WHERE case_id=$1`, caseID)
		return err
	}
	var finding modelv1.TrafficFinding
	if err := protojson.Unmarshal(findingRaw, &finding); err != nil {
		return err
	}
	actualFindingDigest, err := typedTrafficFindingDigest(&finding)
	if err != nil || actualFindingDigest != findingDigest {
		return errors.New("traffic finding changed after shadow coordination was queued")
	}
	if finding.GetDisposition() != modelv1.TrafficFindingDisposition_TRAFFIC_FINDING_DISPOSITION_SUSPECTED_MISS || finding.GetOptionalShapeDraft() == nil {
		return errors.New("shadow coordination requires a suspected miss shape draft")
	}
	trusted, err := trustedTrafficShadowProposal(assetID, &finding, representativesRaw)
	if err != nil {
		return err
	}
	actorID := trafficShadowCoordinatorID + ":" + caseID
	if jobState == "pending" {
		if releaseID == "" {
			err := pool.QueryRow(ctx, `SELECT release_id FROM releases WHERE created_by=$1 ORDER BY proposed_at DESC LIMIT 1`, actorID).Scan(&releaseID)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if releaseID == "" {
				proposed, err := writeTrustedProposal(ctx, pool, actorID, trusted, nil, nil)
				if err != nil {
					return err
				}
				releaseID = proposed.GetReleaseId()
			}
		}
		if _, err := pool.Exec(ctx, `UPDATE shadow_candidate_jobs SET state='proposed',release_id=$2,
			attempts=attempts+1,last_error='',updated_at=now() WHERE case_id=$1 AND state='pending'`, caseID, releaseID); err != nil {
			return err
		}
		jobState = "proposed"
	}
	if jobState == "proposed" {
		release, err := loadRelease(ctx, pool, releaseID)
		if err != nil {
			return err
		}
		switch release.State() {
		case commonv1.ReleaseState_RELEASE_STATE_DRAFT:
			gated, err := writeGate(ctx, pool, key, "agent", trafficShadowCoordinatorID, releaseID, signer, nil)
			if err != nil {
				return err
			}
			if gated.GetState() != commonv1.ReleaseState_RELEASE_STATE_SIGNED {
				_, updateErr := pool.Exec(ctx, `UPDATE shadow_candidate_jobs SET state='failed',last_error='replay gate rejected shape',updated_at=now() WHERE case_id=$1`, caseID)
				return updateErr
			}
		case commonv1.ReleaseState_RELEASE_STATE_SIGNED:
		case commonv1.ReleaseState_RELEASE_STATE_SHADOW:
			return finishShadowCandidateJob(ctx, pool, caseID, releaseID)
		default:
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("shadow candidate release entered an unsupported state"))
		}
		if _, err := pool.Exec(ctx, `UPDATE shadow_candidate_jobs SET state='gated',attempts=attempts+1,last_error='',updated_at=now()
			WHERE case_id=$1 AND state='proposed'`, caseID); err != nil {
			return err
		}
		jobState = "gated"
	}
	if jobState == "gated" {
		release, err := loadRelease(ctx, pool, releaseID)
		if err != nil {
			return err
		}
		if release.State() == commonv1.ReleaseState_RELEASE_STATE_SIGNED {
			if _, err := writeStartShadow(ctx, pool, "agent", trafficShadowCoordinatorID, releaseID, key, signer, nil); err != nil {
				return err
			}
		} else if release.State() != commonv1.ReleaseState_RELEASE_STATE_SHADOW {
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("gated shadow candidate is not signed"))
		}
		return finishShadowCandidateJob(ctx, pool, caseID, releaseID)
	}
	return nil
}

func trustedTrafficShadowProposal(assetID string, finding *modelv1.TrafficFinding, representativesRaw []byte) (trustedProposal, error) {
	if strings.TrimSpace(assetID) == "" || finding == nil || finding.GetOptionalShapeDraft() == nil {
		return trustedProposal{}, errors.New("traffic shadow proposal is incomplete")
	}
	var representatives []json.RawMessage
	if err := json.Unmarshal(representativesRaw, &representatives); err != nil {
		return trustedProposal{}, err
	}
	evidenceRefs := make([]string, 0, len(representatives))
	for _, raw := range representatives {
		var candidate telemetryv1.ReviewCandidate
		if err := protojson.Unmarshal(raw, &candidate); err != nil {
			return trustedProposal{}, err
		}
		if candidate.GetCandidateId() != "" {
			evidenceRefs = append(evidenceRefs, candidate.GetCandidateId())
		}
	}
	intent := &governv1.ProposalIntent{
		Kind: commonv1.ProposalKind_PROPOSAL_KIND_SHAPE, ShapeSource: finding.GetOptionalShapeDraft(),
		Methods: append([]string(nil), finding.GetOptionalShapeDraft().GetMethods()...), RouteTemplate: finding.GetRouteTemplate(),
	}
	return trustedProposal{
		intent: intent, scope: &artifactv1.Scope{AssetIds: []string{assetID}, RouteSelector: finding.GetRouteTemplate()},
		evidenceRefs: evidenceRefs, scopeRisk: commonv1.ScopeRisk_SCOPE_RISK_EXACT,
		evidence: commonv1.EvidenceClass_EVIDENCE_CLASS_MODEL,
	}, nil
}

func finishShadowCandidateJob(ctx context.Context, pool *pgxpool.Pool, caseID, releaseID string) error {
	return withTx(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE shadow_candidate_jobs SET state='shadow',release_id=$2,
			attempts=attempts+1,last_error='',updated_at=now() WHERE case_id=$1 AND state IN ('proposed','gated','shadow')`, caseID, releaseID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE investigation_cases SET state='shadow_observing',shadow_release_id=$2,updated_at=now()
			WHERE case_id=$1 AND state NOT IN ('resolved','failed','evidence_expired')`, caseID, releaseID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO case_activities(case_id,kind,ref_id,summary)
			SELECT $1,'shadow_candidate',$2,'真实 Shadow 发布已创建；后续 Canary 或 Enforce 只能由人工治理流程推进'
			WHERE NOT EXISTS (SELECT 1 FROM case_activities WHERE case_id=$1 AND kind='shadow_candidate' AND ref_id=$2)`, caseID, releaseID)
		return err
	})
}
