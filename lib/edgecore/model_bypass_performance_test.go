package edgecore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"yufeng/lib/kernel"
	artifactv1 "yufeng/proto/gen/artifactv1"
	modelsidev1 "yufeng/proto/gen/modelsidev1"
)

const (
	modelBypassPerformanceEnvironment = "YUFENG_RUN_MODEL_BYPASS_PERFORMANCE"
	modelBypassPerformanceDuration    = "YUFENG_MODEL_BYPASS_MEASURE_DURATION"
	modelBypassPerformanceWarmup      = "YUFENG_MODEL_BYPASS_WARMUP_DURATION"
	modelBypassPerformanceRepeats     = "YUFENG_MODEL_BYPASS_REPEATS"
	modelBypassPerformanceSmoke       = "YUFENG_MODEL_BYPASS_SMOKE"

	modelBypassLoadDisabled    = "bypass_disabled"
	modelBypassLoadIdle        = "modelside_idle"
	modelBypassLoadStable      = "modelside_stable"
	modelBypassLoadFull        = "modelside_full"
	modelBypassLoadUnreachable = "modelside_unreachable"
	modelBypassConcurrency     = 64
)

type modelBypassPerformanceBody struct {
	name string
	body []byte
}

type modelBypassPerformanceWindow struct {
	name    string
	desired *artifactv1.ModelIngressWindow
}

type modelBypassScenarioResult struct {
	Name                     string  `json:"name"`
	Load                     string  `json:"load"`
	Body                     string  `json:"body"`
	BodyBytes                int     `json:"body_bytes"`
	Window                   string  `json:"window"`
	Repeat                   int     `json:"repeat"`
	TargetRequestsPerSecond  int     `json:"target_requests_per_second"`
	OfferedRequestsPerSecond int     `json:"offered_requests_per_second"`
	Concurrency              int     `json:"concurrency"`
	Scheduled                int     `json:"scheduled"`
	Completed                int     `json:"completed"`
	LoadGeneratorDropped     int     `json:"load_generator_dropped"`
	ElapsedMicros            int64   `json:"elapsed_micros"`
	Throughput               float64 `json:"throughput_requests_per_second"`
	P50Micros                int64   `json:"p50_micros"`
	P95Micros                int64   `json:"p95_micros"`
	P99Micros                int64   `json:"p99_micros"`
	P99IncreaseMicros        int64   `json:"p99_increase_micros"`
	CPUPercent               float64 `json:"cpu_percent"`
	CPUPercentIncrease       float64 `json:"cpu_percent_increase"`
	ResidentBytes            uint64  `json:"resident_bytes"`
	DesiredItems             uint32  `json:"desired_items"`
	DesiredBytes             uint64  `json:"desired_bytes"`
	QueuedItems              uint64  `json:"queued_items"`
	QueuedBytes              uint64  `json:"queued_bytes"`
	InFlightItems            uint64  `json:"in_flight_items"`
	InFlightBytes            uint64  `json:"in_flight_bytes"`
	OldestAgeMillis          int64   `json:"oldest_age_millis"`
	EvictedOldest            uint64  `json:"evicted_oldest"`
	Expired                  uint64  `json:"expired"`
	ItemTooLarge             uint64  `json:"item_too_large"`
	InFlightCapacity         uint64  `json:"in_flight_capacity"`
	TransportFailed          uint64  `json:"transport_failed"`
	ModelSideRejected        uint64  `json:"modelside_rejected"`
	AdmissionBudget          uint64  `json:"admission_budget"`
}

type modelBypassPerformanceReport struct {
	SchemaVersion           string                      `json:"schema_version"`
	TargetRequestsPerSecond int                         `json:"target_requests_per_second"`
	P99BudgetMicros         int64                       `json:"p99_budget_micros"`
	CPUPercentBudget        float64                     `json:"cpu_percent_budget"`
	ResidentBytesBudget     int64                       `json:"resident_bytes_budget"`
	MeasurementSeconds      float64                     `json:"measurement_seconds"`
	WarmupSeconds           float64                     `json:"warmup_seconds"`
	Repeats                 int                         `json:"repeats"`
	RealCoraza              bool                        `json:"real_coraza"`
	QualificationRun        bool                        `json:"qualification_run"`
	Scenarios               []modelBypassScenarioResult `json:"scenarios"`
}

type modelBypassProcessUsage struct {
	cpuSeconds    float64
	residentBytes uint64
}

