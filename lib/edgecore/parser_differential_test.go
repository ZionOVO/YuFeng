package edgecore

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"yufeng/lib/kernel"
	commonv1 "yufeng/proto/gen/commonv1"
)

func TestParseDiffCorpusCanonicalize(t *testing.T) {
	path := filepath.Join(inspectionBaselineCorpusDir(t), "parse-diff.jsonl")
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck // 只读测试文件在断言完成后尽力清理。
	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		n++
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("row: %v", err)
		}
		id, _ := row["id"].(string)
		view := viewFromParseDiff(t, row)
		expect, _ := row["expect"].(string)
		if !assertParseExpect(t, id, expect, view) {
			t.Fatalf("%s expect %s rejected=%v body=%d cov=%v", id, expect, view.Rejected, len(view.Body), view.Coverage)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if n != 12 {
		t.Fatalf("parse-diff rows=%d", n)
	}
}

func viewFromParseDiff(t *testing.T, row map[string]any) CanonicalView {
	t.Helper()
	method := "POST"
	path, _ := row["path"].(string)
	if path == "" {
		path = "/x"
	}
	query, _ := row["query"].(string)
	headers := map[string]string{}
	if raw, ok := row["headers"].(map[string]any); ok {
		for k, v := range raw {
			headers[k], _ = v.(string)
		}
	}
	if ct, _ := row["content_type"].(string); ct != "" {
		headers["Content-Type"] = ct
	}
	if ce, _ := row["content_encoding"].(string); ce != "" {
		headers["Content-Encoding"] = ce
	}
	if raws, ok := row["raw_headers"].([]any); ok {
		h := http.Header{}
		for _, item := range raws {
			s, _ := item.(string)
			k, v, _ := strings.Cut(s, ":")
			h.Add(strings.TrimSpace(k), strings.TrimSpace(v))
		}
		if len(h["Content-Length"]) > 1 {
			headers["X-Duplicate-Content-Length"] = "1"
			headers["Content-Length"] = h.Get("Content-Length")
		}
	}
	var body []byte
	if s, ok := row["body"].(string); ok {
		body = []byte(s)
	}
	if n, ok := row["body_bytes"].(float64); ok {
		body = bytes.Repeat([]byte("a"), int(n))
	}
	if headers["Content-Encoding"] == "gzip" {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		_, _ = zw.Write(bytes.Repeat([]byte("z"), kernel.EngineBodyLimitBytes*4))
		_ = zw.Close()
		body = buf.Bytes()
	}
	if _, ok := row["pseudo"]; ok {
		return Canonicalize("GET", "/api/items", "id=1", map[string]string{":authority": "app.example"}, nil, DefaultInspectionProfile())
	}
	return Canonicalize(method, path, query, headers, body, DefaultInspectionProfile())
}

func assertParseExpect(t *testing.T, id, expect string, view CanonicalView) bool {
	t.Helper()
	body := CoverageOf(view.Coverage, commonv1.InspectionSurface_INSPECTION_SURFACE_BODY)
	switch expect {
	case "reject_or_absent_body":
		return view.Rejected || body != commonv1.CoverageStatus_COVERAGE_STATUS_FULL
	case "reject":
		return view.Rejected
	case "first=1":
		return view.Query.Get("id") == "1"
	case "two_round_decode":
		return strings.Contains(view.Query.Get("name"), "..") || strings.Contains(view.Query.Encode(), "etc")
	case "error_or_absent":
		return view.Rejected || body == commonv1.CoverageStatus_COVERAGE_STATUS_ABSENT || body == commonv1.CoverageStatus_COVERAGE_STATUS_ERROR
	case "json_error_or_unparsed":
		return view.Rejected || !json.Valid(view.Body)
	case "partial_or_error":
		return body == commonv1.CoverageStatus_COVERAGE_STATUS_PARTIAL || body == commonv1.CoverageStatus_COVERAGE_STATUS_ERROR || view.Rejected
	case "full":
		return body == commonv1.CoverageStatus_COVERAGE_STATUS_FULL
	case "partial":
		return body == commonv1.CoverageStatus_COVERAGE_STATUS_PARTIAL
	case "same_view_as_http11":
		h11 := Canonicalize("GET", "/api/items", "id=1", nil, nil, DefaultInspectionProfile())
		return view.Path == h11.Path && view.Query.Get("id") == h11.Query.Get("id")
	default:
		t.Fatalf("%s unknown expect %s", id, expect)
		return false
	}
}

func inspectionBaselineCorpusDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "procedures", "http-inspection-baseline", "corpus")
}
