package edgecore

import (
	"bufio"
	"encoding/json"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestCorazaDetectionOnlyNoBlock(t *testing.T) {
	d, err := NewCorazaDetector()
	if err != nil {
		t.Fatal(err)
	}
	v, err := d.Evaluate(t.Context(), Request{Method: "GET", Path: "/api/items", Query: "id=1+UNION+SELECT+password"})
	if err != nil {
		t.Fatal(err)
	}
	if v.Action == ActionBlock {
		t.Fatal("coraza must not block")
	}
	dets, err := d.Detect(Request{Method: "GET", Path: "/api/items", Query: "id=1+UNION+SELECT+password"})
	if err != nil {
		t.Fatal(err)
	}
	if len(dets) == 0 {
		t.Fatal("sqli sample should produce a detection key")
	}
}

func TestCorazaUsesClientSourceWithoutChangingDetectionKey(t *testing.T) {
	d, err := NewCorazaDetector()
	if err != nil {
		t.Fatal(err)
	}
	base := Request{Method: "GET", Path: "/api/items", Query: "id=1+UNION+SELECT+password"}
	first := base
	first.ClientAddress = netip.MustParseAddr("198.51.100.4")
	second := base
	second.ClientAddress = netip.MustParseAddr("203.0.113.9")
	a, err := d.Detect(first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := d.Detect(second)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) == 0 || !reflect.DeepEqual(a, b) {
		t.Fatalf("client source changed detection keys: %#v vs %#v", a, b)
	}
}

func TestCorazaCoreRuleSetCorpus(t *testing.T) {
	d, err := NewCorazaDetector()
	if err != nil {
		t.Fatal(err)
	}
	dir := inspectionBaselineDir(t)
	mal := readJSONL(t, filepath.Join(dir, "corpus", "malicious.jsonl"))
	for _, row := range mal {
		req := Request{Method: row["method"], Path: row["path"], Query: row["query"], Body: []byte(row["body"])}
		dets, err := d.Detect(req)
		if err != nil {
			t.Fatalf("%s detect: %v", row["id"], err)
		}
		if len(dets) == 0 {
			t.Fatalf("%s expected detection", row["id"])
		}
		prefix := row["expect_rule_prefix"]
		if prefix != "" && !hasPrefix(dets, prefix) {
			t.Fatalf("%s want rule prefix %s got %#v", row["id"], prefix, dets)
		}
	}
	ben := readJSONL(t, filepath.Join(dir, "corpus", "benign.jsonl"))
	for _, row := range ben {
		req := Request{Method: row["method"], Path: row["path"], Query: row["query"], Body: []byte(row["body"])}
		dets, err := d.Detect(req)
		if err != nil {
			t.Fatalf("%s detect: %v", row["id"], err)
		}
		if len(dets) != 0 {
			t.Fatalf("%s benign hit %#v", row["id"], dets)
		}
	}
}

func hasPrefix(dets []Detection, prefix string) bool {
	for _, d := range dets {
		if strings.HasPrefix(d.RuleID, prefix) {
			return true
		}
	}
	return false
}

func inspectionBaselineDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "procedures", "http-inspection-baseline")
}

func readJSONL(t *testing.T, path string) []map[string]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck // 只读测试文件在断言完成后尽力清理。
	var rows []map[string]string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal(sc.Bytes(), &raw); err != nil {
			t.Fatal(err)
		}
		row := map[string]string{}
		for k, v := range raw {
			if s, ok := v.(string); ok {
				row[k] = s
			}
		}
		rows = append(rows, row)
	}
	return rows
}

func TestCRSAutoGovernRule(t *testing.T) {
	if !CRSAutoGovernRule("942100") || !CRSAutoGovernRule("941100") || !CRSAutoGovernRule("930120") {
		t.Fatal("attack-class keys must be auto-governable")
	}
	if CRSAutoGovernRule("920100") || CRSAutoGovernRule("913100") || CRSAutoGovernRule("949110") {
		t.Fatal("protocol/scanner/anomaly keys must not auto-govern")
	}
}
