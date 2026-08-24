package docs

import (
	"strings"
	"testing"
)

// TestHumanDeliveryDocsAligned 锁住人工 Edge 生命周期与人机交付文档。
// 缺锚点、引导专节、架构决策记录 036 或把贾维斯写成部署方即失败。
// 产品文档锁语义与路径，不锁历史清单键。
func TestHumanDeliveryDocsAligned(t *testing.T) {
	glossary := readDoc(t, "glossary.md")
	arch := readDoc(t, "architecture.md")
	api := readDoc(t, "api.md")
	design := readDoc(t, "design.md")
	codeMap := readDoc(t, "code-map.md")
	readme := readRepoFile(t, "README.md")

	requireContains(t, "glossary.md#onboarding", glossary, `<a id="onboarding"></a>`, "初次配置引导", "https://127.0.0.1:9050/app/setup")
	requireContains(t, "glossary.md#onboarding-state", glossary, `<a id="onboarding-state"></a>`, "ONBOARDING_STATE_PENDING", "ONBOARDING_STATE_COMPLETED")
	requireContains(t, "glossary.md#manual-edge-lifecycle", glossary, `<a id="manual-edge-lifecycle"></a>`, "Edge 人工生命周期", "Docker Compose")
	requireContains(t, "glossary.md#async-detection-worker", glossary, `<a id="async-detection-worker"></a>`, "yufeng-modelside", "不持 Gate")
	requireContains(t, "glossary.md#human-delivery", glossary, `<a id="human-delivery"></a>`, "人机交付闭环")
	requireContains(t, "glossary.md#modelgateway", glossary, `<a id="modelgateway"></a>`, "模型网关")
	requireContains(t, "glossary.md#model-dialect", glossary, `<a id="model-dialect"></a>`, "MODEL_DIALECT_OPENAI_CHAT", "MODEL_DIALECT_CLAUDE_MESSAGES")

	sec3 := section(arch, "## 3. ", "## 4. ")
	sec54 := section(arch, "### 5.4 ", "### 5.5 ")
	sec7 := section(arch, "## 7. ", "## 8. ")
	requireContains(t, "architecture.md §3", sec3, "ONBOARDING_STATE_COMPLETED", "模型网关", "Edge 生命周期", "架构决策记录 036")
	requireContains(t, "architecture.md §5.4", sec54, "CompleteChat", "-model-url", "Docker")
	if !strings.Contains(sec54, "禁止") || !strings.Contains(sec54, "-model-url") {
		t.Fatal("architecture §5.4 must forbid -model-url")
	}
	requireContains(t, "architecture.md §7", sec7, "引导凭据槽", "Connect-ES", "onboarding-live")
	requireContains(t, "architecture.md ADR-036", arch, "| 036 |", "Edge 生命周期归技术人员", "modelside")

	sec171 := section(api, "### 17.1 ", "### 17.2 ")
	sec179 := section(api, "### 17.9 ", "## 19. ")
	sec19 := section(api, "## 19. ", "## 18. ")
	sec1812 := section(api, "### 18.1.2", "### 18.2")
	requireContains(t, "api.md §17.1", sec171, "/app", "只连接真实 brain")
	requireContains(t, "api.md §17.9", sec179, "https://127.0.0.1:9050/app/setup", "ONBOARDING_STATE_COMPLETED", "onboarding_incomplete")
	requireContains(t, "api.md §19", sec19, "OnboardingGate", "missing_predicates", "ONBOARDING_STATE_PENDING", "PutDeploymentSpecification", "edge_ready")
	requireContains(t, "api.md §18.1.2", sec1812, "KIND_RULE", "rules/v1", "failed_precondition")

	sec6 := section(design, "## 6. ", "## 7. ")
	sec10 := section(design, "## 10. ", "## 11. ")
	sec16 := section(design, "## 16. ", "\x00")
	requireContains(t, "design.md §6", sec6, "yufeng-modelside", "人工安装", "架构决策记录 036")
	requireContains(t, "design.md §10", sec10, "初次配置引导", "brain 托管")
	requireContains(t, "design.md §16", sec16, "贾维斯", "架构决策记录 036", "Edge")

	requireContains(t, "code-map.md", codeMap, "deployment_onboarding", "PutDeploymentSpecification", "Edge 人工部署")
	requireContains(t, "README.md", readme, "人机交付", "Edge 人工生命周期", "compose-up")
	for name, document := range map[string]string{"README.md": readme, "architecture.md": arch, "design.md": design, "code-map.md": codeMap} {
		for _, forbidden := range []string{"unit.ensure_local", "generation.publish_baseline", "edge.probe", "ONBOARDING_DEPLOY"} {
			if strings.Contains(document, forbidden) {
				t.Errorf("%s must not contain retired Edge deployment path %q", name, forbidden)
			}
		}
	}

	forbidden := []struct{ name, text, phrase string }{
		{"README.md", readme, "演示修复循环宣布可交付"},
		{"design.md §10", sec10, "工具网关路径仍未做"},
	}
	for _, f := range forbidden {
		if strings.Contains(f.text, f.phrase) {
			t.Errorf("%s must not say %q", f.name, f.phrase)
		}
	}
	if strings.Contains(sec10, "GetMe.access") && strings.Contains(sec10, "未做（第 7 节勾选超前") {
		t.Error("design.md §10 must not still call login access, list binding trim, or propose-intent reject unfinished")
	}
}

func TestPilotChangeRecordTemplateCoversReleaseFields(t *testing.T) {
	record := readRepoFile(t, "deploy/pilot-change-record.md")
	deployReadme := readRepoFile(t, "deploy/README.md")
	scenarios := readDoc(t, "deployment-scenarios.md")
	requireContains(t, "deploy/pilot-change-record.md", record,
		"候选提交", "发布标签", "入口姿态", "业务 TLS 证书", "真实上游", "可信代理网段",
		"部署规格摘要", "上一版入口配置", "切换负责人", "回退负责人",
		"密钥轮换负责人", "备份恢复负责人", "回退触发条件", "不得记录")
	requireContains(t, "deploy/README.md", deployReadme, "pilot-change-record.md")
	requireContains(t, "deployment-scenarios.md", scenarios, "pilot-change-record.md")
}

func requireContains(t *testing.T, name, blob string, phrases ...string) {
	t.Helper()
	if blob == "" {
		t.Fatalf("%s: empty section", name)
	}
	for _, p := range phrases {
		if !strings.Contains(blob, p) {
			t.Errorf("%s missing %q", name, p)
		}
	}
}
