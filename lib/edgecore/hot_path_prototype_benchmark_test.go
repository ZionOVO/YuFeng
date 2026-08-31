package edgecore

import (
	"bytes"
	"context"
	"fmt"
	"net/netip"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/corazawaf/coraza/v3"

	artifactv1 "yufeng/proto/gen/artifactv1"
	commonv1 "yufeng/proto/gen/commonv1"
)

const benchmarkRequestIDPoolSize = 4096

// benchmarkExecutionPlan 模拟装载世代时编译、请求路径只读的不可变执行计划。
// 过期发布由计划替换或低频清扫移除，不在每个请求上扫描。
type benchmarkExecutionPlan struct {
	inspectors    []Inspector
	mapper        *artifactv1.TaxonomyMapper
	releases      []managedRelease
	counters      map[string]*counter
	posture       commonv1.IngressPosture
	generationID  string
	generationSeq int64
}

func captureBenchmarkExecutionPlan(set *ReleaseSet, now time.Time) benchmarkExecutionPlan {
	set.mu.RLock()
	defer set.mu.RUnlock()

	plan := benchmarkExecutionPlan{
		inspectors: append([]Inspector(nil), set.inspectors...),
		mapper:     set.mapper,
		releases:   make([]managedRelease, 0, len(set.releases)),
		counters:   make(map[string]*counter, len(set.releases)),
		posture:    ResolvePosture(set.posture),
	}
	for id, release := range set.releases {
		if release.expiresAt.After(now) {
			plan.releases = append(plan.releases, release)
			plan.counters[id] = set.counters[id]
		}
	}
	if set.activeGen != nil {
		plan.generationID = set.activeGen.GenerationId
		plan.generationSeq = set.activeGen.GenerationSeq
	}
	return plan
}

func (p benchmarkExecutionPlan) decide(ctx context.Context, req Request, requestID string, view CanonicalView) Decision {
	insp := inspectWith(ctx, p.inspectors, p.mapper, InspectionInput{View: view, ClientAddress: req.ClientAddress})
	dec := gateWith(p.releases, p.posture, req, requestID, insp, view, p.counters)
	dec.GenerationID = p.generationID
	dec.GenerationSeq = p.generationSeq
	return dec
}

type benchmarkEvidenceItem struct {
	raw      []byte
	expires  time.Time
	sequence uint64
}

type benchmarkEvidenceReference struct {
	id       string
	sequence uint64
}

// benchmarkEvidenceRing 模拟基于头指针和惰性失效引用的常数时间证据环。
// 重复标识产生的旧引用在驱逐时跳过，因此无需线性删除顺序表。
type benchmarkEvidenceRing struct {
	mu         sync.Mutex
	ttl        time.Duration
	maxEntries int
	maxBytes   int
	bytes      int
	sequence   uint64
	head       int
	order      []benchmarkEvidenceReference
	items      map[string]benchmarkEvidenceItem
}

type benchmarkWindow struct {
	requests atomic.Uint64
	mu       sync.Mutex
	routes   []string
}

func (w *benchmarkWindow) note(method, path string) {
	requestCount := w.requests.Add(1)
	if requestCount > 32 {
		return
	}
	w.mu.Lock()
	if len(w.routes) < 32 {
		w.routes = append(w.routes, method+" "+path)
	}
	w.mu.Unlock()
}

func newBenchmarkEvidenceRing(current *EvidenceRing) *benchmarkEvidenceRing {
	return &benchmarkEvidenceRing{
		ttl:        current.ttl,
		maxEntries: current.maxEntries,
		maxBytes:   current.maxBytes,
		items:      make(map[string]benchmarkEvidenceItem, current.maxEntries),
	}
}

func (r *benchmarkEvidenceRing) putParts(id, query string, body []byte, now time.Time) {
	if id == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	r.expire(now)
	r.sequence++
	if old, ok := r.items[id]; ok {
		r.bytes -= len(old.raw)
	}
	raw := make([]byte, len(query)+len(body))
	copy(raw, query)
	copy(raw[len(query):], body)
	r.items[id] = benchmarkEvidenceItem{raw: raw, expires: now.Add(r.ttl), sequence: r.sequence}
	r.order = append(r.order, benchmarkEvidenceReference{id: id, sequence: r.sequence})
	r.bytes += len(raw)
	for (r.maxEntries > 0 && len(r.items) > r.maxEntries) || (r.maxBytes > 0 && r.bytes > r.maxBytes) {
		r.evictHead()
	}
	r.compactOrder()
}

