package replay

import (
	"context"
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"

	artifactv1 "yufeng/proto/gen/artifactv1"

	"yufeng/lib/edgecore"
	"yufeng/lib/kernel"
)

const policySchema = "policy/v1"
const shapeSchema = "shape/v1"

// RunPolicy 用同一规范视图 + 同一引擎发现按检测键回放。
func RunPolicy(ctx context.Context, artifact *artifactv1.Artifact, corpus []Case, det *edgecore.CorazaDetector) (*artifactv1.ReplayReport, error) {
	if artifact == nil || artifact.PayloadSchema != policySchema {
		return nil, fmt.Errorf("replay policy requires %s", policySchema)
	}
	var cand artifactv1.PolicyCandidate
	if err := protojson.Unmarshal(artifact.Payload, &cand); err != nil {
		return nil, fmt.Errorf("policy payload: %w", err)
	}
	if cand.Predicate == nil || len(cand.Predicate.DetectionKeys) == 0 {
		return nil, fmt.Errorf("policy predicate detection_keys required")
	}
	if det == nil {
		var err error
		det, err = edgecore.NewCorazaDetector()
		if err != nil {
			return nil, err
		}
	}
	report := &artifactv1.ReplayReport{CorpusRef: "r5:policy/v1"}
	for _, c := range corpus {
		view := edgecore.Canonicalize(c.Request.Method, c.Request.Path, c.Request.Query, c.Request.Headers, c.Request.Body, edgecore.DefaultInspectionProfile())
		if !edgecore.PolicyCandidateApplies(&cand, c.Request, view) {
			continue
		}
		inspection, err := det.Inspect(ctx, edgecore.InspectionInput{View: view})
		if err != nil {
			return nil, fmt.Errorf("case %s: %w", c.ID, err)
		}
		blocked := policyBlocks(&cand, inspection.Detections, view)
		account(report, c, blocked)
	}
	report.Passed = kernel.GatePassed(report)
	return report, ctx.Err()
}

// policyBlocks 使用生产数据面同一判据判断策略是否拦截当前规范请求。
func policyBlocks(cand *artifactv1.PolicyCandidate, found []edgecore.Detection, view edgecore.CanonicalView) bool {
	return edgecore.PolicyCandidateBlocks(cand, found, view)
}

// account 按样本类别累计回放总量与拦截量。
func account(report *artifactv1.ReplayReport, c Case, blocked bool) {
	switch c.Label {
	case LabelMalicious:
		report.MaliciousTotal++
		if blocked {
			report.MaliciousBlocked++
		}
	case LabelBenign:
		report.BenignTotal++
		if blocked {
			report.BenignBlocked++
		}
	case LabelManagement:
		report.ManagementTotal++
		if blocked {
			report.ManagementBlocked++
		}
	}
}