type modelBypassLoadHarness struct {
	queue  *ModelIngressQueue
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// TestModelIngressWindowNeverBlocksRequestPath 验证旁路关闭和窗口饱和都不改变真实 Coraza 裁决。
func TestModelIngressWindowNeverBlocksRequestPath(t *testing.T) {
	set, req := modelBypassPerformanceFixture(t, []byte(`{"message":"bounded model bypass"}`))
	view := Canonicalize(req.Method, req.Path, req.Query, req.Headers, req.Body, DefaultInspectionProfile())
	for _, scenario := range []struct {
		name  string
		queue *ModelIngressQueue
	}{
		{name: modelBypassLoadDisabled},
		{name: "edge_window_saturated", queue: saturatedModelIngressQueue(t)},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			proxy := NewReleaseProxy(set, nil, nil, "asset-performance")
			proxy.SetUnitID("unit-performance")
			if scenario.queue != nil {
				proxy.SetModelIngress(scenario.queue)
			}
			for index := 0; index <= kernel.ModelIngressBatchMaxItems; index++ {
				decision := proxy.DecideRequest(context.Background(), req, "request-performance", view)
				if decision.Action != ActionAllow {
					t.Fatalf("request %d action=%v", index, decision.Action)
				}
			}
		})
	}
}

// TestModelIngressWindowCapacityMatrix 是显式启用的真实 Coraza 发布容量门禁。
func TestModelIngressWindowCapacityMatrix(t *testing.T) {
	if os.Getenv(modelBypassPerformanceEnvironment) != "1" {
		t.Skip("set YUFENG_RUN_MODEL_BYPASS_PERFORMANCE=1 for the release capacity gate")
	}
	measurement := modelBypassPerformanceDurationValue(t, modelBypassPerformanceDuration, 60*time.Second)
	warmup := modelBypassPerformanceDurationValue(t, modelBypassPerformanceWarmup, 5*time.Second)
	repeats := modelBypassPerformanceRepeatValue(t)
	smoke := os.Getenv(modelBypassPerformanceSmoke) == "1"
	qualification := measurement >= 60*time.Second && repeats >= 3
	if !smoke && !qualification {
		t.Fatal("a qualification run requires at least 60 seconds and three repeats; set YUFENG_MODEL_BYPASS_SMOKE=1 only for a non-qualifying smoke run")
	}

	report := modelBypassPerformanceReport{
		SchemaVersion:           "model-ingress-window-capacity/v2",
		TargetRequestsPerSecond: kernel.EdgeThroughputRPS,
		P99BudgetMicros:         kernel.ModelBypassP99Budget.Microseconds(),
		CPUPercentBudget:        kernel.ModelBypassCPUPercentBudget,
		ResidentBytesBudget:     kernel.EdgeMemoryBytes,
		MeasurementSeconds:      measurement.Seconds(),
		WarmupSeconds:           warmup.Seconds(),
		Repeats:                 repeats,
		RealCoraza:              true,
		QualificationRun:        qualification && !smoke,
	}
	loads := []string{modelBypassLoadIdle, modelBypassLoadStable, modelBypassLoadFull, modelBypassLoadUnreachable}
	for repeat := 1; repeat <= repeats; repeat++ {
		for _, body := range modelBypassPerformanceBodies() {
			set, req := modelBypassPerformanceFixture(t, body.body)
			baseline := runModelBypassScenario(t, set, req, body.name, "not_applicable", repeat, modelBypassLoadDisabled, nil, warmup, measurement)
			report.Scenarios = append(report.Scenarios, baseline)
			validateModelBypassScenario(t, baseline, smoke)
			for _, window := range modelBypassPerformanceWindows() {
				for _, load := range loads {
					harness := newModelBypassLoadHarness(t, load, window.desired)
					result := runModelBypassScenario(t, set, req, body.name, window.name, repeat, load, harness.queue, warmup, measurement)
					harness.stop()
					if result.P99Micros > baseline.P99Micros {
						result.P99IncreaseMicros = result.P99Micros - baseline.P99Micros
					}
					if result.CPUPercent > baseline.CPUPercent {
						result.CPUPercentIncrease = result.CPUPercent - baseline.CPUPercent
					}
					validateModelBypassScenario(t, result, smoke)
					validateModelBypassLoadEvidence(t, result)
					report.Scenarios = append(report.Scenarios, result)
				}
			}
		}
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("MODEL_INGRESS_WINDOW_PERFORMANCE %s", raw)
	if path := os.Getenv("YUFENG_MODEL_BYPASS_REPORT"); path != "" {
		if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func modelBypassPerformanceBodies() []modelBypassPerformanceBody {
	return []modelBypassPerformanceBody{
		{name: "small", body: []byte(`{"message":"bounded model bypass"}`)},
		{name: "near_inspection_limit", body: bytes.Repeat([]byte("a"), kernel.EngineBodyLimitBytes-1)},
	}
}

func modelBypassPerformanceWindows() []modelBypassPerformanceWindow {
	return []modelBypassPerformanceWindow{
		{name: "default", desired: kernel.DefaultModelIngressWindow()},
		{name: "local_hard_limit", desired: kernel.DefaultModelIngressHardLimit()},
	}
}

func modelBypassPerformanceFixture(t *testing.T, body []byte) (*ReleaseSet, Request) {
	t.Helper()
	crs, err := SharedCoraza()
	if err != nil {
		t.Fatal(err)
	}
	profile := kernel.DefaultModelProfile()
	profile.AllowedHeaders = []string{"content-type", "user-agent"}
	set := NewReleaseSet()
	set.inspectors = []Inspector{crs}
	set.modelProfile = profile
	set.modelDigest = "sha256:model-profile-performance"
	set.activeGen = &artifactv1.AssetGeneration{GenerationId: "generation-performance", GenerationSeq: 1}
	return set, Request{
		Method: "POST", Path: "/api/performance", Query: "shape=benign",
		Headers: map[string]string{"Content-Type": "text/plain", "User-Agent": "yufeng-capacity-gate"},
		Body:    body, UnitID: "unit-performance", AssetID: "asset-performance",
	}
}

func saturatedModelIngressQueue(t *testing.T) *ModelIngressQueue {
	t.Helper()
	queue, err := NewModelIngressQueueWithHardLimit(modelIngressTestWindow(1, 1<<20, time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.Configure(modelIngressTestWindow(1, 1<<20, time.Minute)); err != nil {
		t.Fatal(err)
	}
	if !queue.Offer(&ModelIngressItem{
		Profile: kernel.DefaultModelProfile(),
		Traffic: &modelsidev1.NormalizedTraffic{RequestId: "prefill", ModelProfileDigest: "profile-performance"},
	}) {
		t.Fatal("prefill saturated model ingress")
	}
	return queue
}

func newModelBypassLoadHarness(t *testing.T, load string, desired *artifactv1.ModelIngressWindow) *modelBypassLoadHarness {
	t.Helper()
	queue, err := NewModelIngressQueueWithHardLimit(kernel.DefaultModelIngressHardLimit())
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.Configure(desired); err != nil {
		t.Fatal(err)
	}
	harness := &modelBypassLoadHarness{queue: queue}
	ctx, cancel := context.WithCancel(context.Background())
	harness.cancel = cancel
	batches := make(chan *ModelIngressBatch)
	harness.wg.Add(1)
	go func() {
		defer harness.wg.Done()
		defer close(batches)
		for {
			batch, ok := queue.TakeBatch(ctx, kernel.ModelIngressBatchMaxItems, kernel.ModelIngressBatchMaxBytes, kernel.ModelIngressBatchWait)
			if !ok {
				return
			}
			select {
			case batches <- batch:
			case <-ctx.Done():
				queue.CompleteBatch(batch, 0, true)
				return
			}
		}
	}()
	for range kernel.ModelSideIngressWorkers {
		harness.wg.Add(1)
		go func() {
			defer harness.wg.Done()
			modelBypassConsumeBatches(ctx, queue, batches, load)
		}()
	}
	return harness
}

func modelBypassConsumeBatches(ctx context.Context, queue *ModelIngressQueue, batches <-chan *ModelIngressBatch, load string) {
	for batch := range batches {
		accepted := uint32(len(batch.Traffic))
		transportFailed := false
		var delay time.Duration
		switch load {
		case modelBypassLoadStable:
			delay = 20 * time.Millisecond
		case modelBypassLoadFull:
			accepted = 0
		case modelBypassLoadUnreachable:
			delay = kernel.ModelSideIngressTimeout
			accepted = 0
			transportFailed = true
		}
		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				queue.CompleteBatch(batch, 0, true)
				return
			case <-timer.C:
			}
		}
		queue.CompleteBatch(batch, accepted, transportFailed)
		if ctx.Err() != nil {
			return
		}
	}
}

func (h *modelBypassLoadHarness) stop() {
	if h == nil || h.cancel == nil {
		return
	}
	h.cancel()
	h.wg.Wait()
}

func runModelBypassScenario(t *testing.T, set *ReleaseSet, req Request, bodyName, windowName string, repeat int, load string, queue *ModelIngressQueue, warmup, measurement time.Duration) modelBypassScenarioResult {
	t.Helper()
	runtime.GC()
	debug.FreeOSMemory()
	proxy := NewReleaseProxy(set, nil, nil, "asset-performance")
	proxy.SetUnitID("unit-performance")
	if queue != nil {
		proxy.SetModelIngress(queue)
	}
	if warmup > 0 {
		modelBypassRunRequests(t, proxy, req, warmup, false)
	}
	usageBefore := modelBypassReadProcessUsage(t)
	latencies, elapsed, scheduled, completed, loadGeneratorDropped := modelBypassRunRequests(t, proxy, req, measurement, true)
	usageAfter := modelBypassReadProcessUsage(t)
	result := modelBypassScenarioResult{
		Name: fmt.Sprintf("%s/%s/%s/repeat-%d", load, bodyName, windowName, repeat),
		Load: load, Body: bodyName, BodyBytes: len(req.Body), Window: windowName, Repeat: repeat,
		TargetRequestsPerSecond:  kernel.EdgeThroughputRPS,
		OfferedRequestsPerSecond: kernel.EdgeThroughputRPS + kernel.EdgeThroughputRPS/20,
		Concurrency:              modelBypassConcurrency,
		Scheduled:                scheduled,
		Completed:                completed,
		LoadGeneratorDropped:     loadGeneratorDropped,
		ElapsedMicros:            elapsed.Microseconds(),
		Throughput:               float64(completed) / elapsed.Seconds(),
		P50Micros:                modelBypassPercentile(latencies, 50),
		P95Micros:                modelBypassPercentile(latencies, 95),
		P99Micros:                modelBypassPercentile(latencies, 99),
		CPUPercent:               (usageAfter.cpuSeconds - usageBefore.cpuSeconds) / elapsed.Seconds() * 100,
		ResidentBytes:            usageAfter.residentBytes,
	}
	if queue != nil {
		snapshot := queue.Snapshot()
		result.DesiredItems = snapshot.Desired.GetMaxItems()
		result.DesiredBytes = snapshot.Desired.GetMaxRetainedBytes()
		result.QueuedItems = snapshot.QueuedItems
		result.QueuedBytes = snapshot.QueuedBytes
		result.InFlightItems = snapshot.InFlightItems
		result.InFlightBytes = snapshot.InFlightBytes
		result.OldestAgeMillis = snapshot.OldestAge.Milliseconds()
		result.EvictedOldest = snapshot.Drops.GetEvictedOldest()
		result.Expired = snapshot.Drops.GetExpired()
		result.ItemTooLarge = snapshot.Drops.GetItemTooLarge()
		result.InFlightCapacity = snapshot.Drops.GetInFlightCapacity()
		result.TransportFailed = snapshot.Drops.GetTransportFailed()
		result.ModelSideRejected = snapshot.Drops.GetModelsideRejected()
		result.AdmissionBudget = snapshot.Drops.GetAdmissionBudget()
	}
	return result
}

func modelBypassRunRequests(t *testing.T, proxy *ReleaseProxy, req Request, duration time.Duration, record bool) ([]int64, time.Duration, int, int, int) {
	t.Helper()
	offeredRequests := kernel.EdgeThroughputRPS + kernel.EdgeThroughputRPS/20
	scheduled := int(math.Ceil(float64(offeredRequests) * duration.Seconds()))
	if scheduled < 1 {
		scheduled = 1
	}
	type modelBypassJob struct {
		index int
		due   time.Time
	}
	latencies := make([]int64, scheduled)
	jobs := make(chan modelBypassJob, modelBypassConcurrency)
	interval := time.Second / time.Duration(offeredRequests)
	started := time.Now()
	var rejected atomic.Int64
	var completed atomic.Int64
	var workers sync.WaitGroup
	for range min(modelBypassConcurrency, scheduled) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for job := range jobs {
				view := Canonicalize(req.Method, req.Path, req.Query, req.Headers, req.Body, DefaultInspectionProfile())
				decision := proxy.DecideRequest(context.Background(), req, "request-performance", view)
				if decision.Action != ActionAllow {
					rejected.Add(1)
				}
				if record {
					latencies[job.index] = time.Since(job.due).Microseconds()
				}
				completed.Add(1)
			}
		}()
	}
	loadGeneratorDropped := 0
	for index := 0; index < scheduled; index++ {
		due := started.Add(time.Duration(index) * interval)
		if wait := time.Until(due); wait > 0 {
			time.Sleep(wait)
		}
		select {
		case jobs <- modelBypassJob{index: index, due: due}:
		default:
			loadGeneratorDropped++
		}
	}
	close(jobs)
	workers.Wait()
	if rejected.Load() != 0 {
		t.Fatalf("%d requests changed the Coraza decision", rejected.Load())
	}
	if record && loadGeneratorDropped > 0 {
		kept := latencies[:0]
		for _, latency := range latencies {
			if latency > 0 {
				kept = append(kept, latency)
			}
		}
		latencies = kept
	}
	return latencies, time.Since(started), scheduled, int(completed.Load()), loadGeneratorDropped
}

func modelBypassPercentile(values []int64, percentile int) int64 {
	if len(values) == 0 {
		return 0
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	index := (len(values)*percentile+99)/100 - 1
	return values[index]
}

func modelBypassReadProcessUsage(t *testing.T) modelBypassProcessUsage {
	t.Helper()
	var usage unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_SELF, &usage); err != nil {
		t.Fatal(err)
	}
	resident := uint64(usage.Maxrss)
	if runtime.GOOS != "darwin" {
		resident *= 1024
	}
	return modelBypassProcessUsage{
		cpuSeconds:    float64(usage.Utime.Sec) + float64(usage.Utime.Usec)/1e6 + float64(usage.Stime.Sec) + float64(usage.Stime.Usec)/1e6,
		residentBytes: resident,
	}
}

func modelBypassPerformanceDurationValue(t *testing.T, name string, fallback time.Duration) time.Duration {
	t.Helper()
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		t.Fatalf("%s must be a positive duration", name)
	}
	return value
}