func (r *benchmarkEvidenceRing) expire(now time.Time) {
	for r.head < len(r.order) {
		ref := r.order[r.head]
		item, ok := r.items[ref.id]
		if !ok || item.sequence != ref.sequence {
			r.head++
			continue
		}
		if item.expires.After(now) {
			return
		}
		r.bytes -= len(item.raw)
		delete(r.items, ref.id)
		r.head++
	}
}

func (r *benchmarkEvidenceRing) evictHead() {
	for r.head < len(r.order) {
		ref := r.order[r.head]
		r.head++
		item, ok := r.items[ref.id]
		if !ok || item.sequence != ref.sequence {
			continue
		}
		r.bytes -= len(item.raw)
		delete(r.items, ref.id)
		return
	}
}

func (r *benchmarkEvidenceRing) compactOrder() {
	if r.head < 1024 || r.head*2 < len(r.order) {
		return
	}
	copy(r.order, r.order[r.head:])
	r.order = r.order[:len(r.order)-r.head]
	r.head = 0
}

func BenchmarkReleaseSetImmutableExecutionPlan(b *testing.B) {
	ctx := context.Background()
	req := Request{
		Method:        "GET",
		Path:          "/api/items",
		Query:         "page=2&sort=created_at",
		Headers:       map[string]string{"host": "app.example"},
		ClientAddress: netip.MustParseAddr("192.0.2.10"),
		UnitID:        "unit-benchmark",
	}
	view := Canonicalize(req.Method, req.Path, req.Query, req.Headers, req.Body, DefaultInspectionProfile())
	for _, releaseCount := range []int{1, 16, 64} {
		b.Run(fmt.Sprintf("releases_%d", releaseCount), func(b *testing.B) {
			set := newBenchmarkReleaseSet(b, releaseCount)
			plan := captureBenchmarkExecutionPlan(set, time.Now())
			current := set.InspectThenGate(ctx, req, "equivalence", view)
			proposed := plan.decide(ctx, req, "equivalence", view)
			if current.Action != proposed.Action || len(current.Observations) != len(proposed.Observations) || !reflect.DeepEqual(current.Detections, proposed.Detections) {
				b.Fatal("immutable execution plan changed decision semantics")
			}

			b.Run("current_request_snapshot", func(b *testing.B) {
				var decision Decision
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					decision = set.InspectThenGate(ctx, req, "request", view)
				}
				runtime.KeepAlive(decision)
			})
			b.Run("proposed_generation_plan", func(b *testing.B) {
				var decision Decision
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					decision = plan.decide(ctx, req, "request", view)
				}
				runtime.KeepAlive(decision)
			})
		})
	}
}

func BenchmarkEvidenceRingConstantTimePrototype(b *testing.B) {
	requestIDs := benchmarkRequestIDs()
	now := time.Unix(1_800_000_000, 0)
	for _, bodySize := range []int{1024, 64 << 10} {
		body := bytes.Repeat([]byte("x"), bodySize)
		query := "page=2&sort=created_at"
		b.Run(fmt.Sprintf("body_%d_bytes", bodySize), func(b *testing.B) {
			b.Run("current_full_scan_and_double_copy", func(b *testing.B) {
				ring := NewEvidenceRing()
				warmEvidenceRingCurrent(ring, requestIDs, query, body, now)
				b.ReportAllocs()
				b.SetBytes(int64(len(query) + len(body)))
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					raw := append([]byte(query), body...)
					ring.Put(requestIDs[i&(len(requestIDs)-1)], raw, now)
				}
			})
			b.Run("proposed_head_expiry_and_single_copy", func(b *testing.B) {
				ring := newBenchmarkEvidenceRing(NewEvidenceRing())
				warmEvidenceRingProposed(ring, requestIDs, query, body, now)
				b.ReportAllocs()
				b.SetBytes(int64(len(query) + len(body)))
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					ring.putParts(requestIDs[i&(len(requestIDs)-1)], query, body, now)
				}
			})
		})
	}
}

