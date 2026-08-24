package replay

import (
	"context"
	"testing"

	artifactv1 "yufeng/proto/gen/artifactv1"

	"yufeng/lib/edgecore"
)

func TestBuiltinCorpusGatesDemoRules(t *testing.T) {
	payload, err := edgecore.MarshalRules([]edgecore.Rule{
		{ID: "sql-union", Pattern: `(?i)union\s+select`},
		{ID: "xss-script", Pattern: `(?i)<script`},
		{ID: "path-traversal", Pattern: `\.\./etc/passwd`},
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := Run(context.Background(), &artifactv1.Artifact{
		Kind: artifactv1.Kind_KIND_RULE, Payload: payload, PayloadSchema: edgecore.RulePayloadSchema,
	}, BuiltinCorpus("asset-1"))
	if err != nil {
		t.Fatal(err)
	}
	if !report.Passed {
		t.Fatalf("门禁应通过: %+v", report)
	}
	if report.MaliciousBlocked != report.MaliciousTotal {
		t.Fatalf("恶意样本应全拦: %+v", report)
	}
	if report.ManagementBlocked != 0 {
		t.Fatalf("管理面应零命中: %+v", report)
	}
}

func TestReplayRejectsFalsePositive(t *testing.T) {
	payload, err := edgecore.MarshalRules([]edgecore.Rule{{ID: "all", Pattern: `/`}})
	if err != nil {
		t.Fatal(err)
	}
	report, err := Run(context.Background(), &artifactv1.Artifact{
		Kind: artifactv1.Kind_KIND_RULE, Payload: payload, PayloadSchema: edgecore.RulePayloadSchema,
	}, BuiltinCorpus("asset-1"))
	if err != nil {
		t.Fatal(err)
	}
	if report.Passed {
		t.Fatalf("误伤规则不应通过: %+v", report)
	}
}
