package edgecore

import (
	"strings"
	"testing"

	commonv1 "yufeng/proto/gen/commonv1"

	"yufeng/lib/kernel"
)

func TestCanonicalizeBodyCoverage(t *testing.T) {
	p := DefaultInspectionProfile()
	full := Canonicalize("GET", "/api/items", "id=1", nil, []byte("hello"), p)
	if CoverageOf(full.Coverage, commonv1.InspectionSurface_INSPECTION_SURFACE_BODY) != commonv1.CoverageStatus_COVERAGE_STATUS_FULL {
		t.Fatal("short body should be FULL")
	}
	absent := Canonicalize("GET", "/api/items", "", nil, nil, p)
	if CoverageOf(absent.Coverage, commonv1.InspectionSurface_INSPECTION_SURFACE_BODY) != commonv1.CoverageStatus_COVERAGE_STATUS_ABSENT {
		t.Fatal("empty body should be ABSENT")
	}
	big := make([]byte, kernel.EngineBodyLimitBytes+1)
	partial := Canonicalize("POST", "/x", "", nil, big, p)
	if CoverageOf(partial.Coverage, commonv1.InspectionSurface_INSPECTION_SURFACE_BODY) != commonv1.CoverageStatus_COVERAGE_STATUS_PARTIAL {
		t.Fatal("oversize body should be PARTIAL")
	}
	if len(partial.Body) != kernel.EngineBodyLimitBytes {
		t.Fatalf("truncated to %d", len(partial.Body))
	}
}

func TestCanonicalizeDuplicateQueryFirst(t *testing.T) {
	v := Canonicalize("GET", "/x", "id=1&id=2", nil, nil, DefaultInspectionProfile())
	if got := v.Query.Get("id"); got != "1" {
		t.Fatalf("duplicate query first=%s", got)
	}
}

func TestCanonicalizeCLTEReject(t *testing.T) {
	v := Canonicalize("POST", "/x", "", map[string]string{"Content-Length": "4", "Transfer-Encoding": "chunked"}, []byte("abcd"), DefaultInspectionProfile())
	if !v.Rejected {
		t.Fatal("CL/TE conflict must reject")
	}
}

func TestCanonicalizeEncodedSlash(t *testing.T) {
	v := Canonicalize("GET", "/files%2fsecret", "", nil, nil, DefaultInspectionProfile())
	if !v.Rejected && !strings.Contains(v.Path, "secret") {
		t.Fatalf("encoded slash handling path=%s rejected=%v", v.Path, v.Rejected)
	}
	upper := Canonicalize("GET", "/files%2Fsecret", "", nil, nil, DefaultInspectionProfile())
	if !upper.Rejected {
		t.Fatal("uppercase encoded slash must reject")
	}
}

func TestCanonicalizeMalformedQueryAndLimits(t *testing.T) {
	if !Canonicalize("GET", "/x", "id=%zz", nil, nil, DefaultInspectionProfile()).Rejected {
		t.Fatal("malformed percent query must reject")
	}
	p := DefaultInspectionProfile()
	p.MaxParams = 1
	if !Canonicalize("GET", "/x", "a=1&b=2", nil, nil, p).Rejected {
		t.Fatal("too many params must reject")
	}
	p = DefaultInspectionProfile()
	p.JsonMaxDepth = 1
	deep := Canonicalize("POST", "/x", "", map[string]string{"Content-Type": "application/json"}, []byte(`{"a":{"b":1}}`), p)
	if !deep.Rejected {
		t.Fatal("json deeper than max must reject")
	}
	dup := CanonicalizeHTTP("GET", "/x", "", map[string][]string{"X-A": {"1", "2"}}, nil, DefaultInspectionProfile())
	if got := dup.Headers["x-a"]; len(got) != 1 || got[0] != "1" {
		t.Fatalf("duplicate header first=%v", got)
	}
}

func TestPolicySkipsAbsentBody(t *testing.T) {
	view := Canonicalize("GET", "/x", "", nil, nil, DefaultInspectionProfile())
	if CoverageOf(view.Coverage, commonv1.InspectionSurface_INSPECTION_SURFACE_BODY) != commonv1.CoverageStatus_COVERAGE_STATUS_ABSENT {
		t.Fatal("body ABSENT")
	}
	if !ShouldSkipBodyPolicy(view) {
		t.Fatal("body policy must not participate when body ABSENT")
	}
}