func BenchmarkReleaseProxySelectiveObservation(b *testing.B) {
	ctx := context.Background()
	requestIDs := benchmarkRequestIDs()
	for _, bodySize := range []int{0, 1024, 64 << 10} {
		body := bytes.Repeat([]byte("x"), bodySize)
		req := Request{
			Method:        "POST",
			Path:          "/api/items",
			Query:         "page=2",
			Headers:       map[string]string{"host": "app.example", "content-type": "application/octet-stream"},
			Body:          body,
			ClientAddress: netip.MustParseAddr("192.0.2.10"),
			UnitID:        "unit-benchmark",
		}
		view := Canonicalize(req.Method, req.Path, req.Query, req.Headers, req.Body, DefaultInspectionProfile())
		b.Run(fmt.Sprintf("body_%d_bytes", bodySize), func(b *testing.B) {
			b.Run("current_always_on_attachments", func(b *testing.B) {
				var decision Decision
				proxy := NewReleaseProxy(NewReleaseSet(), nil, nil, "asset-benchmark")
				proxy.SetEvidence(NewEvidenceRing())
				proxy.SetModelIngress(NewModelIngressQueue())
				for i := range requestIDs {
					proxy.DecideRequest(ctx, req, requestIDs[i], view)
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					decision = proxy.DecideRequest(ctx, req, requestIDs[i&(len(requestIDs)-1)], view)
				}
				runtime.KeepAlive(decision)
			})
			b.Run("proposed_policy_disabled_fast_path", func(b *testing.B) {
				var decision Decision
				proxy := NewReleaseProxy(NewReleaseSet(), nil, nil, "asset-benchmark")
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					decision = proxy.DecideRequest(ctx, req, requestIDs[i&(len(requestIDs)-1)], view)
				}
				runtime.KeepAlive(decision)
			})
		})
	}
}

func BenchmarkReleaseProxyParallelHotPath(b *testing.B) {
	ctx := context.Background()
	requestIDs := benchmarkRequestIDs()
	req := Request{
		Method:        "POST",
		Path:          "/api/items",
		Query:         "page=2",
		Headers:       map[string]string{"host": "app.example", "content-type": "application/octet-stream"},
		Body:          bytes.Repeat([]byte("x"), 1024),
		ClientAddress: netip.MustParseAddr("192.0.2.10"),
		UnitID:        "unit-benchmark",
	}
	view := Canonicalize(req.Method, req.Path, req.Query, req.Headers, req.Body, DefaultInspectionProfile())
	b.Run("current_always_on_attachments", func(b *testing.B) {
		proxy := NewReleaseProxy(NewReleaseSet(), nil, nil, "asset-benchmark")
		proxy.SetEvidence(NewEvidenceRing())
		proxy.SetModelIngress(NewModelIngressQueue())
		for i := range requestIDs {
			proxy.DecideRequest(ctx, req, requestIDs[i], view)
		}
		var cursor atomic.Uint64
		b.ReportAllocs()
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			var last Decision
			for pb.Next() {
				index := cursor.Add(1) & uint64(len(requestIDs)-1)
				last = proxy.DecideRequest(ctx, req, requestIDs[index], view)
			}
			if last.Action != ActionAllow {
				b.Errorf("unexpected action %v", last.Action)
			}
		})
	})
	b.Run("proposed_selective_attachments", func(b *testing.B) {
		proxy := NewReleaseProxy(NewReleaseSet(), nil, nil, "asset-benchmark")
		var cursor atomic.Uint64
		b.ReportAllocs()
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			var last Decision
			for pb.Next() {
				index := cursor.Add(1) & uint64(len(requestIDs)-1)
				last = proxy.DecideRequest(ctx, req, requestIDs[index], view)
			}
			if last.Action != ActionAllow {
				b.Errorf("unexpected action %v", last.Action)
			}
		})
	})
	b.Run("proposed_generation_plan_and_atomic_window", func(b *testing.B) {
		plan := captureBenchmarkExecutionPlan(NewReleaseSet(), time.Now())
		window := &benchmarkWindow{}
		for i := 0; i < 32; i++ {
			window.note(req.Method, req.Path)
		}
		var cursor atomic.Uint64
		b.ReportAllocs()
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			var last Decision
			for pb.Next() {
				index := cursor.Add(1) & uint64(len(requestIDs)-1)
				last = plan.decide(ctx, req, requestIDs[index], view)
				window.note(req.Method, req.Path)
			}
			if last.Action != ActionAllow {
				b.Errorf("unexpected action %v", last.Action)
			}
		})
	})
}

