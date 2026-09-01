package edgecore

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"sort"
	"strings"
	"testing"

	"yufeng/lib/kernel"
)

const (
	corazaGoldenUpdateEnvironment = "YUFENG_UPDATE_CORAZA_GOLDEN"
	corazaGoldenSchema            = "coraza-detection-golden/v1"
	corazaOfficialEngine          = "github.com/corazawaf/coraza/v3@v3.7.0"
	corazaOfficialRxPrefilter     = "Off"
	corazaMaintainedEngine        = "github.com/ZionOVO/coraza/v3@v3.7.0-zion.1"
	corazaMaintainedRxPrefilter   = "On"
)

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

// TestCorazaMaintainedEnginePreservesOfficialDetectionGolden 保证自维护引擎不丢失或改写官方基线的发现与覆盖度。
func TestCorazaMaintainedEnginePreservesOfficialDetectionGolden(t *testing.T) {
	detector := newOwnedCorazaForTest(t)
	actualEngine, actualRxPrefilter := corazaMaintainedEngine, corazaMaintainedRxPrefilter
	if corazaUsesOfficialBaseline() {
		// 官方对照副本会移除实验性指令。
		actualEngine, actualRxPrefilter = corazaOfficialEngine, corazaOfficialRxPrefilter
	}
	actual := captureCorazaDetectionGolden(t, detector, actualEngine, actualRxPrefilter)
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
	if want.SchemaVersion != corazaGoldenSchema || want.Engine != corazaOfficialEngine || want.CRSVersion != kernel.CRSVersion || want.RxPrefilter != corazaOfficialRxPrefilter {
		t.Fatalf("invalid Coraza golden metadata: %#v", want)
	}
	if actual.SchemaVersion != corazaGoldenSchema || actual.Engine != actualEngine || actual.CRSVersion != kernel.CRSVersion || actual.RxPrefilter != actualRxPrefilter {
		t.Fatalf("invalid maintained Coraza metadata: %#v", actual)
	}
	blockers, maliciousAdditions := compareCorazaDetectionGolden(want, actual)
	if len(maliciousAdditions) > 0 {
		t.Logf("maintained Coraza added detections on malicious cases:\n%s", strings.Join(maliciousAdditions, "\n"))
	}
	if len(blockers) > 0 {
		t.Fatalf("maintained Coraza regressed official detection golden:\n%s", strings.Join(blockers, "\n"))
	}
}

func compareCorazaDetectionGolden(want, actual corazaDetectionGoldenFile) (blockers, maliciousAdditions []string) {
	if len(want.Cases) != len(actual.Cases) {
		return []string{fmt.Sprintf("case count: want=%d got=%d", len(want.Cases), len(actual.Cases))}, nil
	}
	for i := range want.Cases {
		wantCase, actualCase := want.Cases[i], actual.Cases[i]
		if wantCase.ID != actualCase.ID || wantCase.Rejected != actualCase.Rejected {
			blockers = append(blockers, fmt.Sprintf("case %d identity or rejection: want=%q/%t got=%q/%t", i, wantCase.ID, wantCase.Rejected, actualCase.ID, actualCase.Rejected))
			continue
		}
		if !reflect.DeepEqual(wantCase.Coverage, actualCase.Coverage) {
			blockers = append(blockers, fmt.Sprintf("%s coverage: want=%#v got=%#v", wantCase.ID, wantCase.Coverage, actualCase.Coverage))
		}
		for _, wantDetection := range wantCase.Detections {
			if !containsCorazaDetection(actualCase.Detections, wantDetection) {
				blockers = append(blockers, fmt.Sprintf("%s missing detection: %s", wantCase.ID, corazaDetectionFingerprint(wantDetection)))
			}
		}
		for _, actualDetection := range actualCase.Detections {
			if containsCorazaDetection(wantCase.Detections, actualDetection) {
				continue
			}
			difference := fmt.Sprintf("%s additional detection: %s", actualCase.ID, corazaDetectionFingerprint(actualDetection))
			if corazaCaseContainsAttack(actualCase.ID) {
				maliciousAdditions = append(maliciousAdditions, difference)
			} else {
				blockers = append(blockers, difference)
			}
		}
	}
	return blockers, maliciousAdditions
}

func containsCorazaDetection(detections []corazaDetectionGolden, want corazaDetectionGolden) bool {
	for _, detection := range detections {
		if reflect.DeepEqual(detection, want) {
			return true
		}
	}
	return false
}

func corazaCaseContainsAttack(id string) bool {
	return strings.HasPrefix(id, "m-") || strings.HasPrefix(id, "capacity-attack-")
}

func corazaDetectionFingerprint(detection corazaDetectionGolden) string {
	selectorDigest := sha256.Sum256([]byte(detection.Selector))
	return fmt.Sprintf("inspector=%q rule=%q class=%q score=%g location=%q selector_bytes=%d selector_sha256=%x phase=%q version=%q manifest=%q profile=%q",
		detection.InspectorID, detection.RuleID, detection.Class, detection.Score, detection.Location, len(detection.Selector), selectorDigest,
		detection.Phase, detection.Version, detection.ManifestDigest, detection.ProfileDigest)
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
	if _, ok := detector.waf.(interface{ Close() error }); !ok {
		t.Fatal("maintained Coraza WAF must expose Close")
	}
	if err := detector.Close(); err != nil {
		t.Fatalf("close Coraza detector: %v", err)
	}
	if err := detector.Close(); err != nil {
		t.Fatalf("close Coraza detector twice: %v", err)
	}
}

