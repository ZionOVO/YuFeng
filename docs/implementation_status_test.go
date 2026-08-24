package docs

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestImplementedCapabilitiesNotDescribedAsMissing(t *testing.T) {
	arch := readDoc(t, "architecture.md")
	sec7 := section(arch, "## 7. ", "## 8. ")
	codeMap := readDoc(t, "code-map.md")
	design := readDoc(t, "design.md")
	scenarios := readDoc(t, "deployment-scenarios.md")
	agents := readRepoFile(t, "AGENTS.md")
	readme := readRepoFile(t, "README.md")
	dirQuick := section(agents, "## 5. 目录速查", "## 6. ")

	forbidden := []string{
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
		"无完整 steer/follow-up 交互面、可恢复工具 intent/effect/settlement、DescribeTool、Skill 渐进披露",
		"仍缺可恢复副作用账本",
		"AgentInteraction、可恢复工具副作用与审批仍未实现",
		"工具 intent/settlement 权威账本、子 run 等待、审批与压缩仍按后续条目实现",
	}
	blobs := []struct{ name, text string }{
		{"architecture.md §7", sec7},
		{"code-map.md", codeMap},
		{"design.md", design},
		{"deployment-scenarios.md", scenarios},
		{"AGENTS.md 目录速查", dirQuick},
		{"README.md", readme},
	}
	for _, b := range blobs {
		for _, phrase := range forbidden {
			if strings.Contains(b.text, phrase) {
				t.Errorf("%s must not say %q", b.name, phrase)
			}
		}
	}
	if !strings.Contains(sec7, "Coraza") || !strings.Contains(sec7, "已冻结并引入") {
		t.Fatal("architecture §7 must record Coraza as introduced")
	}
	if !strings.Contains(sec7, "JetStream") || !strings.Contains(sec7, "库级已落地") {
		t.Fatal("architecture §7 must record JetStream API as implemented at library level")
	}
	if !strings.Contains(codeMap, "yufeng-run") || !strings.Contains(codeMap, "循环已建") {
		t.Fatal("code-map must record yufeng-run / runtime as built")
	}
	if !strings.Contains(dirQuick, "runtime 已建") {
		t.Fatal("AGENTS.md directory must record jarvis runtime as built")
	}
	if !strings.Contains(dirQuick, "yufeng-run") {
		t.Fatal("AGENTS.md directory must list yufeng-run")
	}
	if !strings.Contains(dirQuick, "yufeng-dataplane") {
		t.Fatal("AGENTS.md directory must list yufeng-dataplane")
	}
	if strings.Contains(dirQuick, "yufeng-dataplane（待建") {
		t.Fatal("implemented yufeng-dataplane must not be described as 待建")
	}
	for _, item := range []string{"可恢复补偿事务", "只追加权威审计", "DescribeTool", "签名技能", "调查执行实例"} {
		if !strings.Contains(codeMap, item) {
			t.Errorf("code-map must record implemented agent capability %q", item)
		}
	}
	for _, item := range []string{"可恢复补偿", "权威审计", "签名工具描述与技能"} {
		if !strings.Contains(readme, item) {
			t.Errorf("README.md must record implemented agent capability %q", item)
		}
	}
}

func TestReleaseDocumentationSeparatesSoftwareReleaseFromDeploymentEvidence(t *testing.T) {
	readme := readRepoFile(t, "README.md")
	delivery := readDoc(t, "delivery-evidence.md")
	codeMap := readDoc(t, "code-map.md")
	scenarios := readDoc(t, "deployment-scenarios.md")
	design := readDoc(t, "design.md")
	architecture := readDoc(t, "architecture.md")
	api := readDoc(t, "api.md")
	glossary := readDoc(t, "glossary.md")
	agents := readRepoFile(t, "AGENTS.md")

	for name, document := range map[string]string{
		"README.md":                    readme,
		"AGENTS.md":                    agents,
		"docs/architecture.md":         architecture,
		"docs/api.md":                  api,
		"docs/design.md":               design,
		"docs/code-map.md":             codeMap,
		"docs/deployment-scenarios.md": scenarios,
	} {
		if !strings.Contains(document, "VERSION") || !strings.Contains(document, "releases/latest") {
			t.Errorf("%s must derive current release status from VERSION and GitHub Releases", name)
		}
	}
	if strings.Contains(readme, "| 发布版本 | `v0.0.2`") {
		t.Fatal("README.md must not hard-code a historical release as current")
	}
	for _, asset := range []string{
		"yufeng-v0.1.0-linux-amd64.tar.gz",
		"yufeng-v0.1.0-modelside-image-linux-amd64.tar.gz",
		"yufeng-v0.1.0-release-manifest.json",
		"yufeng-v0.1.0-checksums.txt",
		"yufeng.software-release/v1",
	} {
		if !strings.Contains(delivery, asset) {
			t.Errorf("delivery-evidence.md must define software release asset or schema %q", asset)
		}
	}
	for _, phrase := range []string{
		"一次",
		"不可变工作流制品",
		"禁止覆盖",
		"重新下载",
		"部署验收证据",
		"不撤销或改写已经公开的软件 Release",
	} {
		if !strings.Contains(delivery, phrase) {
			t.Errorf("delivery-evidence.md must preserve release convergence rule %q", phrase)
		}
	}
	if strings.Contains(delivery, "live-evidence.tar.gz") || strings.Contains(delivery, "release-preflight") {
		t.Fatal("software Release must not depend on retired deployment evidence archives")
	}
	for name, document := range map[string]string{
		"README.md":                    readme,
		"AGENTS.md":                    agents,
		"docs/architecture.md":         architecture,
		"docs/api.md":                  api,
		"docs/design.md":               design,
		"docs/glossary.md":             glossary,
		"docs/code-map.md":             codeMap,
		"docs/deployment-scenarios.md": scenarios,
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

	if strings.Contains(design, "数据库角色分离（遥测写者不能改发布账）**未做**") {
		t.Fatal("design.md must not describe implemented database role isolation as missing")
	}
	for _, phrase := range []string{"yufeng_traffic", "insufficient_privilege", "两个数据库连接池"} {
		if !strings.Contains(design, phrase) {
			t.Errorf("design.md must record database isolation boundary %q", phrase)
		}
	}
	for _, phrase := range []string{"ValidateRestrictedTrafficRole", "yufeng_traffic", "两个数据库连接池"} {
		if !strings.Contains(architecture, phrase) {
			t.Errorf("architecture.md must record implemented database isolation boundary %q", phrase)
		}
	}
}

func readDoc(t *testing.T, name string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), name))
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
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func section(text, start, end string) string {
	i := strings.Index(text, start)
	if i < 0 {
		return ""
	}
	rest := text[i:]
	if j := strings.Index(rest[len(start):], end); j >= 0 {
		return rest[:len(start)+j]
	}
	return rest
}
