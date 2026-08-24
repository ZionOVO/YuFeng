package observability

import (
	"fmt"
	"strings"
	"sync"
)

// Metrics 是生产最低必备指标寄存器。
type Metrics struct {
	mu   sync.Mutex
	vals map[string]float64
}

const (
	MetricReleaseSyncDelay = "yufeng_release_sync_delay_seconds"
	MetricTelemetryDropped = "yufeng_telemetry_dropped_total"
	MetricQueueBacklog     = "yufeng_queue_backlog"
	MetricLeaseExpired     = "yufeng_lease_expired_total"
	MetricDetectorErrors   = "yufeng_detector_errors_total"
	MetricAutoRollback     = "yufeng_auto_rollback_total"
)

// RequiredMetricNames 是生产可观测性合同固定的指标名。
var RequiredMetricNames = []string{
	MetricReleaseSyncDelay,
	MetricTelemetryDropped,
	MetricQueueBacklog,
	MetricLeaseExpired,
	MetricDetectorErrors,
	MetricAutoRollback,
}

var defaultMetrics = NewMetrics()

// NewMetrics 构造全零必备指标。
func NewMetrics() *Metrics {
	m := &Metrics{vals: map[string]float64{}}
	for _, n := range RequiredMetricNames {
		m.vals[n] = 0
	}
	return m
}

// Default 返回进程默认指标。
func Default() *Metrics { return defaultMetrics }

// Add 累加计数。
func (m *Metrics) Add(name string, delta float64) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.vals[name] += delta
	m.mu.Unlock()
}

// Set 覆盖仪表值。
func (m *Metrics) Set(name string, v float64) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.vals[name] = v
	m.mu.Unlock()
}

// Get 读取当前值。
func (m *Metrics) Get(name string) float64 {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.vals[name]
}

// Prometheus 渲染文本格式。
func (m *Metrics) Prometheus() string {
	if m == nil {
		return ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var b strings.Builder
	for _, n := range RequiredMetricNames {
		_, _ = fmt.Fprintf(&b, "# TYPE %s gauge\n%s %g\n", n, n, m.vals[n])
	}
	return b.String()
}
