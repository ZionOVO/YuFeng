// performance-load 对固定超文本传输协议目标执行有界并发负载并输出机器可读结果。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"yufeng/lib/kernel"
)

type loadResult struct {
	URL             string         `json:"url"`
	Requests        int            `json:"requests"`
	Concurrency     int            `json:"concurrency"`
	TargetRate      int            `json:"target_requests_per_second"`
	Completed       int            `json:"completed"`
	TransportErrors int            `json:"transport_errors"`
	StatusCounts    map[string]int `json:"status_counts"`
	DurationMicros  int64          `json:"duration_micros"`
	RequestsPerSec  float64        `json:"requests_per_second"`
	P50Micros       int64          `json:"p50_micros"`
	P95Micros       int64          `json:"p95_micros"`
	P99Micros       int64          `json:"p99_micros"`
	MaxMicros       int64          `json:"max_micros"`
	GoVersion       string         `json:"go_version"`
}

type budgetResult struct {
	P99ExtraLatencyMicros   int64   `json:"p99_extra_latency_micros"`
	ModelBypassP99Micros    int64   `json:"model_bypass_p99_micros"`
	ModelBypassCPUPercent   float64 `json:"model_bypass_cpu_percent"`
	EdgeThroughputRPS       int     `json:"edge_throughput_rps"`
	EdgeMemoryBytes         int64   `json:"edge_memory_bytes"`
	EdgeCacheDiskBytes      int64   `json:"edge_cache_disk_bytes"`
	EdgeTelemetrySpoolBytes int64   `json:"edge_telemetry_spool_bytes"`
	EdgeInFlight            int     `json:"edge_in_flight"`
	CRSVersion              string  `json:"crs_version"`
	CRSParanoia             int     `json:"crs_paranoia"`
	GoVersion               string  `json:"go_version"`
}

type runner struct {
	client *http.Client
	url    string
}

func main() {
	url := flag.String("url", "", "负载目标绝对地址")
	requests := flag.Int("requests", 20_000, "测量请求数")
	concurrency := flag.Int("concurrency", 64, "并发数")
	warmup := flag.Int("warmup", 2_000, "预热请求数")
	timeout := flag.Duration("timeout", 15*time.Second, "单请求超时")
	rate := flag.Int("rate", 0, "每秒请求目标；0 表示不节流")
	budgets := flag.Bool("budgets", false, "仅输出冻结预算")
	flag.Parse()
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	if *budgets {
		if err := enc.Encode(frozenBudgets()); err != nil {
			fatal(err)
		}
		return
	}
	if *url == "" || *requests < 1 || *concurrency < 1 || *warmup < 0 || *timeout <= 0 || *rate < 0 {
		fatal(fmt.Errorf("url, requests, concurrency, warmup and timeout are invalid"))
	}
	if *concurrency > *requests {
		*concurrency = *requests
	}
	transport := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		MaxIdleConns:        *concurrency,
		MaxIdleConnsPerHost: *concurrency,
		IdleConnTimeout:     30 * time.Second,
		DisableCompression:  true,
	}
	r := runner{client: &http.Client{Transport: transport, Timeout: *timeout}, url: *url}
	defer transport.CloseIdleConnections()
	if *warmup > 0 {
		warmConcurrency := min(*concurrency, *warmup)
		_ = r.run(*warmup, warmConcurrency, 0)
	}
	result := r.run(*requests, *concurrency, *rate)
	if err := enc.Encode(result); err != nil {
		fatal(err)
	}
	if result.Completed != result.Requests || result.TransportErrors != 0 {
		os.Exit(1)
	}
}

func frozenBudgets() budgetResult {
	return budgetResult{
		P99ExtraLatencyMicros:   kernel.P99ExtraLatency.Microseconds(),
		ModelBypassP99Micros:    kernel.ModelBypassP99Budget.Microseconds(),
		ModelBypassCPUPercent:   kernel.ModelBypassCPUPercentBudget,
		EdgeThroughputRPS:       kernel.EdgeThroughputRPS,
		EdgeMemoryBytes:         kernel.EdgeMemoryBytes,
		EdgeCacheDiskBytes:      kernel.EdgeCacheDiskBytes,
		EdgeTelemetrySpoolBytes: kernel.EdgeTelemetrySpoolBytes,
		EdgeInFlight:            kernel.EdgeInFlight,
		CRSVersion:              kernel.CRSVersion,
		CRSParanoia:             kernel.CRSParanoia,
		GoVersion:               runtime.Version(),
	}
}

func (r runner) run(requests, concurrency, targetRate int) loadResult {
	latencies := make([]int64, requests)
	results := make(chan map[int]int, concurrency)
	startAll := make(chan struct{})
	var next atomic.Int64
	var transportErrors atomic.Int64
	var wg sync.WaitGroup
	var scheduleStart time.Time
	for range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses := make(map[int]int)
			<-startAll
			for {
				index := int(next.Add(1)) - 1
				if index >= requests {
					break
				}
				if targetRate > 0 {
					due := scheduleStart.Add(time.Duration(index) * time.Second / time.Duration(targetRate))
					if wait := time.Until(due); wait > 0 {
						time.Sleep(wait)
					}
				}
				started := time.Now()
				resp, err := r.client.Get(r.url)
				if err != nil {
					latencies[index] = time.Since(started).Microseconds()
					transportErrors.Add(1)
					continue
				}
				_, copyErr := io.Copy(io.Discard, resp.Body)
				closeErr := resp.Body.Close()
				latencies[index] = time.Since(started).Microseconds()
				if copyErr != nil || closeErr != nil {
					transportErrors.Add(1)
					continue
				}
				statuses[resp.StatusCode]++
			}
			results <- statuses
		}()
	}
	started := time.Now()
	scheduleStart = started
	close(startAll)
	wg.Wait()
	elapsed := time.Since(started)
	close(results)
	statusCounts := make(map[string]int)
	completed := 0
	for statuses := range results {
		for status, count := range statuses {
			statusCounts[strconv.Itoa(status)] += count
			completed += count
		}
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	return loadResult{
		URL: r.url, Requests: requests, Concurrency: concurrency, TargetRate: targetRate, Completed: completed,
		TransportErrors: int(transportErrors.Load()), StatusCounts: statusCounts,
		DurationMicros: elapsed.Microseconds(), RequestsPerSec: float64(completed) / elapsed.Seconds(),
		P50Micros: percentile(latencies, 0.50), P95Micros: percentile(latencies, 0.95),
		P99Micros: percentile(latencies, 0.99), MaxMicros: percentile(latencies, 1),
		GoVersion: runtime.Version(),
	}
}

func percentile(sorted []int64, quantile float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	index := int(float64(len(sorted))*quantile+0.999999999) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}

func fatal(err error) {
	_, _ = fmt.Fprintln(os.Stderr, err)
	os.Exit(2)
}
