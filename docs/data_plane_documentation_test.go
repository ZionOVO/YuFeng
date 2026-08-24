package docs

import (
	"strings"
	"testing"
)

// TestDataPlaneDocumentationAligned 锁住数据面升级文档的跨文件语义。
// 缺术语锚点、失败矩阵、架构决策记录或可选模型进程边界时即失败。
func TestDataPlaneDocumentationAligned(t *testing.T) {
	glossary := readDoc(t, "glossary.md")
	arch := readDoc(t, "architecture.md")
	api := readDoc(t, "api.md")
	design := readDoc(t, "design.md")
	codeMap := readDoc(t, "code-map.md")
	upgrade := readDoc(t, "yufeng-edge-upgrade.md")
	implementationPlan := readRepoFile(t, "implementation-plan.md")
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

	sec4 := section(arch, "## 4. ", "## 5. ")
	requireContains(t, "architecture.md §4", sec4, "Inspector", "Gate", "入口姿态", "单元监听计划", "ModelSide", "yufeng-modelside", "纯 Go")
	if !strings.Contains(arch, "| 027 |") {
		t.Fatal("architecture.md must record ADR-027")
	}
	requireContains(t, "architecture.md §13", section(arch, "## 13. ", "默认凭证策略"), "预算", "ModelBypassP99Budget", "ModelSideIngressQueueMax", "ModelSideResultQueueMax", "ExtAuthzHalfOpenPerSec")

	sec21 := section(api, "## 21. ", "\x00")
	requireContains(t, "api.md §21", sec21, "413", "would_have_blocked", "EvidencePolicy", "ForwardPolicy", "tap_silent")
	requireContains(t, "api.md §0.1", section(api, "### 0.1 ", "### 0.2 "), "Inspector", "Gate", "不是接口边界正例")

	sec14iface := section(design, "### 1.4 ", "### 1.5 ")
	requireContains(t, "design.md §1.4", sec14iface, "Inspector", "Gate", "不是目标同步口")
	sec411 := section(design, "### 4.11 ", "## 5. ")
	requireContains(t, "design.md §4.11", sec411, "413", "不当无发现放行", "观察壳不 503")

	sec45 := section(design, "### 4.5 ", "### 4.6 ")
	requireContains(t, "design.md §4.5", sec45, "五层眼睛", "ModelSide", "Brain", "纯 Go", "不进")
	sec461 := section(design, "#### 4.6.1 ", "#### 4.6.2 ")
	requireContains(t, "design.md §4.6.1", sec461, "证据策略", "证据摘要", "转发策略")
	sec47 := section(design, "### 4.7 ", "### 4.8 ")
	requireContains(t, "design.md §4.7", sec47, "413", "侧载只告警")
	sec8 := section(design, "## 8. ", "## 9. ")
	requireContains(t, "design.md §8", sec8, "四姿态壳")
	sec10 := section(design, "## 10. ", "## 11. ")
	requireContains(t, "design.md §10", sec10, "Inspector", "Gate", "yufeng-modelside", "不是平台 Go 二进制")
	sec13 := section(design, "## 13. ", "## 14. ")
	requireContains(t, "design.md §13", sec13, "四姿态", "平台二进制")
	sec14 := section(design, "## 14. ", "## 15. ")
	requireContains(t, "design.md §14", sec14, "已写入", "architecture.md")

	if !strings.Contains(upgrade, "已写入") {
		t.Fatal("yufeng-edge-upgrade.md banner must say 已拍板项已写入权威文档")
	}

	requireContains(t, "implementation-plan.md Edge 模型旁路", implementationPlan,
		"0.2.0 Edge 人工生命周期", "components/modelside", "五种异步旁路场景")
	requireContains(t, "AGENTS.md 项目速览", section(agents, "## 0. ", "## 1. "),
		"流量拦截层的生产语义", "客户上线")

	product := []struct{ name, text string }{
		{"architecture.md", arch},
		{"api.md", api},
		{"design.md", design},
		{"glossary.md", glossary},
		{"code-map.md", codeMap},
		{"README.md", readme},
	}
	legacy := []string{
		"双形态 + 分形态失败语义",
		"双形态：外部授权失败即开",
		"数值待写入 architecture",
		"两种入口必须先变成",
		"八个接口 = 六消息 + 进程内 `edgecore.Detector`",
		"反代形态仍是单请求失败即关",
	}
	for _, p := range product {
		for _, phrase := range legacy {
			if strings.Contains(p.text, phrase) {
				t.Errorf("%s must not keep old target phrase %q", p.name, phrase)
			}
		}
	}
}
