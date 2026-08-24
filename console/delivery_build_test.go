package console_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDeliveryBuildUsesOnlyConnectAndRelativeAPI(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	root := filepath.Dir(file)

	connectTS, err := os.ReadFile(filepath.Join(root, "src", "api", "connect.ts"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(connectTS)
	if !strings.Contains(text, "this.baseUrl = opts.baseUrl ?? ''") {
		t.Fatal("ConnectClient must default to a relative base url")
	}
	if strings.Contains(text, "http://localhost:9050") || strings.Contains(text, "https://127.0.0.1:9050") {
		t.Fatal("ConnectClient must not hard-code an absolute brain url")
	}
	viteConfig, err := os.ReadFile(filepath.Join(root, "vite.config.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(viteConfig), "https://127.0.0.1:9050") || !strings.Contains(string(viteConfig), "secure: false") {
		t.Fatal("development proxy must target the local HTTPS brain and accept only its self-signed development certificate")
	}

	indexTS, err := os.ReadFile(filepath.Join(root, "src", "api", "index.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(indexTS), "VITE_API_MODE") || strings.Contains(string(indexTS), "MockClient") {
		t.Fatal("runtime client factory must not contain a mock mode")
	}
	if _, err := os.Stat(filepath.Join(root, "src", "api", "mock")); !os.IsNotExist(err) {
		t.Fatal("runtime api tree must not contain a mock implementation")
	}
	if _, err := os.Stat(filepath.Join(root, "src", "demo")); !os.IsNotExist(err) {
		t.Fatal("production source tree must not contain a design gallery or demo business route")
	}
	loginTS, err := os.ReadFile(filepath.Join(root, "src", "pages", "login", "LoginPage.tsx"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"admin123456", "operator123456", "viewer123456", "演示账户"} {
		if strings.Contains(string(loginTS), forbidden) {
			t.Fatalf("login page must not embed %q", forbidden)
		}
	}
	forbiddenRuntimeText := []string{
		"VITE_API_MODE", "MockClient", "test/fixtures", "admin123456", "operator123456", "viewer123456",
		"GalleryApp", "DEMO_RELEASES", "rel_01J8SQGT",
	}
	if err := filepath.WalkDir(filepath.Join(root, "src"), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() && entry.Name() == "test" {
			return filepath.SkipDir
		}
		if entry.IsDir() || strings.Contains(entry.Name(), ".test.") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, forbidden := range forbiddenRuntimeText {
			if strings.Contains(string(raw), forbidden) {
				t.Fatalf("runtime source %s must not contain %q", path, forbidden)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	index, err := os.ReadFile(filepath.Join(root, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(index), "登录") {
		t.Fatal("index.html must contain 登录 so GET /app/ passes curl without executing JS")
	}
	tailwind, err := os.ReadFile(filepath.Join(root, "tailwind.config.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(tailwind), "demo/gallery") {
		t.Fatal("production theme configuration must not import the design gallery")
	}
}