func BenchmarkCorazaReleaseProxyParallel(b *testing.B) {
	crs := newOwnedCorazaForTest(b)
	ctx := context.Background()
	requestIDs := benchmarkRequestIDs()
	req := Request{
		Method:        "GET",
		Path:          "/api/items",
		Query:         "page=2",
		Headers:       map[string]string{"host": "app.example"},
		ClientAddress: netip.MustParseAddr("192.0.2.10"),
		UnitID:        "unit-benchmark",
	}
	view := Canonicalize(req.Method, req.Path, req.Query, req.Headers, req.Body, DefaultInspectionProfile())
	newSet := func() *ReleaseSet {
		set := NewReleaseSet()
		set.inspectors = []Inspector{crs}
		return set
	}

	b.Run("current_complete_decision_path", func(b *testing.B) {
		proxy := NewReleaseProxy(newSet(), nil, nil, "asset-benchmark")
		proxy.SetEvidence(NewEvidenceRing())
		proxy.SetModelIngress(NewModelIngressQueue())
		for i := range requestIDs {
			proxy.DecideRequest(ctx, req, requestIDs[i], view)
		}
		var cursor atomic.Uint64
		b.ReportAllocs()
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			var last Decision
			for pb.Next() {
				index := cursor.Add(1) & uint64(len(requestIDs)-1)
				last = proxy.DecideRequest(ctx, req, requestIDs[index], view)
			}
			if last.Action != ActionAllow {
				b.Errorf("unexpected action %v", last.Action)
			}
		})
	})
	b.Run("proposed_selective_attachments", func(b *testing.B) {
		proxy := NewReleaseProxy(newSet(), nil, nil, "asset-benchmark")
		var cursor atomic.Uint64
		b.ReportAllocs()
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			var last Decision
			for pb.Next() {
				index := cursor.Add(1) & uint64(len(requestIDs)-1)
				last = proxy.DecideRequest(ctx, req, requestIDs[index], view)
			}
			if last.Action != ActionAllow {
				b.Errorf("unexpected action %v", last.Action)
			}
		})
	})
	b.Run("proposed_generation_plan_and_atomic_window", func(b *testing.B) {
		plan := captureBenchmarkExecutionPlan(newSet(), time.Now())
		window := &benchmarkWindow{}
		for i := 0; i < 32; i++ {
			window.note(req.Method, req.Path)
		}
		var cursor atomic.Uint64
		b.ReportAllocs()
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			var last Decision
			for pb.Next() {
				index := cursor.Add(1) & uint64(len(requestIDs)-1)
				last = plan.decide(ctx, req, requestIDs[index], view)
				window.note(req.Method, req.Path)
			}
			if last.Action != ActionAllow {
				b.Errorf("unexpected action %v", last.Action)
			}
		})
	})
}

func BenchmarkCorazaSharedCanonicalRequestPrototype(b *testing.B) {
	crs := newOwnedCorazaForTest(b)
	cases := []struct {
		name  string
		query string
		body  []byte
	}{
		{name: "benign_get", query: "page=2"},
		{name: "sql_injection_query", query: "id=1+UNION+SELECT+pw"},
		{name: "benign_64_kib_body", query: "page=2", body: bytes.Repeat([]byte("x"), 64<<10)},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			headers := map[string]string{"host": "app.example", "content-type": "application/octet-stream"}
			view := Canonicalize("POST", "/api/items", tc.query, headers, tc.body, DefaultInspectionProfile())
			req := RequestFromView(view)
			req.ClientAddress = netip.MustParseAddr("192.0.2.10")
			input := InspectionInput{View: view, ClientAddress: req.ClientAddress}
			current, err := crs.Inspect(context.Background(), input)
			if err != nil {
				b.Fatal(err)
			}
			proposed, err := inspectCorazaWithCanonicalRequest(crs, req, view)
			if err != nil {
				b.Fatal(err)
			}
			if !reflect.DeepEqual(current, proposed) {
				b.Fatal("shared canonical request changed Coraza inspection")
			}

			b.Run("current_rebuild_request", func(b *testing.B) {
				var inspection Inspection
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					var inspectErr error
					inspection, inspectErr = crs.Inspect(context.Background(), input)
					if inspectErr != nil {
						b.Fatal(inspectErr)
					}
				}
				runtime.KeepAlive(inspection)
			})
			b.Run("proposed_reuse_canonical_request", func(b *testing.B) {
				var inspection Inspection
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					var inspectErr error
					inspection, inspectErr = inspectCorazaWithCanonicalRequest(crs, req, view)
					if inspectErr != nil {
						b.Fatal(inspectErr)
					}
				}
				runtime.KeepAlive(inspection)
			})
		})
	}
}

