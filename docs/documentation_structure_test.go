package docs

import (
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

var markdownLinkPattern = regexp.MustCompile(`!?\[[^\]]*\]\(([^)[:space:]]+)(?:[[:space:]]+"[^"]*")?\)`)

func TestDocumentationIndexListsCentralDocuments(t *testing.T) {
	index := readDoc(t, "README.md")
	for _, target := range []string{
		"architecture.md",
		"api.md",
		"glossary.md",
		"guides/getting-started.md",
		"operations/deployment.md",
		"operations/release-and-delivery.md",
		"development/code-map.md",
		"development/testing/model-scoring-and-agent-rule-datasets.md",
	} {
		if !strings.Contains(index, "]("+target+")") {
			t.Errorf("docs/README.md must link %s", target)
		}
	}
}

func TestCentralDocumentsUseAudienceDirectories(t *testing.T) {
	root := repositoryRoot(t)
	docsRoot := filepath.Join(root, "docs")
	rootDocuments := map[string]bool{
		"README.md":       true,
		"architecture.md": true,
		"api.md":          true,
		"glossary.md":     true,
	}
	audienceDirectories := map[string]bool{
		"guides":      true,
		"operations":  true,
		"development": true,
	}
	err := filepath.WalkDir(docsRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".md" {
			return nil
		}
		relative, err := filepath.Rel(docsRoot, path)
		if err != nil {
			return err
		}
		parts := strings.Split(filepath.ToSlash(relative), "/")
		if len(parts) == 1 {
			if !rootDocuments[parts[0]] {
				t.Errorf("central document must use an audience directory: %s", relative)
			}
			return nil
		}
		if !audienceDirectories[parts[0]] {
			t.Errorf("unsupported documentation audience directory: %s", relative)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryMarkdownRelativeLinksResolve(t *testing.T) {
	root := repositoryRoot(t)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules", ".venv", "__pycache__", "dist":
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".md" {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, match := range markdownLinkPattern.FindAllSubmatch(raw, -1) {
			target := strings.Trim(string(match[1]), "<>")
			if target == "" || strings.HasPrefix(target, "#") {
				continue
			}
			parsed, err := url.Parse(target)
			if err != nil {
				t.Errorf("%s has invalid link %q: %v", relativePath(root, path), target, err)
				continue
			}
			if parsed.Scheme != "" || parsed.Host != "" || strings.HasPrefix(target, "/") {
				continue
			}
			decoded, err := url.PathUnescape(parsed.Path)
			if err != nil {
				t.Errorf("%s has invalid escaped link %q: %v", relativePath(root, path), target, err)
				continue
			}
			if decoded == "" {
				continue
			}
			resolved := filepath.Clean(filepath.Join(filepath.Dir(path), filepath.FromSlash(decoded)))
			if _, err := os.Stat(resolved); err != nil {
				t.Errorf("%s links missing path %q", relativePath(root, path), target)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRetiredDocumentationDoesNotReturn(t *testing.T) {
	root := repositoryRoot(t)
	retiredPaths := []string{
		filepath.Join("docs", "de"+"sign.md"),
		filepath.Join("docs", "yufeng-edge-"+"upgrade.md"),
		filepath.Join("docs", "product-vision-"+"history.md"),
		filepath.Join("docs", "architecture"+".svg"),
		filepath.Join("docs", "performance-"+"baseline.md"),
		"implementation-" + "plan.md",
	}
	retiredNames := make([]string, 0, len(retiredPaths)+4)
	for _, relative := range retiredPaths {
		if _, err := os.Stat(filepath.Join(root, relative)); !os.IsNotExist(err) {
			t.Errorf("retired documentation path exists: %s", relative)
		}
		retiredNames = append(retiredNames, filepath.Base(relative))
	}
	retiredNames = append(retiredNames,
		"deployment-"+"scenarios.md",
		"delivery-"+"evidence.md",
		"docs/"+"code-map.md",
		"docs/"+"test/model-scoring-and-agent-rule-datasets.md",
	)
	textExtensions := map[string]bool{
		".md": true, ".svg": true, ".sh": true, ".go": true, ".proto": true,
		".ts": true, ".tsx": true, ".js": true, ".json": true,
		".yaml": true, ".yml": true, ".toml": true,
	}

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules", ".venv", "__pycache__", "dist":
				return filepath.SkipDir
			}
			return nil
		}
		extension := filepath.Ext(path)
		if !textExtensions[extension] && entry.Name() != ".gitignore" {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, retired := range retiredNames {
			if strings.Contains(string(raw), retired) {
				t.Errorf("%s still references retired documentation %q", relativePath(root, path), retired)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), ".."))
}

func relativePath(root, path string) string {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(relative)
}
