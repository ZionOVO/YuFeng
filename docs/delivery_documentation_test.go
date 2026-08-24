package docs

import (
	"strings"
	"testing"
)

// TestHumanDeliveryDocsAligned 锁住人工 Edge 生命周期与人机交付边界。
// 缺引导契约、人工部署说明或把贾维斯写成部署方时即失败。
func TestHumanDeliveryDocsAligned(t *testing.T) {
	glossary := readDoc(t, "glossary.md")
	architecture := readDoc(t, "architecture.md")
	api := readDoc(t, "api.md")
	codeMap := readDoc(t, "development/code-map.md")
	deployment := readDoc(t, "operations/deployment.md")
	gettingStarted := readDoc(t, "guides/getting-started.md")
	readme := readRepoFile(t, "README.md")

	requireContains(t, "glossary.md#onboarding", glossary, `<a id="onboarding"></a>`, "初次配置引导", "https://127.0.0.1:9050/app/setup")
	requireContains(t, "glossary.md#onboarding-state", glossary, `<a id="onboarding-state"></a>`, "ONBOARDING_STATE_PENDING", "ONBOARDING_STATE_COMPLETED")
	requireContains(t, "glossary.md#manual-edge-lifecycle", glossary, `<a id="manual-edge-lifecycle"></a>`, "Edge 人工生命周期", "Docker Compose")
	requireContains(t, "glossary.md#async-detection-worker", glossary, `<a id="async-detection-worker"></a>`, "yufeng-modelside", "不持 Gate")
	requireContains(t, "glossary.md#human-delivery", glossary, `<a id="human-delivery"></a>`, "人机交付闭环")
	requireContains(t, "glossary.md#modelgateway", glossary, `<a id="modelgateway"></a>`, "模型网关")
	requireContains(t, "glossary.md#model-dialect", glossary, `<a id="model-dialect"></a>`, "MODEL_DIALECT_OPENAI_CHAT", "MODEL_DIALECT_CLAUDE_MESSAGES")

	section3 := section(architecture, "## 3. ", "## 4. ")
	section54 := section(architecture, "### 5.4 ", "### 5.5 ")
	section7 := section(architecture, "## 7. ", "## 8. ")
	requireContains(t, "architecture.md §3", section3, "ONBOARDING_STATE_COMPLETED", "模型网关", "Edge 生命周期", "架构决策记录 036")
	requireContains(t, "architecture.md §5.4", section54, "CompleteChat", "-model-url", "Docker")
	if !strings.Contains(section54, "禁止") || !strings.Contains(section54, "-model-url") {
		t.Fatal("architecture §5.4 must forbid -model-url")
	}
	requireContains(t, "architecture.md §7", section7, "引导凭据槽", "Connect-ES", "onboarding-live")
	requireContains(t, "architecture.md ADR-036", architecture, "| 036 |", "Edge 生命周期归技术人员", "modelside")

	section171 := section(api, "### 17.1 ", "### 17.2 ")
	section179 := section(api, "### 17.9 ", "## 19. ")
	section19 := section(api, "## 19. ", "## 18. ")
	section1812 := section(api, "### 18.1.2", "### 18.2")
	requireContains(t, "api.md §17.1", section171, "/app", "只连接真实 brain")
	requireContains(t, "api.md §17.9", section179, "https://127.0.0.1:9050/app/setup", "ONBOARDING_STATE_COMPLETED", "onboarding_incomplete")
	requireContains(t, "api.md §19", section19, "OnboardingGate", "missing_predicates", "ONBOARDING_STATE_PENDING", "PutDeploymentSpecification", "edge_ready")
	requireContains(t, "api.md §18.1.2", section1812, "KIND_RULE", "rules/v1", "failed_precondition")

	requireContains(t, "operations/deployment.md", deployment,
		"yufeng-modelside", "人工安装", "Brain、贾维斯", "部署规格", "主动注册")
	requireContains(t, "development/code-map.md", codeMap,
		"deployment_onboarding", "PutDeploymentSpecification", "Edge 人工部署")
	requireContains(t, "guides/getting-started.md", gettingStarted,
		"make compose-up", "六步引导", "人工安装数据面")
	requireContains(t, "README.md", readme,
		"部署与上线", "软件 Release 公开不等于客户现场上线完成")

	for name, document := range map[string]string{
		"README.md":                 readme,
		"architecture.md":           architecture,
		"development/code-map.md":   codeMap,
		"operations/deployment.md":  deployment,
		"guides/getting-started.md": gettingStarted,
	} {
		for _, forbidden := range []string{"unit.ensure_local", "generation.publish_baseline", "edge.probe", "ONBOARDING_DEPLOY"} {
			if strings.Contains(document, forbidden) {
				t.Errorf("%s must not contain retired Edge deployment path %q", name, forbidden)
			}
		}
	}
}

func TestPilotChangeRecordTemplateCoversReleaseFields(t *testing.T) {
	record := readRepoFile(t, "deploy/pilot-change-record.md")
	deployReadme := readRepoFile(t, "deploy/README.md")
	deployment := readDoc(t, "operations/deployment.md")
	requireContains(t, "deploy/pilot-change-record.md", record,
		"候选提交", "发布标签", "入口姿态", "业务 TLS 证书", "真实上游", "可信代理网段",
		"部署规格摘要", "上一版入口配置", "切换负责人", "回退负责人",
		"密钥轮换负责人", "备份恢复负责人", "回退触发条件", "不得记录")
	requireContains(t, "deploy/README.md", deployReadme, "pilot-change-record.md")
	requireContains(t, "operations/deployment.md", deployment, "pilot-change-record.md")
}

func requireContains(t *testing.T, name, blob string, phrases ...string) {
	t.Helper()
	if blob == "" {
		t.Fatalf("%s: empty section", name)
	}
	for _, phrase := range phrases {
		if !strings.Contains(blob, phrase) {
			t.Errorf("%s missing %q", name, phrase)
		}
	}
}