func BenchmarkCorazaParallelCapacity(b *testing.B) {
	crs := newOwnedCorazaForTest(b)
	cases := []struct {
		name  string
		query string
		body  []byte
	}{
		{name: "benign_get", query: "page=2"},
		{name: "sql_injection_query", query: "id=1+UNION+SELECT+pw"},
		{name: "benign_1_kib_body", query: "page=2", body: bytes.Repeat([]byte("x"), 1<<10)},
		{name: "benign_4_kib_body", query: "page=2", body: bytes.Repeat([]byte("x"), 4<<10)},
		{name: "benign_16_kib_body", query: "page=2", body: bytes.Repeat([]byte("x"), 16<<10)},
		{name: "benign_32_kib_body", query: "page=2", body: bytes.Repeat([]byte("x"), 32<<10)},
		{name: "benign_64_kib_body", query: "page=2", body: bytes.Repeat([]byte("x"), 64<<10)},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			headers := map[string]string{"host": "app.example", "content-type": "application/octet-stream"}
			view := Canonicalize("POST", "/api/items", tc.query, headers, tc.body, DefaultInspectionProfile())
			input := InspectionInput{View: view, ClientAddress: netip.MustParseAddr("192.0.2.10")}
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				var last Inspection
				for pb.Next() {
					var inspectErr error
					last, inspectErr = crs.Inspect(context.Background(), input)
					if inspectErr != nil {
						b.Error(inspectErr)
						return
					}
				}
				if last.Rejected != view.Rejected {
					b.Error("Coraza inspection changed canonical rejection state")
				}
			})
		})
	}
}

func BenchmarkCorazaRegexPrefilter(b *testing.B) {
	current := newOwnedCorazaForTest(b)
	prefiltered, err := newBenchmarkCorazaWithRegexPrefilter()
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if err := prefiltered.Close(); err != nil {
			b.Errorf("close prefiltered Coraza detector: %v", err)
		}
	})
	verifyCorazaPrefilterDetections(b, current, prefiltered)

	for _, bodySize := range []int{1 << 10, 4 << 10, 64 << 10} {
		req := Request{
			Method: "POST", Path: "/api/items", Query: "page=2",
			Headers: map[string]string{"host": "app.example", "content-type": "application/octet-stream"},
			Body:    bytes.Repeat([]byte("x"), bodySize), ClientAddress: netip.MustParseAddr("192.0.2.10"),
		}
		b.Run(fmt.Sprintf("body_%d_bytes", bodySize), func(b *testing.B) {
			b.Run("current_regex_prefilter_off", func(b *testing.B) {
				var detections []Detection
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					var detectErr error
					detections, detectErr = current.Detect(req)
					if detectErr != nil {
						b.Fatal(detectErr)
					}
				}
				runtime.KeepAlive(detections)
			})
			b.Run("proposed_regex_prefilter_on", func(b *testing.B) {
				var detections []Detection
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					var detectErr error
					detections, detectErr = prefiltered.Detect(req)
					if detectErr != nil {
						b.Fatal(detectErr)
					}
				}
				runtime.KeepAlive(detections)
			})
		})
	}
}

func BenchmarkCorazaBodyProcessorCost(b *testing.B) {
	crs := newOwnedCorazaForTest(b)
	const bodySize = 4 << 10
	cases := []struct {
		name        string
		contentType string
		body        []byte
	}{
		{name: "octet_stream", contentType: "application/octet-stream", body: bytes.Repeat([]byte("x"), bodySize)},
		{name: "json", contentType: "application/json", body: benchmarkJSONBody(bodySize)},
		{name: "urlencoded", contentType: "application/x-www-form-urlencoded", body: append([]byte("value="), bytes.Repeat([]byte("x"), bodySize-len("value="))...)},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			var detections []Detection
			req := Request{
				Method: "POST", Path: "/api/items", Query: "page=2",
				Headers: map[string]string{"host": "app.example", "content-type": tc.contentType},
				Body:    tc.body, ClientAddress: netip.MustParseAddr("192.0.2.10"),
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				var detectErr error
				detections, detectErr = crs.Detect(req)
				if detectErr != nil {
					b.Fatal(detectErr)
				}
			}
			runtime.KeepAlive(detections)
		})
	}
}

