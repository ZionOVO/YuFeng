package edgecore

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"yufeng/lib/kernel"

	artifactv1 "yufeng/proto/gen/artifactv1"
	commonv1 "yufeng/proto/gen/commonv1"
)

// 检测上限内的请求体必须原样到达上游；超限请求失败即关。
func TestProxyForwardsOversizedBodyIntact(t *testing.T) {
	cases := []struct {
		size       int
		wantStatus int
	}{
		{size: kernel.EngineBodyLimitBytes - 1, wantStatus: http.StatusOK},
		{size: kernel.EngineBodyLimitBytes, wantStatus: http.StatusOK},
		{size: kernel.EngineBodyLimitBytes + 1, wantStatus: http.StatusRequestEntityTooLarge},
		{size: kernel.EngineBodyLimitBytes * 3, wantStatus: http.StatusRequestEntityTooLarge},
	}
	for _, tc := range cases {
		t.Run("", func(t *testing.T) {
			var got []byte
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("上游读体失败: %v", err)
				}
				got = raw
			}))
			defer upstream.Close()

			body := make([]byte, tc.size)
			for i := range body {
				body[i] = byte('a' + i%26)
			}
			proxy := NewProxy(NewEngine(), commonv1.ReleaseMode_RELEASE_MODE_ENFORCE, nil, mustParseURL(t, upstream.URL), "asset-1")
			ts := httptest.NewServer(proxy)
			defer ts.Close()

			resp, err := http.Post(ts.URL, "application/octet-stream", bytes.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			if err := resp.Body.Close(); err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("状态码 %d，期望 %d", resp.StatusCode, tc.wantStatus)
			}
			if tc.wantStatus == http.StatusOK && !bytes.Equal(got, body) {
				t.Fatalf("转发体被破坏：上游收到 %d 字节（期望 %d）", len(got), tc.size)
			}
		})
	}
}

func TestProxyForwardsChunkedBodyIntact(t *testing.T) {
	var got []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("上游读体失败: %v", err)
		}
		got = raw
	}))
	defer upstream.Close()

	body := bytes.Repeat([]byte("chunked-body-"), 200)
	proxy := NewProxy(NewEngine(), commonv1.ReleaseMode_RELEASE_MODE_ENFORCE, nil, mustParseURL(t, upstream.URL), "asset-1")
	ts := httptest.NewServer(proxy)
	defer ts.Close()

	pr, pw := io.Pipe()
	go func() {
		_, _ = pw.Write(body)
		_ = pw.Close()
	}()
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/upload", pr)
	if err != nil {
		t.Fatal(err)
	}
	req.ContentLength = -1
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("chunked status %d", resp.StatusCode)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("chunked body broken: got %d want %d", len(got), len(body))
	}
}

func TestLegacyObserveShellDoesNotForwardUpstream(t *testing.T) {
	var calls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	proxy := NewProxy(NewEngine(), commonv1.ReleaseMode_RELEASE_MODE_ENFORCE, nil, mustParseURL(t, upstream.URL), "asset-1")
	proxy.SetPosture(commonv1.IngressPosture_INGRESS_POSTURE_TAP_ALERT)
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/mirror-copy", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("observe shell status=%d", rec.Code)
	}
	if calls != 0 {
		t.Fatalf("observe shell forwarded %d requests upstream", calls)
	}
}

// ReleaseProxy.ServeHTTP 的行为闭环：放行转发、命中拦截 403 且带请求标识。
func TestReleaseProxyServeHTTP(t *testing.T) {
	pub, priv, err := newKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := MarshalRules([]Rule{{ID: "block-q", Pattern: `(?i)union\s+select`}})
	if err != nil {
		t.Fatal(err)
	}
	a := &artifactv1.Artifact{
		Kind: artifactv1.Kind_KIND_RULE, Payload: payload, PayloadSchema: RulePayloadSchema,
		Ttl: durationpb.New(time.Hour), CreatedAt: timestamppb.Now(), CreatedBy: "test",
	}
	if err := kernel.SignArtifact(a, priv); err != nil {
		t.Fatal(err)
	}
	set := NewReleaseSet()
	if err := set.Apply(&artifactv1.ReleaseItem{ReleaseId: "rel-1", Artifact: a, Mode: commonv1.ReleaseMode_RELEASE_MODE_ENFORCE}, pub); err != nil {
		t.Fatal(err)
	}

	upstream := httptest.NewServer(newEchoHandler())
	defer upstream.Close()
	proxy := NewReleaseProxy(set, NewTelemetry(nil), mustParseURL(t, upstream.URL), "asset-1")
	ts := httptest.NewServer(proxy)
	defer ts.Close()

	code, body := doReq(t, ts.URL, "/api/items?page=2")
	if code != 200 || !strings.Contains(body, "upstream ok") {
		t.Fatalf("放行请求异常: code=%d body=%s", code, body)
	}
	code, _ = doReq(t, ts.URL, "/api/items?id=1+UNION+SELECT+pw")
	if code != http.StatusForbidden {
		t.Fatalf("命中拦截应 403，实际 %d", code)
	}
	requests, routes := proxy.WindowSnapshot()
	if requests != 2 || len(routes) != 2 {
		t.Fatalf("心跳窗应每个 HTTP 请求只记一次：requests=%d routes=%v", requests, routes)
	}
}

