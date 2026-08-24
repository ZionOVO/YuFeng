// Package replay 实现发布前回放门禁：与 edge 共用同一裁决纯函数。
package replay

import (
	"context"
	"fmt"

	artifactv1 "yufeng/proto/gen/artifactv1"

	"yufeng/lib/edgecore"
	"yufeng/lib/kernel"
)

// 语料标签（Case.Label 的合法取值）。
const (
	LabelMalicious  = "malicious"
	LabelBenign     = "benign"
	LabelManagement = "management"
)

// Case 是一条回放样本。
type Case struct {
	ID      string
	Label   string // 取值为 LabelMalicious / LabelBenign / LabelManagement
	Request edgecore.Request
}

// BuiltinCorpus 返回 v1 内置语料：恶意样本、良性样本与管理面样本。
func BuiltinCorpus(assetID string) []Case {
	return []Case{
		{ID: "malicious:sql", Label: LabelMalicious, Request: edgecore.Request{AssetID: assetID, Method: "GET", Path: "/api/items", Query: "id=1+UNION+SELECT+password"}},
		{ID: "malicious:xss", Label: LabelMalicious, Request: edgecore.Request{AssetID: assetID, Method: "POST", Path: "/api/items", Query: "q=<script>alert(1)</script>"}},
		{ID: "malicious:traversal", Label: LabelMalicious, Request: edgecore.Request{AssetID: assetID, Method: "GET", Path: "/download/../../etc/passwd"}},
		{ID: "benign:page", Label: LabelBenign, Request: edgecore.Request{AssetID: assetID, Method: "GET", Path: "/api/items", Query: "page=2"}},
		{ID: "benign:health", Label: LabelBenign, Request: edgecore.Request{AssetID: assetID, Method: "GET", Path: "/healthz"}},
		{ID: "management:livez", Label: LabelManagement, Request: edgecore.Request{AssetID: assetID, Method: "GET", Path: "/livez"}},
		{ID: "management:readyz", Label: LabelManagement, Request: edgecore.Request{AssetID: assetID, Method: "GET", Path: "/readyz"}},
		{ID: "management:metrics", Label: LabelManagement, Request: edgecore.Request{AssetID: assetID, Method: "GET", Path: "/metrics"}},
	}
}

// Run 按 payload_schema 选择回放：rules/v1 用正则检测器，policy/v1 用检测键匹配。
func Run(ctx context.Context, artifact *artifactv1.Artifact, corpus []Case) (*artifactv1.ReplayReport, error) {
	if artifact == nil {
		return nil, fmt.Errorf("artifact is required")
	}
	switch artifact.PayloadSchema {
	case policySchema:
		return RunPolicy(ctx, artifact, corpus, nil)
	case shapeSchema:
		return RunShape(ctx, artifact, corpus)
	case edgecore.RulePayloadSchema:
	default:
		return nil, fmt.Errorf("replay only supports %s rule artifacts", edgecore.RulePayloadSchema)
	}
	rules, err := edgecore.ParseRules(artifact.Payload)
	if err != nil {
		return nil, err
	}
	base, err := edgecore.NewRuleDetector(artifact.Id, rules)
	if err != nil {
		return nil, err
	}
	var detector edgecore.Detector = base
	if artifact.Scope != nil && artifact.Scope.RouteSelector != "" {
		detector = edgecore.NewScoped(detector, artifact.Scope.RouteSelector)
	}
	report := &artifactv1.ReplayReport{CorpusRef: "builtin:l1-rules-v1"}
	for _, c := range corpus {
		// 门禁语境下检测器出错必须让回放失败：静默按"未拦截"计账
		// 会把坏制品放进通过分支。
		v, err := detector.Evaluate(ctx, c.Request)
		if err != nil {
			return nil, fmt.Errorf("case %s: detector: %w", c.ID, err)
		}
		blocked := v.Action == edgecore.ActionBlock
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
		default:
			return nil, fmt.Errorf("case %s: unknown label %q", c.ID, c.Label)
		}
	}
	report.Passed = kernel.GatePassed(report)
	return report, nil
}
