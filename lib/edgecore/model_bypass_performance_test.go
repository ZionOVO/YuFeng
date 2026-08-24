package edgecore

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"yufeng/lib/kernel"
	artifactv1 "yufeng/proto/gen/artifactv1"
	modelsidev1 "yufeng/proto/gen/modelsidev1"
)

const modelBypassPerformanceEnvironment = "YUFENG_RUN_MODEL_BYPASS_PERFORMANCE"

type modelBypassScenarioResult struct {
	Name                     string  `json:"name"`
	TargetRequestsPerSecond  int     `json:"target_requests_per_second"`
	OfferedRequestsPerSecond int     `json:"offered_requests_per_second"`
	Completed                int     `json:"completed"`
	ElapsedMicros            int64   `json:"elapsed_micros"`
	Throughput               float64 `json:"throughput_requests_per_second"`
	P99Micros                int64   `json:"p99_micros"`
	P99IncreaseMicros        int64   `json:"p99_increase_micros"`
	IngressDepth             int     `json:"ingress_depth"`
	ResultDepth              int     `json:"result_depth"`
	IngressDropped           uint64  `json:"ingress_dropped"`
	ReviewResultsDropped     uint64  `json:"review_results_dropped"`
	AlertResultsDropped      uint64  `json:"alert_results_dropped"`
	ResultUploadRetries      uint64  `json:"result_upload_retries"`
}

type modelBypassPerformanceReport struct {
	TargetRequestsPerSecond int                         `json:"target_requests_per_second"`
	P99BudgetMicros         int64                       `json:"p99_budget_micros"`
	Scenarios               []modelBypassScenarioResult `json:"scenarios"`
}

type modelBypassScenarioHarness struct {
	queue         *ModelIngressQueue
	results       chan bool
	cancel        context.CancelFunc
	wg            sync.WaitGroup
	reviewDropped atomic.Uint64
	alertDropped  atomic.Uint64
	uploadRetries atomic.Uint64
}

// TestModelBypassRequestPathNeverWaitsForSidecar 验证旁路关闭与两级队列饱和都不改变请求裁决。
func TestModelBypassRequestPathNeverWaitsForSidecar(t *testing.T) {
	set, req := modelBypassPerformanceFixture()
	view := Canonicalize(req.Method, req.Path, req.Query, req.Headers, req.Body, DefaultInspectionProfile())
	for _, scenario := range []struct {
		name  string
		queue *ModelIngressQueue
	}{
		{name: "bypass_disabled"},
		{name: "modelside_saturated", queue: saturatedModelIngressQueue(t)},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			proxy := NewReleaseProxy(set, nil, nil, "asset-performance")
			proxy.SetUnitID("unit-performance")
			if scenario.queue != nil {
				proxy.SetModelIngress(scenario.queue)
			}
			for index := 0; index <= kernel.ModelSideIngressQueueMax; index++ {
				decision := proxy.DecideRequest(context.Background(), req, "request-performance", view)
				if decision.Action != ActionAllow {
					t.Fatalf("request %d action=%v", index, decision.Action)
				}
			}
		})
	}
}

