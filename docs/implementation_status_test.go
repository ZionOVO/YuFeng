package docs

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestImplementedCapabilitiesNotDescribedAsMissing(t *testing.T) {
	architecture := readDoc(t, "architecture.md")
	section7 := section(architecture, "## 7. ", "## 8. ")
	codeMap := readDoc(t, "development/code-map.md")
	deployment := readDoc(t, "operations/deployment.md")
	agents := readRepoFile(t, "AGENTS.md")
	readme := readRepoFile(t, "README.md")
	directorySummary := section(agents, "## 5. 目录速查", "## 6. ")

	for name, document := range map[string]string{
		"architecture.md §7":       section7,
		"development/code-map.md":  codeMap,
		"operations/deployment.md": deployment,
		"AGENTS.md 目录速查":           directorySummary,
		"README.md":                readme,
	} {
		for _, phrase := range []string{
			"贾维斯运行时尚未引入",
			"贾维斯运行时当前无实现",
			"yufeng-run 尚未引入",
			"yufeng-run 当前无实现",
			"Coraza 尚未引入",
			"Coraza 当前无实现",
			"JetStream API 尚未引入",
			"JetStream 尚未引入",
			"JetStream 当前无实现",
			"可恢复补偿、权威审计、Skill 和调查 run 仍是继承工作",
			"工具补偿、完整审计与 Skill 仍未实现",
			"工具补偿与完整审计未完成",
			"固定补偿仍不可恢复；权威审计链、签名 Skill 和调查 run 未完成",
			"仍缺可恢复副作用账本",
			"AgentInteraction、可恢复工具副作用与审批仍未实现",
		} {
			if strings.Contains(document, phrase) {
				t.Errorf("%s must not say %q", name, phrase)
			}
		}
	}
	requireContains(t, "architecture.md §7", section7, "Coraza", "已冻结并引入", "JetStream", "库级已落地")
	requireContains(t, "development/code-map.md", codeMap,
		"yufeng-run", "循环已建", "可恢复补偿事务", "只追加权威审计",
		"DescribeTool", "签名技能", "调查执行实例")
	requireContains(t, "AGENTS.md 目录速查", directorySummary,
		"runtime 已建", "yufeng-run", "yufeng-dataplane")
	if strings.Contains(directorySummary, "yufeng-dataplane（待建") {
		t.Fatal("implemented yufeng-dataplane must not be described as 待建")
	}
}

func TestReleaseDocumentationSeparatesSoftwareReleaseFromDeploymentEvidence(t *testing.T) {
	readme := readRepoFile(t, "README.md")
	releaseDelivery := readDoc(t, "operations/release-and-delivery.md")
	codeMap := readDoc(t, "development/code-map.md")
	deployment := readDoc(t, "operations/deployment.md")
	architecture := readDoc(t, "architecture.md")
	agents := readRepoFile(t, "AGENTS.md")

	requireContains(t, "README.md", readme,
		"v0.1.2", "2026-08-24", "releases/latest", "软件 Release 公开不等于客户现场上线完成")
	for name, document := range map[string]string{
		"AGENTS.md":                               agents,
		"docs/architecture.md":                    architecture,
		"docs/development/code-map.md":            codeMap,
		"docs/operations/deployment.md":           deployment,
		"docs/operations/release-and-delivery.md": releaseDelivery,
	} {
		if !strings.Contains(document, "VERSION") || !strings.Contains(document, "releases/latest") {
			t.Errorf("%s must derive release status from VERSION and GitHub Releases", name)
		}
	}
	requireContains(t, "operations/release-and-delivery.md", releaseDelivery,
		"v0.1.1", "2026-08-24", "正式公开", "v0.1.2", "冻结发布验收合同")
	for _, asset := range []string{
		"yufeng-v0.1.0-linux-amd64.tar.gz",
		"yufeng-v0.1.0-modelside-image-linux-amd64.tar.gz",
		"yufeng-v0.1.0-release-manifest.json",
		"yufeng-v0.1.0-checksums.txt",
		"yufeng-v0.1.1-linux-amd64.tar.gz",
		"yufeng-v0.1.1-modelside-image-linux-amd64.tar.gz",
		"yufeng-v0.1.1-release-manifest.json",
		"yufeng-v0.1.1-checksums.txt",
		"yufeng-v0.1.2-linux-amd64.tar.gz",
		"yufeng-v0.1.2-modelside-image-linux-amd64.tar.gz",
		"yufeng-v0.1.2-release-manifest.json",
		"yufeng-v0.1.2-checksums.txt",
		"yufeng.software-release/v1",
	} {
		if !strings.Contains(releaseDelivery, asset) {
			t.Errorf("release-and-delivery.md must define software release asset or schema %q", asset)
		}
	}
	for _, phrase := range []string{
		"一次",
		"不可变工作流制品",
		"禁止覆盖",
		"重新下载",
		"正常成功路径不追加重复测试",
		"部署验收证据",
		"不撤销或改写已经公开的软件 Release",
	} {
		if !strings.Contains(releaseDelivery, phrase) {
			t.Errorf("release-and-delivery.md must preserve release convergence rule %q", phrase)
		}
	}
	if strings.Contains(releaseDelivery, "live-evidence.tar.gz") || strings.Contains(releaseDelivery, "release-preflight") {
		t.Fatal("software Release must not depend on retired deployment evidence archives")
	}

	for name, document := range map[string]string{
		"README.md":                               readme,
		"AGENTS.md":                               agents,
		"docs/architecture.md":                    architecture,
		"docs/development/code-map.md":            codeMap,
		"docs/operations/deployment.md":           deployment,
		"docs/operations/release-and-delivery.md": releaseDelivery,
	} {
		for _, stale := range []string{
			"软件发布与机器验收已经闭环",
			"软件发布和机器验收已经闭环",
			"当前软件已经完成单站点企业试点的软件发布与机器验收",
			"当前软件版本已经完成单站点企业试点的软件发布与机器验收",
			"单站点企业试点的软件发布和机器验收已经完成",
			"单站点软件发布与机器验收已经完成",
			"该闭环已经通过机器验收",
		} {
			if strings.Contains(document, stale) {
				t.Errorf("%s must not make unconditional current-release claim %q", name, stale)
			}
		}
	}

	requireContains(t, "architecture.md database isolation", architecture,
		"ValidateRestrictedTrafficRole", "yufeng_traffic", "两个数据库连接池")
}

func readDoc(t *testing.T, name string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func readRepoFile(t *testing.T, name string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func section(text, start, end string) string {
	index := strings.Index(text, start)
	if index < 0 {
		return ""
	}
	rest := text[index:]
	if next := strings.Index(rest[len(start):], end); next >= 0 {
		return rest[:len(start)+next]
	}
	return rest
}
