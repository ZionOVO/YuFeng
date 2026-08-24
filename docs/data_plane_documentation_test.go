package docs

import (
	"strings"
	"testing"
)

// TestDataPlaneDocumentationAligned 锁住权威数据面语义。
// 缓解条件、失败矩阵、可选模型进程或实现状态缺失时即失败。
func TestDataPlaneDocumentationAligned(t *testing.T) {
	glossary := readDoc(t, "glossary.md")
	architecture := readDoc(t, "architecture.md")
	api := readDoc(t, "api.md")
	codeMap := readDoc(t, "development/code-map.md")
	deployment := readDoc(t, "operations/deployment.md")
	agents := readRepoFile(t, "AGENTS.md")
	readme := readRepoFile(t, "README.md")

	requireContains(t, "glossary.md#inspector", glossary, `<a id="inspector"></a>`, "返回拦截动作")
	requireContains(t, "glossary.md#gate", glossary, `<a id="gate"></a>`, "唯一能对本次请求给出处置动作")
	requireContains(t, "glossary.md#ingress-posture", glossary, `<a id="ingress-posture"></a>`, "侧载只告警")
	requireContains(t, "glossary.md#unit-listen-plan", glossary, `<a id="unit-listen-plan"></a>`, "不进资产世代")
	requireContains(t, "glossary.md#evidence-policy", glossary, `<a id="evidence-policy"></a>`, "home")
	requireContains(t, "glossary.md#evidence-digest", glossary, `<a id="evidence-digest"></a>`, "span_sha256")
	requireContains(t, "glossary.md#forward-policy", glossary, `<a id="forward-policy"></a>`, "AGENT_INVESTIGATE", "不负责模型推理")
	requireContains(t, "glossary.md#async-detection-worker", glossary, `<a id="async-detection-worker"></a>`, "ModelSide", "不持 Gate", "消息服务器", "数据库")
	requireContains(t, "glossary.md#http-inspection-profile", glossary, `<a id="http-inspection-profile"></a>`, "四种入口壳")
	requireContains(t, "glossary.md#local-async-bypass", glossary, `<a id="local-async-bypass"></a>`, "纯 Go")

	section4 := section(architecture, "## 4. ", "## 5. ")
	requireContains(t, "architecture.md §4", section4,
		"Inspector", "Gate", "入口姿态", "单元监听计划", "边缘观察与中台研判",
		"资产世代", "ModelSide", "yufeng-modelside", "纯 Go")
	if !strings.Contains(architecture, "| 027 |") {
		t.Fatal("architecture.md must record ADR-027")
	}
	requireContains(t, "architecture.md §13", section(architecture, "## 13. ", "默认凭证策略"),
		"预算", "ModelBypassP99Budget", "ModelSideIngressQueueMax", "ModelSideResultQueueMax", "ExtAuthzHalfOpenPerSec")

	section1812 := section(api, "### 18.1.2", "### 18.2")
	requireContains(t, "api.md §18.1.2", section1812,
		"TRIAGE_REASON_SUSPECTED_MISS", "MissEvidenceType", "人工报告", "漏洞回放或复现",
		"可信情报", "已签名模型档案")
	section21 := section(api, "## 21. ", "\x00")
	requireContains(t, "api.md §21", section21,
		"OBSERVATION_STATE_SYNC_DETECTED", "TRIAGE_REASON_INSPECTION_INCOMPLETE", "COVERAGE_STATUS_UNSUPPORTED",
		"413", "would_have_blocked", "EvidencePolicy", "ForwardPolicy", "tap_silent", "ModelSide")
	requireContains(t, "api.md §0.1", section(api, "### 0.1 ", "### 0.2 "), "Inspector", "Gate", "不是接口边界正例")

	requireContains(t, "development/code-map.md", codeMap,
		"Inspector", "Gate", "ModelSide", "Edge 邻近异步模型旁路")
	requireContains(t, "operations/deployment.md", deployment,
		"反向代理", "Envoy 外部授权", "异步模型数据边界", "故障与回退", "现场变更记录")
	requireContains(t, "AGENTS.md 项目速览", section(agents, "## 0. ", "## 1. "),
		"流量拦截层的架构语义", "网络结果与失败语义", "客户上线")
	requireContains(t, "README.md", readme,
		"软件 Release 公开不等于客户现场上线完成", "当前能力边界")

	product := []struct {
		name string
		text string
	}{
		{"architecture.md", architecture},
		{"api.md", api},
		{"glossary.md", glossary},
		{"development/code-map.md", codeMap},
		{"operations/deployment.md", deployment},
		{"README.md", readme},
	}
	for _, document := range product {
		for _, phrase := range []string{
			"双形态 + 分形态失败语义",
			"双形态：外部授权失败即开",
			"数值待写入 architecture",
			"两种入口必须先变成",
			"八个接口 = 六消息 + 进程内 `edgecore.Detector`",
			"反代形态仍是单请求失败即关",
		} {
			if strings.Contains(document.text, phrase) {
				t.Errorf("%s must not keep old target phrase %q", document.name, phrase)
			}
		}
	}
}
