package edgecore

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"sync/atomic"
	"testing"

	commonv1 "yufeng/proto/gen/commonv1"
)

type corazaCapacityBenchmark struct {
	name     string
	requests []Request
	bytes    int64
}

type corazaBenchmarkRoundTripper func(*http.Request) (*http.Response, error)

func (f corazaBenchmarkRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type corazaBenchmarkResponseWriter struct {
	header http.Header
	status int
}

func (w *corazaBenchmarkResponseWriter) Header() http.Header { return w.header }

func (w *corazaBenchmarkResponseWriter) WriteHeader(status int) { w.status = status }

func (w *corazaBenchmarkResponseWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return len(body), nil
}

func (w *corazaBenchmarkResponseWriter) reset() {
	clear(w.header)
	w.status = 0
}

// BenchmarkCorazaDetectorSerial 测量单工作协程的 Coraza 检查耗时与分配。
func BenchmarkCorazaDetectorSerial(b *testing.B) {
	detector := newOwnedCorazaForTest(b)
	for _, fixture := range corazaCapacityBenchmarks() {
		b.Run(fixture.name, func(b *testing.B) {
			validateCorazaCapacityBenchmark(b, detector, fixture)
			var detections []Detection
			b.SetBytes(fixture.bytes)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				request := fixture.requests[i%len(fixture.requests)]
				var detectErr error
				detections, detectErr = detector.Detect(request)
				if detectErr != nil {
					b.Fatal(detectErr)
				}
			}
			runtime.KeepAlive(detections)
		})
	}
}

// BenchmarkCorazaDetectorParallel 测量共享 Coraza 实例在 32 个逻辑处理器下的聚合完成间隔。
func BenchmarkCorazaDetectorParallel(b *testing.B) {
	detector := newOwnedCorazaForTest(b)
	for _, fixture := range corazaCapacityBenchmarks() {
		b.Run(fixture.name, func(b *testing.B) {
			validateCorazaCapacityBenchmark(b, detector, fixture)
			b.SetBytes(fixture.bytes)
			b.ReportAllocs()
			b.ResetTimer()
			var workerSequence atomic.Uint64
			b.RunParallel(func(pb *testing.PB) {
				iteration := int(workerSequence.Add(1) - 1)
				var detections []Detection
				for pb.Next() {
					request := fixture.requests[iteration%len(fixture.requests)]
					var detectErr error
					detections, detectErr = detector.Detect(request)
					if detectErr != nil {
						b.Error(detectErr)
						return
					}
					iteration++
				}
				runtime.KeepAlive(detections)
			})
		})
	}
}

// BenchmarkCorazaReleaseProxyCapacityParallel 测量生产核心请求壳，不包含套接字与上游网络抖动。
func BenchmarkCorazaReleaseProxyCapacityParallel(b *testing.B) {
	detector := newOwnedCorazaForTest(b)
	set := NewReleaseSet()
	set.inspectors = []Inspector{detector}
	set.SetPosture(commonReverseProxyPosture())
	upstream, err := url.Parse("http://benchmark.invalid")
	if err != nil {
		b.Fatal(err)
	}
	proxy := NewReleaseProxy(set, nil, upstream, "asset-benchmark")
	proxy.SetUnitID("unit-benchmark")
	proxy.SetEvidence(NewEvidenceRing())
	proxy.SetObserver(func(Request, Decision, string) {})
	proxy.rp.Transport = corazaBenchmarkRoundTripper(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Header:     make(http.Header),
			Body:       http.NoBody,
			Request:    request,
		}, nil
	})

	for _, fixture := range corazaCapacityBenchmarks() {
		b.Run(fixture.name, func(b *testing.B) {
			validateCorazaCapacityBenchmark(b, detector, fixture)
			b.SetBytes(fixture.bytes)
			b.ReportAllocs()
			b.ResetTimer()
			var workerSequence atomic.Uint64
			b.RunParallel(func(pb *testing.PB) {
				requests := make([]*http.Request, len(fixture.requests))
				for i, request := range fixture.requests {
					requests[i] = newCorazaBenchmarkHTTPRequest(request)
				}
				writer := &corazaBenchmarkResponseWriter{header: make(http.Header)}
				iteration := int(workerSequence.Add(1) - 1)
				for pb.Next() {
					index := iteration % len(requests)
					resetCorazaBenchmarkHTTPRequest(requests[index], fixture.requests[index])
					writer.reset()
					proxy.ServeHTTP(writer, requests[index])
					if writer.status != http.StatusNoContent {
						b.Errorf("status=%d", writer.status)
						return
					}
					iteration++
				}
			})
		})
	}
}