// TestModelBypassFiveScenarioCapacity 是发布时显式启用的定速容量门禁；普通 go test 不等待五个时间窗。
func TestModelBypassFiveScenarioCapacity(t *testing.T) {
	if os.Getenv(modelBypassPerformanceEnvironment) != "1" {
		t.Skip("set YUFENG_RUN_MODEL_BYPASS_PERFORMANCE=1 for the release capacity gate")
	}
	set, req := modelBypassPerformanceFixture()
	scenarios := []string{"bypass_disabled", "modelside_idle", "modelside_saturated", "brain_disconnected", "brain_disk_slow"}
	report := modelBypassPerformanceReport{
		TargetRequestsPerSecond: kernel.EdgeThroughputRPS,
		P99BudgetMicros:         kernel.ModelBypassP99Budget.Microseconds(),
		Scenarios:               make([]modelBypassScenarioResult, 0, len(scenarios)),
	}
	var baselineP99 int64
	for _, scenario := range scenarios {
		harness := newModelBypassScenarioHarness(t, scenario)
		result := runModelBypassScenario(t, set, req, scenario, harness)
		harness.stop()
		if scenario == "bypass_disabled" {
			baselineP99 = result.P99Micros
		} else if result.P99Micros > baselineP99 {
			result.P99IncreaseMicros = result.P99Micros - baselineP99
		}
		if result.Throughput < float64(kernel.EdgeThroughputRPS) {
			t.Fatalf("%s throughput %.2f is below %d requests per second", scenario, result.Throughput, kernel.EdgeThroughputRPS)
		}
		if result.P99IncreaseMicros > kernel.ModelBypassP99Budget.Microseconds() {
			t.Fatalf("%s p99 increase %dµs exceeds %s", scenario, result.P99IncreaseMicros, kernel.ModelBypassP99Budget)
		}
		report.Scenarios = append(report.Scenarios, result)
	}
	if report.Scenarios[2].IngressDropped == 0 {
		t.Fatal("saturated modelside scenario must exercise ingress dropping")
	}
	if report.Scenarios[3].ResultDepth == 0 || report.Scenarios[3].ResultUploadRetries == 0 {
		t.Fatal("disconnected Brain scenario must retain a bounded result backlog and retry independently")
	}
	if report.Scenarios[4].ResultDepth == 0 || report.Scenarios[4].ResultUploadRetries == 0 {
		t.Fatal("slow Brain disk scenario must retain a bounded result backlog and retry independently")
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("MODEL_BYPASS_PERFORMANCE %s", raw)
	if path := os.Getenv("YUFENG_MODEL_BYPASS_REPORT"); path != "" {
		if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func modelBypassPerformanceFixture() (*ReleaseSet, Request) {
	profile := kernel.DefaultModelProfile()
	profile.AllowedHeaders = []string{"content-type", "user-agent"}
	set := NewReleaseSet()
	set.modelProfile = profile
	set.modelDigest = "sha256:model-profile-performance"
	set.activeGen = &artifactv1.AssetGeneration{GenerationId: "generation-performance", GenerationSeq: 1}
	return set, Request{
		Method: "POST", Path: "/api/performance", Query: "shape=benign",
		Headers: map[string]string{"Content-Type": "application/json", "User-Agent": "yufeng-capacity-gate"},
		Body:    []byte(`{"message":"bounded model bypass"}`), UnitID: "unit-performance", AssetID: "asset-performance",
	}
}

func saturatedModelIngressQueue(t *testing.T) *ModelIngressQueue {
	t.Helper()
	queue := NewModelIngressQueue()
	queue.ch = make(chan *ModelIngressItem, 1)
	if !queue.Offer(&ModelIngressItem{
		Profile: kernel.DefaultModelProfile(),
		Traffic: &modelsidev1.NormalizedTraffic{RequestId: "prefill"},
	}) {
		t.Fatal("prefill saturated model ingress")
	}
	return queue
}

func newModelBypassScenarioHarness(t *testing.T, scenario string) *modelBypassScenarioHarness {
	t.Helper()
	harness := &modelBypassScenarioHarness{}
	if scenario == "bypass_disabled" {
		return harness
	}
	if scenario == "modelside_saturated" {
		harness.queue = saturatedModelIngressQueue(t)
		return harness
	}
	harness.queue = NewModelIngressQueue()
	harness.results = make(chan bool, 32)
	ctx, cancel := context.WithCancel(context.Background())
	harness.cancel = cancel
	harness.wg.Add(1)
	go func() {
		defer harness.wg.Done()
		for {
			_, ok := harness.queue.Take(ctx)
			if !ok {
				return
			}
			if scenario == "modelside_idle" {
				continue
			}
			alert := scenario == "brain_disconnected"
			select {
			case harness.results <- alert:
			default:
				if alert {
					harness.alertDropped.Add(1)
				} else {
					harness.reviewDropped.Add(1)
				}
				harness.uploadRetries.Add(1)
			}
		}
	}()
	if scenario == "brain_disk_slow" {
		harness.wg.Add(1)
		go func() {
			defer harness.wg.Done()
			ticker := time.NewTicker(20 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					select {
					case <-harness.results:
						harness.uploadRetries.Add(1)
					default:
					}
				}
			}
		}()
	}
	return harness
}

func (h *modelBypassScenarioHarness) stop() {
	if h.cancel != nil {
		h.cancel()
		h.wg.Wait()
	}
}

func runModelBypassScenario(t *testing.T, set *ReleaseSet, req Request, name string, harness *modelBypassScenarioHarness) modelBypassScenarioResult {
	t.Helper()
	proxy := NewReleaseProxy(set, nil, nil, "asset-performance")
	proxy.SetUnitID("unit-performance")
	if harness.queue != nil {
		proxy.SetModelIngress(harness.queue)
	}
	// 门禁以高于冻结目标 5% 的固定节奏留出计时器抖动，实测速率仍必须不低于目标。
	offeredRequests := kernel.EdgeThroughputRPS + kernel.EdgeThroughputRPS/20
	latencies := make([]int64, 0, offeredRequests)
	interval := time.Second / time.Duration(offeredRequests)
	started := time.Now()
	for index := 0; index < offeredRequests; index++ {
		if wait := time.Until(started.Add(time.Duration(index) * interval)); wait > 0 {
			time.Sleep(wait)
		}
		requestStarted := time.Now()
		view := Canonicalize(req.Method, req.Path, req.Query, req.Headers, req.Body, DefaultInspectionProfile())
		decision := proxy.DecideRequest(context.Background(), req, "request-performance", view)
		if decision.Action != ActionAllow {
			t.Fatalf("%s request %d action=%v", name, index, decision.Action)
		}
		latencies = append(latencies, time.Since(requestStarted).Microseconds())
	}
	elapsed := time.Since(started)
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	result := modelBypassScenarioResult{
		Name: name, TargetRequestsPerSecond: kernel.EdgeThroughputRPS, OfferedRequestsPerSecond: offeredRequests, Completed: len(latencies),
		ElapsedMicros: elapsed.Microseconds(), Throughput: float64(len(latencies)) / elapsed.Seconds(),
		P99Micros:            latencies[(len(latencies)*99+99)/100-1],
		ReviewResultsDropped: harness.reviewDropped.Load(), AlertResultsDropped: harness.alertDropped.Load(),
		ResultUploadRetries: harness.uploadRetries.Load(),
	}
	if harness.queue != nil {
		result.IngressDepth = harness.queue.Depth()
		result.IngressDropped = harness.queue.Dropped()
	}
	if harness.results != nil {
		result.ResultDepth = len(harness.results)
	}
	return result
}