func TestReleaseProxyCRSHitWithoutPolicyStays200(t *testing.T) {
	pub, priv, err := newKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	set := NewReleaseSet()
	if err := InstallSignedCRS(set, pub, priv); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(newEchoHandler())
	defer upstream.Close()
	proxy := NewReleaseProxy(set, NewTelemetry(nil), mustParseURL(t, upstream.URL), "asset-1")
	var got Decision
	proxy.SetObserver(func(_ Request, dec Decision, _ string) { got = dec })
	ts := httptest.NewServer(proxy)
	defer ts.Close()

	code, body := doReq(t, ts.URL, "/api/items?id=1+UNION+SELECT+pw")
	if code != http.StatusOK || !strings.Contains(body, "upstream ok") {
		t.Fatalf("无策略核心规则集命中必须 200: code=%d body=%s", code, body)
	}
	if len(got.Detections) == 0 {
		t.Fatal("无策略命中必须带上检测键")
	}
}

func TestReleaseProxyPolicyEnforceBlocksDetectionKey(t *testing.T) {
	pub, priv, err := newKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	crs := newOwnedCorazaForTest(t)
	req := Request{Method: "GET", Path: "/api/items", Query: "id=1+UNION+SELECT+pw"}
	view := Canonicalize(req.Method, req.Path, req.Query, nil, nil, DefaultInspectionProfile())
	inspection, err := crs.Inspect(context.Background(), InspectionInput{View: view})
	if err != nil || len(inspection.Detections) == 0 {
		t.Fatalf("need crs key: %v %#v", err, inspection.Detections)
	}
	dets := inspection.Detections
	payload, err := protojson.Marshal(&artifactv1.PolicyCandidate{
		Action: "block",
		Predicate: &artifactv1.PolicyPredicate{
			DetectionKeys: []*commonv1.DetectionKey{{
				DetectorId: "crs", RuleId: dets[0].RuleID,
				TargetLocation: dets[0].Location,
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	a := &artifactv1.Artifact{
		Kind: artifactv1.Kind_KIND_POLICY, Payload: payload, PayloadSchema: PolicyPayloadSchema,
		Ttl: durationpb.New(time.Hour), CreatedAt: timestamppb.Now(), CreatedBy: "test",
	}
	if err := kernel.SignArtifact(a, priv); err != nil {
		t.Fatal(err)
	}
	set := NewReleaseSet()
	if err := InstallSignedCRS(set, pub, priv); err != nil {
		t.Fatal(err)
	}
	if err := set.Apply(&artifactv1.ReleaseItem{ReleaseId: "rel-pol", Artifact: a, Mode: commonv1.ReleaseMode_RELEASE_MODE_ENFORCE}, pub); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(newEchoHandler())
	defer upstream.Close()
	proxy := NewReleaseProxy(set, NewTelemetry(nil), mustParseURL(t, upstream.URL), "asset-1")
	ts := httptest.NewServer(proxy)
	defer ts.Close()

	code, _ := doReq(t, ts.URL, "/api/items?page=2")
	if code != http.StatusOK {
		t.Fatalf("普通请求应 200，实际 %d", code)
	}
	code, _ = doReq(t, ts.URL, "/api/items?id=1+UNION+SELECT+pw")
	if code != http.StatusForbidden {
		t.Fatalf("检测键策略 enforce 应 403，实际 %d", code)
	}
}

// ScopeCoversAsset 是装载期过滤的纯函数，表驱动覆盖三种范围形态。
func TestScopeCoversAsset(t *testing.T) {
	cases := []struct {
		name    string
		scope   *artifactv1.Scope
		assetID string
		want    bool
	}{
		{"nil 范围全局生效", nil, "any", true},
		{"空列表全局生效", &artifactv1.Scope{}, "any", true},
		{"列表命中", &artifactv1.Scope{AssetIds: []string{"a-1", "a-2"}}, "a-2", true},
		{"列表未命中", &artifactv1.Scope{AssetIds: []string{"a-1"}}, "a-3", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ScopeCoversAsset(c.scope, c.assetID); got != c.want {
				t.Fatalf("ScopeCoversAsset(%v, %s) = %v, want %v", c.scope, c.assetID, got, c.want)
			}
		})
	}
}
