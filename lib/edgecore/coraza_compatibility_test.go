package edgecore

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"sort"
	"testing"

	"yufeng/lib/kernel"
)

const corazaGoldenUpdateEnvironment = "YUFENG_UPDATE_CORAZA_GOLDEN"

const corazaGoldenSchema = "coraza-detection-golden/v1"

const corazaOfficialEngine = "github.com/corazawaf/coraza/v3@v3.7.0"

type corazaCompatibilityCase struct {
	id      string
	request Request
}

type corazaDetectionGolden struct {
	InspectorID    string  `json:"inspector_id"`
	RuleID         string  `json:"rule_id"`
	Class          string  `json:"class"`
	Score          float64 `json:"score"`
	Location       string  `json:"location"`
	Selector       string  `json:"selector"`
	Phase          string  `json:"phase"`
	Version        string  `json:"version"`
	ManifestDigest string  `json:"manifest_digest"`
	ProfileDigest  string  `json:"profile_digest"`
}

type corazaCoverageGolden struct {
	Target    string `json:"target"`
	Status    string `json:"status"`
	Inspected int64  `json:"inspected"`
	Total     int64  `json:"total"`
}

type corazaCaseGolden struct {
	ID         string                  `json:"id"`
	Rejected   bool                    `json:"rejected"`
	Detections []corazaDetectionGolden `json:"detections"`
	Coverage   []corazaCoverageGolden  `json:"coverage"`
}

type corazaDetectionGoldenFile struct {
	SchemaVersion string             `json:"schema_version"`
	Engine        string             `json:"engine"`
	CRSVersion    string             `json:"crs_version"`
	RxPrefilter   string             `json:"sec_rx_prefilter"`
	Cases         []corazaCaseGolden `json:"cases"`
}