func newBenchmarkReleaseSet(b *testing.B, releaseCount int) *ReleaseSet {
	b.Helper()
	detector, err := NewRuleDetector("benchmark-rule", []Rule{{ID: "never-match", Pattern: "forbidden", Target: "path"}})
	if err != nil {
		b.Fatal(err)
	}
	set := NewReleaseSet()
	expires := time.Now().Add(time.Hour)
	for i := 0; i < releaseCount; i++ {
		id := fmt.Sprintf("release-%d", i)
		set.releases[id] = managedRelease{
			releaseID: id, artifactID: "artifact-" + id, detector: detector,
			prefix: "/admin", mode: commonv1.ReleaseMode_RELEASE_MODE_SHADOW, expiresAt: expires,
		}
		set.counters[id] = &counter{}
	}
	return set
}

func benchmarkRequestIDs() []string {
	ids := make([]string, benchmarkRequestIDPoolSize)
	for i := range ids {
		ids[i] = fmt.Sprintf("request-%08d", i)
	}
	return ids
}

func warmEvidenceRingCurrent(ring *EvidenceRing, ids []string, query string, body []byte, now time.Time) {
	for _, id := range ids {
		raw := append([]byte(query), body...)
		ring.Put(id, raw, now)
	}
}

func warmEvidenceRingProposed(ring *benchmarkEvidenceRing, ids []string, query string, body []byte, now time.Time) {
	for _, id := range ids {
		ring.putParts(id, query, body, now)
	}
}

func inspectCorazaWithCanonicalRequest(detector *CorazaDetector, req Request, view CanonicalView) (Inspection, error) {
	detections, err := detector.Detect(req)
	out := Inspection{Coverage: view.Coverage, Rejected: view.Rejected}
	if err != nil {
		out.Coverage = append(append([]Coverage(nil), view.Coverage...), CoverageError(commonv1.InspectionSurface_INSPECTION_SURFACE_QUERY))
		return out, err
	}
	for i := range detections {
		detections[i].InspectorID = corazaDetectorID
		detections[i].ProfileDigest = view.ProfileDigest
	}
	out.Detections = detections
	return out, nil
}

func newBenchmarkCorazaWithRegexPrefilter() (*CorazaDetector, error) {
	directives := `
Include @coraza.conf-recommended
SecRxPreFilter On
SecRuleEngine DetectionOnly
SecRequestBodyAccess On
SecRequestBodyInMemoryLimit 65536
SecRequestBodyLimit 65536
SecRequestBodyNoFilesLimit 65536
SecRequestBodyLimitAction ProcessPartial
SecResponseBodyAccess Off
SecAuditEngine Off
Include @crs-setup.conf.example
Include @owasp_crs/REQUEST-901-INITIALIZATION.conf
Include @owasp_crs/REQUEST-930-APPLICATION-ATTACK-LFI.conf
Include @owasp_crs/REQUEST-931-APPLICATION-ATTACK-RFI.conf
Include @owasp_crs/REQUEST-932-APPLICATION-ATTACK-RCE.conf
Include @owasp_crs/REQUEST-934-APPLICATION-ATTACK-GENERIC.conf
Include @owasp_crs/REQUEST-941-APPLICATION-ATTACK-XSS.conf
Include @owasp_crs/REQUEST-942-APPLICATION-ATTACK-SQLI.conf
`
	waf, err := coraza.NewWAF(coraza.NewWAFConfig().WithRootFS(newCorazaRootFS()).WithDirectives(directives))
	if err != nil {
		return nil, err
	}
	return &CorazaDetector{waf: waf}, nil
}

func verifyCorazaPrefilterDetections(b *testing.B, current, prefiltered *CorazaDetector) {
	b.Helper()
	req := Request{
		Method: "POST", Path: "/api/items", Query: "id=1+UNION+SELECT+pw",
		Headers: map[string]string{"host": "app.example", "content-type": "application/json"},
		Body:    []byte(`{"comment":"<script>alert(1)</script>"}`), ClientAddress: netip.MustParseAddr("192.0.2.10"),
	}
	want, err := current.Detect(req)
	if err != nil {
		b.Fatal(err)
	}
	got, err := prefiltered.Detect(req)
	if err != nil {
		b.Fatal(err)
	}
	if !reflect.DeepEqual(want, got) {
		b.Fatal("regex prefilter changed representative Coraza detections")
	}
}

func benchmarkJSONBody(size int) []byte {
	prefix := []byte(`{"value":"`)
	suffix := []byte(`"}`)
	body := make([]byte, 0, size)
	body = append(body, prefix...)
	body = append(body, bytes.Repeat([]byte("x"), size-len(prefix)-len(suffix))...)
	body = append(body, suffix...)
	return body
}