func TestCorazaDetectionGoldenComparisonRejectsRegressions(t *testing.T) {
	detection := corazaDetectionGolden{
		InspectorID: "crs", RuleID: "942100", Class: "ATTACK_CLASS_SQLI", Score: 1,
		Location: "INSPECTION_SURFACE_QUERY", Selector: "query.id", Phase: "request",
		Version: kernel.CRSVersion, ManifestDigest: kernel.CRSTarballSHA256, ProfileDigest: "profile",
	}
	coverage := []corazaCoverageGolden{{Target: "INSPECTION_SURFACE_QUERY", Status: "COVERAGE_STATUS_FULL", Inspected: 4, Total: 4}}
	want := corazaDetectionGoldenFile{Cases: []corazaCaseGolden{
		{ID: "m-attack", Detections: []corazaDetectionGolden{detection}, Coverage: coverage},
		{ID: "b-benign", Coverage: coverage},
	}}
	additional := detection
	additional.RuleID = "942190"
	changedLocation := detection
	changedLocation.Location = "INSPECTION_SURFACE_BODY"

	tests := []struct {
		name          string
		actual        corazaDetectionGoldenFile
		blockers      int
		maliciousAdds int
	}{
		{name: "equal", actual: cloneCorazaGolden(t, want)},
		{name: "missing official detection", actual: corazaDetectionGoldenFile{Cases: []corazaCaseGolden{
			{ID: "m-attack", Coverage: coverage}, {ID: "b-benign", Coverage: coverage},
		}}, blockers: 1},
		{name: "changed official location", actual: corazaDetectionGoldenFile{Cases: []corazaCaseGolden{
			{ID: "m-attack", Detections: []corazaDetectionGolden{changedLocation}, Coverage: coverage}, {ID: "b-benign", Coverage: coverage},
		}}, blockers: 1, maliciousAdds: 1},
		{name: "benign additional detection", actual: corazaDetectionGoldenFile{Cases: []corazaCaseGolden{
			{ID: "m-attack", Detections: []corazaDetectionGolden{detection}, Coverage: coverage}, {ID: "b-benign", Detections: []corazaDetectionGolden{additional}, Coverage: coverage},
		}}, blockers: 1},
		{name: "malicious additional detection", actual: corazaDetectionGoldenFile{Cases: []corazaCaseGolden{
			{ID: "m-attack", Detections: []corazaDetectionGolden{detection, additional}, Coverage: coverage}, {ID: "b-benign", Coverage: coverage},
		}}, maliciousAdds: 1},
		{name: "coverage changed", actual: corazaDetectionGoldenFile{Cases: []corazaCaseGolden{
			{ID: "m-attack", Detections: []corazaDetectionGolden{detection}, Coverage: []corazaCoverageGolden{{Target: "INSPECTION_SURFACE_QUERY", Status: "COVERAGE_STATUS_PARTIAL", Inspected: 2, Total: 4}}},
			{ID: "b-benign", Coverage: coverage},
		}}, blockers: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			blockers, additions := compareCorazaDetectionGolden(want, test.actual)
			if len(blockers) != test.blockers || len(additions) != test.maliciousAdds {
				t.Fatalf("blockers=%v additions=%v", blockers, additions)
			}
		})
	}
}

func cloneCorazaGolden(t *testing.T, source corazaDetectionGoldenFile) corazaDetectionGoldenFile {
	t.Helper()
	raw, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	var clone corazaDetectionGoldenFile
	if err := json.Unmarshal(raw, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

func captureCorazaDetectionGolden(t *testing.T, detector *CorazaDetector, engine, rxPrefilter string) corazaDetectionGoldenFile {
	t.Helper()
	golden := corazaDetectionGoldenFile{
		SchemaVersion: corazaGoldenSchema,
		Engine:        engine,
		CRSVersion:    kernel.CRSVersion,
		RxPrefilter:   rxPrefilter,
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
	cases = append(cases, []corazaCompatibilityCase{
		{id: "capacity-json-4-kib-benign", request: corazaCapacityRequest("application/json", corazaJSONBody(4<<10))},
		{id: "capacity-simple-64-kib-benign", request: corazaCapacityRequest("application/octet-stream", bytes.Repeat([]byte("x"), 64<<10))},
		{id: "capacity-natural-text-64-kib-benign", request: corazaCapacityRequest("text/plain", corazaNaturalTextBody(64<<10))},
		{id: "capacity-base64-64-kib-benign", request: corazaCapacityRequest("application/octet-stream", corazaBase64Body(64<<10))},
		{id: "capacity-binary-64-kib-benign", request: corazaCapacityRequest("application/octet-stream", corazaBinaryBody(64<<10))},
		{id: "capacity-attack-64-kib-head", request: corazaCapacityRequest("application/octet-stream", corazaAttackBody(64<<10, true))},
		{id: "capacity-attack-64-kib-tail", request: corazaCapacityRequest("application/octet-stream", corazaAttackBody(64<<10, false))},
	}...)
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
