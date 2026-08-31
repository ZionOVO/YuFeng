package httpinspectionbaseline_test

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"yufeng/lib/kernel"
)

func TestCoreRuleSetManifestIsPinned(t *testing.T) {
	dir := testDir(t)
	raw, err := os.ReadFile(filepath.Join(dir, "core-rule-set-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var man struct {
		Version       string   `json:"version"`
		TarballSHA256 string   `json:"tarball_sha256"`
		Paranoia      int      `json:"paranoia"`
		IncludeFiles  []string `json:"include_files"`
		GoModule      string   `json:"go_module"`
		Engine        string   `json:"engine"`
		RxPrefilter   string   `json:"sec_rx_prefilter"`
	}
	if err := json.Unmarshal(raw, &man); err != nil {
		t.Fatal(err)
	}
	if man.Version != kernel.CRSVersion {
		t.Fatalf("crs version manifest=%s kernel=%s", man.Version, kernel.CRSVersion)
	}
	if man.TarballSHA256 != kernel.CRSTarballSHA256 {
		t.Fatalf("crs sha mismatch")
	}
	if man.Paranoia != kernel.CRSParanoia {
		t.Fatalf("paranoia=%d", man.Paranoia)
	}
	if man.GoModule != kernel.CRSGoModule {
		t.Fatalf("go module=%s", man.GoModule)
	}
	if man.Engine != "github.com/ZionOVO/coraza/v3@v3.7.1-0.20260831022307-151f051001b8" {
		t.Fatalf("engine=%s", man.Engine)
	}
	if man.RxPrefilter != "Off" {
		t.Fatalf("sec_rx_prefilter=%s", man.RxPrefilter)
	}
	if len(man.IncludeFiles) != 9 {
		t.Fatalf("include_files=%d", len(man.IncludeFiles))
	}
}

func TestInspectionCorpusSampleCounts(t *testing.T) {
	dir := testDir(t)
	raw, err := os.ReadFile(filepath.Join(dir, "samples.json"))
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Malicious      int               `json:"malicious"`
		Benign         int               `json:"benign"`
		Management     int               `json:"management"`
		ParseDiff      int               `json:"parse_diff"`
		ProfileSamples int               `json:"profile_samples"`
		Files          map[string]string `json:"files"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	want := map[string]int{
		"malicious":       spec.Malicious,
		"benign":          spec.Benign,
		"management":      spec.Management,
		"parse_diff":      spec.ParseDiff,
		"profile_samples": spec.ProfileSamples,
	}
	if spec.Malicious != 15 || spec.Benign != 15 || spec.Management != 5 || spec.ParseDiff != 12 || spec.ProfileSamples != 5 {
		t.Fatalf("frozen counts changed: %+v", spec)
	}
	for key, n := range want {
		got := countJSONL(t, filepath.Join(dir, spec.Files[key]))
		if got != n {
			t.Fatalf("%s: want %d got %d", key, n, got)
		}
	}
}

func countJSONL(t *testing.T, path string) int {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck // 只读测试文件在断言完成后尽力清理。
	n := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if len(sc.Bytes()) > 0 {
			n++
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return n
}

func testDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	return filepath.Dir(file)
}