// TestCorazaMaintainedEngineMatchesOfficialDetectionGolden 保证自维护引擎不改变官方基线的发现与覆盖度。
func TestCorazaMaintainedEngineMatchesOfficialDetectionGolden(t *testing.T) {
	detector, err := NewCorazaDetector()
	if err != nil {
		t.Fatal(err)
	}
	actual := captureCorazaDetectionGolden(t, detector)
	path := filepath.Join(inspectionBaselineDir(t), "coraza-v3.7.0-detection-golden.json")
	if os.Getenv(corazaGoldenUpdateEnvironment) == "1" {
		assertOfficialCorazaBaseline(t)
		raw, err := json.MarshalIndent(actual, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var want corazaDetectionGoldenFile
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if want.SchemaVersion != corazaGoldenSchema || want.Engine != corazaOfficialEngine || want.CRSVersion != kernel.CRSVersion || want.RxPrefilter != "Off" {
		t.Fatalf("invalid Coraza golden metadata: %#v", want)
	}
	if !reflect.DeepEqual(actual, want) {
		t.Fatalf("maintained Coraza changed official detection golden\nwant: %#v\ngot:  %#v", want, actual)
	}
}

// TestCorazaMaintainedEngineSupportsOwnedLifecycle 保证自维护引擎可释放实例持有的编译缓存。
func TestCorazaMaintainedEngineSupportsOwnedLifecycle(t *testing.T) {
	if corazaUsesOfficialBaseline() {
		t.Skip("official Coraza v3.7.0 does not expose WAF lifecycle ownership")
	}
	detector, err := NewCorazaDetector()
	if err != nil {
		t.Fatal(err)
	}
	closer, ok := any(detector).(interface{ Close() error })
	if !ok {
		t.Fatal("maintained Coraza detector must expose Close")
	}
	if err := closer.Close(); err != nil {
		t.Fatalf("close Coraza detector: %v", err)
	}
	if err := closer.Close(); err != nil {
		t.Fatalf("close Coraza detector twice: %v", err)
	}
}

func captureCorazaDetectionGolden(t *testing.T, detector *CorazaDetector) corazaDetectionGoldenFile {
	t.Helper()
	golden := corazaDetectionGoldenFile{
		SchemaVersion: corazaGoldenSchema,
		Engine:        corazaOfficialEngine,
		CRSVersion:    kernel.CRSVersion,
		RxPrefilter:   "Off",
	}
	for _, fixture := range corazaCompatibilityCases(t) {
		view := Canonicalize(fixture.request.Method, fixture.request.Path, fixture.request.Query, fixture.request.Headers, fixture.request.Body, DefaultInspectionProfile())
		inspection, err := detector.Inspect(t.Context(), InspectionInput{View: view, ClientAddress: netip.MustParseAddr("192.0.2.10")})
		if err != nil {
			t.Fatalf("%s: %v", fixture.id, err)
		}
		entry := corazaCaseGolden{ID: fixture.id, Rejected: inspection.Rejected}
		for _, detection := range inspection.Detections {
			entry.Detections = append(entry.Detections, corazaDetectionGolden{
				InspectorID: detection.InspectorID, RuleID: detection.RuleID, Class: detection.Class.String(), Score: detection.Score,
				Location: detection.Location.String(), Selector: detection.Selector, Phase: detection.Phase, Version: detection.Version,
				ManifestDigest: detection.ManifestDigest, ProfileDigest: detection.ProfileDigest,
			})
		}
		sort.Slice(entry.Detections, func(i, j int) bool {
			left, right := entry.Detections[i], entry.Detections[j]
			if left.RuleID != right.RuleID {
				return left.RuleID < right.RuleID
			}
			if left.Location != right.Location {
				return left.Location < right.Location
			}
			return left.Selector < right.Selector
		})
		for _, coverage := range inspection.Coverage {
			entry.Coverage = append(entry.Coverage, corazaCoverageGolden{
				Target: coverage.Target.String(), Status: coverage.Status.String(), Inspected: coverage.Inspected, Total: coverage.Total,
			})
		}
		golden.Cases = append(golden.Cases, entry)
	}
	return golden
}

func assertOfficialCorazaBaseline(t *testing.T) {
	t.Helper()
	if !corazaUsesOfficialBaseline() {
		t.Fatal("golden updates require official Coraza v3.7.0 without replace")
	}
}

func corazaUsesOfficialBaseline() bool {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return false
	}
	for _, dependency := range info.Deps {
		if dependency.Path != "github.com/corazawaf/coraza/v3" {
			continue
		}
		return dependency.Version == "v3.7.0" && dependency.Replace == nil
	}
	return false
}

func corazaCompatibilityCases(t *testing.T) []corazaCompatibilityCase {
	t.Helper()
	var cases []corazaCompatibilityCase
	for _, name := range []string{"malicious.jsonl", "benign.jsonl"} {
		raw, err := os.ReadFile(filepath.Join(inspectionBaselineDir(t), "corpus", name))
		if err != nil {
			t.Fatal(err)
		}
		scanner := bufio.NewScanner(bytes.NewReader(raw))
		for scanner.Scan() {
			var row struct {
				ID          string `json:"id"`
				Method      string `json:"method"`
				Path        string `json:"path"`
				Query       string `json:"query"`
				Body        string `json:"body"`
				ContentType string `json:"content_type"`
			}
			if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
				t.Fatal(err)
			}
			headers := map[string]string{"host": "app.example"}
			if row.ContentType != "" {
				headers["content-type"] = row.ContentType
			}
			cases = append(cases, corazaCompatibilityCase{id: row.ID, request: Request{
				Method: row.Method, Path: row.Path, Query: row.Query, Headers: headers, Body: []byte(row.Body),
			}})
		}
		if err := scanner.Err(); err != nil {
			t.Fatal(err)
		}
	}
	for _, fixture := range []corazaCompatibilityCase{
		{id: "capacity-json-4-kib-benign", request: corazaCapacityRequest("application/json", corazaJSONBody(4<<10))},
		{id: "capacity-simple-64-kib-benign", request: corazaCapacityRequest("application/octet-stream", bytes.Repeat([]byte("x"), 64<<10))},
		{id: "capacity-natural-text-64-kib-benign", request: corazaCapacityRequest("text/plain", corazaNaturalTextBody(64<<10))},
		{id: "capacity-base64-64-kib-benign", request: corazaCapacityRequest("application/octet-stream", corazaBase64Body(64<<10))},
		{id: "capacity-binary-64-kib-benign", request: corazaCapacityRequest("application/octet-stream", corazaBinaryBody(64<<10))},
		{id: "capacity-attack-64-kib-head", request: corazaCapacityRequest("application/octet-stream", corazaAttackBody(64<<10, true))},
		{id: "capacity-attack-64-kib-tail", request: corazaCapacityRequest("application/octet-stream", corazaAttackBody(64<<10, false))},
	} {
		cases = append(cases, fixture)
	}
	return cases
}

func corazaCapacityRequest(contentType string, body []byte) Request {
	return Request{
		Method: "POST", Path: "/api/items", Query: "page=2",
		Headers: map[string]string{"host": "app.example", "content-type": contentType}, Body: body,
		ClientAddress: netip.MustParseAddr("192.0.2.10"),
	}
}

func corazaJSONBody(size int) []byte {
	prefix := []byte(`{"value":"`)
	suffix := []byte(`"}`)
	body := make([]byte, 0, size)
	body = append(body, prefix...)
	body = append(body, bytes.Repeat([]byte("x"), size-len(prefix)-len(suffix))...)
	return append(body, suffix...)
}

func corazaNaturalTextBody(size int) []byte {
	phrase := []byte("The quick brown fox crosses the quiet valley while the service records a routine request. ")
	body := bytes.Repeat(phrase, (size+len(phrase)-1)/len(phrase))
	return body[:size]
}

func corazaBase64Body(size int) []byte {
	body := bytes.Repeat([]byte("QUJD"), (size+3)/4)
	return body[:size]
}

func corazaBinaryBody(size int) []byte {
	pattern := []byte{0x01, 0x02, 0x03, 0x04, 0x80, 0x81, 0xfe, 0xff}
	body := bytes.Repeat(pattern, (size+len(pattern)-1)/len(pattern))
	return body[:size]
}

func corazaAttackBody(size int, atHead bool) []byte {
	payload := []byte("id=1+UNION+SELECT+password+FROM+users")
	body := bytes.Repeat([]byte("x"), size)
	position := size - len(payload)
	if atHead {
		position = 0
	}
	copy(body[position:], payload)
	return body
}