func corazaCapacityBenchmarks() []corazaCapacityBenchmark {
	read := Request{
		Method: "GET", Path: "/api/items", Query: "page=2",
		Headers: map[string]string{"host": "app.example"},
	}
	queryAttack := read
	queryAttack.Query = "id=1+UNION+SELECT+password+FROM+users"
	json1 := corazaCapacityRequest("application/json", corazaJSONBody(1<<10))
	json4 := corazaCapacityRequest("application/json", corazaJSONBody(4<<10))
	simple64 := corazaCapacityRequest("application/octet-stream", bytes.Repeat([]byte("x"), 64<<10))
	natural64 := corazaCapacityRequest("text/plain", corazaNaturalTextBody(64<<10))
	base6464 := corazaCapacityRequest("application/octet-stream", corazaBase64Body(64<<10))
	binary64 := corazaCapacityRequest("application/octet-stream", corazaBinaryBody(64<<10))
	attackHead := corazaCapacityRequest("application/octet-stream", corazaAttackBody(64<<10, true))
	attackTail := corazaCapacityRequest("application/octet-stream", corazaAttackBody(64<<10, false))
	return []corazaCapacityBenchmark{
		{name: "read_no_body", requests: []Request{read}},
		{name: "sql_injection_query", requests: []Request{queryAttack}},
		{name: "json_1_kib", requests: []Request{json1}, bytes: 1 << 10},
		{name: "json_4_kib", requests: []Request{json4}, bytes: 4 << 10},
		{name: "simple_64_kib", requests: []Request{simple64}, bytes: 64 << 10},
		{name: "natural_text_64_kib", requests: []Request{natural64}, bytes: 64 << 10},
		{name: "base64_64_kib", requests: []Request{base6464}, bytes: 64 << 10},
		{name: "binary_64_kib", requests: []Request{binary64}, bytes: 64 << 10},
		{name: "mixed_90_small_10_natural", requests: mixedCorazaCapacityRequests(json1, natural64), bytes: (9*(1<<10) + (64 << 10)) / 10},
		{name: "mixed_90_small_10_binary", requests: mixedCorazaCapacityRequests(json1, binary64), bytes: (9*(1<<10) + (64 << 10)) / 10},
		{name: "attack_64_kib_head", requests: []Request{attackHead}, bytes: 64 << 10},
		{name: "attack_64_kib_tail", requests: []Request{attackTail}, bytes: 64 << 10},
	}
}

func mixedCorazaCapacityRequests(small, large Request) []Request {
	requests := make([]Request, 10)
	for i := range 9 {
		requests[i] = small
	}
	requests[9] = large
	return requests
}

func validateCorazaCapacityBenchmark(b *testing.B, detector *CorazaDetector, fixture corazaCapacityBenchmark) {
	b.Helper()
	for _, request := range fixture.requests {
		if _, err := detector.Detect(request); err != nil {
			b.Fatal(err)
		}
	}
}

func newCorazaBenchmarkHTTPRequest(request Request) *http.Request {
	target := "http://edge.benchmark" + request.Path
	if request.Query != "" {
		target += "?" + request.Query
	}
	httpRequest := httptest.NewRequest(request.Method, target, bytes.NewReader(request.Body))
	httpRequest.RemoteAddr = "192.0.2.10:49152"
	for key, value := range request.Headers {
		if key == "host" {
			httpRequest.Host = value
			continue
		}
		httpRequest.Header.Set(key, value)
	}
	return httpRequest
}

func resetCorazaBenchmarkHTTPRequest(httpRequest *http.Request, request Request) {
	if len(request.Body) == 0 {
		httpRequest.Body = http.NoBody
		httpRequest.ContentLength = 0
		return
	}
	httpRequest.Body = io.NopCloser(bytes.NewReader(request.Body))
	httpRequest.ContentLength = int64(len(request.Body))
}

func commonReverseProxyPosture() commonv1.IngressPosture {
	return commonv1.IngressPosture_INGRESS_POSTURE_REVERSE_PROXY
}
