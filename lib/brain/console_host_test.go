package brain

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yufeng/lib/observability"
)

func TestHostsConsoleAtApp(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(`<!doctype html><title>御锋控制台</title><form><button>登录</button></form>`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "assets", "app.js"), []byte("export const mode='connect'"), 0o644); err != nil {
		t.Fatal(err)
	}

	h := NewConsoleHandler(dir)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	index := getBody(t, srv.URL+"/app/")
	if !strings.Contains(index, "登录") {
		t.Fatalf("/app/ must serve the login page, body=%q", index)
	}
	unknown := getBody(t, srv.URL+"/app/setup")
	if !strings.Contains(unknown, "登录") {
		t.Fatalf("unknown /app/* must fall back to index.html, body=%q", unknown)
	}
	asset := getBody(t, srv.URL+"/app/assets/app.js")
	if asset != "export const mode='connect'" {
		t.Fatalf("assets must be served as files, body=%q", asset)
	}
}

func TestNewMuxWiresConsoleDir(t *testing.T) {
	raw, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "MountConsole(mux, opts.ConsoleDir)") {
		t.Fatal("NewMux must host console/dist at /app")
	}
}

func TestReadyzStatusOk(t *testing.T) {
	h := observability.Handler(nil, "dev", "v1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("readyz status=%d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"status":"ok"`) {
		t.Fatalf("readyz body=%q", rec.Body.String())
	}
}

func getBody(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck // 只读测试响应在断言完成后尽力清理。
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s status=%d", url, resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