func modelBypassPerformanceRepeatValue(t *testing.T) int {
	t.Helper()
	raw := os.Getenv(modelBypassPerformanceRepeats)
	if raw == "" {
		return 3
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		t.Fatalf("%s must be a positive integer", modelBypassPerformanceRepeats)
	}
	return value
}

func validateModelBypassScenario(t *testing.T, result modelBypassScenarioResult, smoke bool) {
	t.Helper()
	if smoke {
		return
	}
	if result.Throughput < float64(kernel.EdgeThroughputRPS) {
		t.Errorf("%s throughput %.2f is below %d requests per second", result.Name, result.Throughput, kernel.EdgeThroughputRPS)
	}
	if result.LoadGeneratorDropped != 0 {
		t.Errorf("%s load generator dropped %d scheduled requests", result.Name, result.LoadGeneratorDropped)
	}
	if result.P99IncreaseMicros > kernel.ModelBypassP99Budget.Microseconds() {
		t.Errorf("%s p99 increase %dµs exceeds %s", result.Name, result.P99IncreaseMicros, kernel.ModelBypassP99Budget)
	}
	if result.CPUPercentIncrease > kernel.ModelBypassCPUPercentBudget {
		t.Errorf("%s CPU increase %.2f percentage points exceeds %.2f", result.Name, result.CPUPercentIncrease, kernel.ModelBypassCPUPercentBudget)
	}
	if result.ResidentBytes > uint64(kernel.EdgeMemoryBytes) {
		t.Errorf("%s resident memory %d exceeds %d bytes", result.Name, result.ResidentBytes, kernel.EdgeMemoryBytes)
	}
}

func validateModelBypassLoadEvidence(t *testing.T, result modelBypassScenarioResult) {
	t.Helper()
	switch result.Load {
	case modelBypassLoadFull:
		if result.ModelSideRejected == 0 {
			t.Errorf("%s did not exercise ModelSide rejection", result.Name)
		}
	case modelBypassLoadUnreachable:
		if result.TransportFailed == 0 && result.InFlightItems == 0 {
			t.Errorf("%s did not exercise an unreachable ModelSide", result.Name)
		}
	}
}
