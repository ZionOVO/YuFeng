package edgecore

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"yufeng/lib/kernel"
	commonv1 "yufeng/proto/gen/commonv1"
)

const envoyAuthPartialBodyHeader = "X-Envoy-Auth-Partial-Body"

// GateFunc 根据规范视图与原始请求给出闸门裁决。
type GateFunc func(view CanonicalView, req Request) Action

// ExtAuthz 是 Envoy 外部授权超文本传输协议适配器：先变成同一规范视图再进闸门。
// 单次检查超时失败即开（业务 200）；窗内超时率超阈值后对该入口 503。
type ExtAuthz struct {
	gate    GateFunc
	assetID string
	unitID  string
	source  *ClientSourceResolver
	now     func() time.Time
	timeout time.Duration

	mu             sync.Mutex
	events         []timeoutSample
	tripped        bool
	belowSince     time.Time
	halfOpen       []time.Time
	Timeouts       int
	Checks         int
	Trips          int
	LastSkipReason string
}

type timeoutSample struct {
	at      time.Time
	timeout bool
}

// NewExtAuthz 构造外部授权壳。
func NewExtAuthz(assetID string, gate GateFunc) *ExtAuthz {
	if gate == nil {
		gate = func(CanonicalView, Request) Action { return ActionAllow }
	}
	source, _ := NewClientSourceResolver(nil)
	return &ExtAuthz{
		gate:    gate,
		assetID: assetID,
		source:  source,
		now:     time.Now,
		timeout: kernel.ExtAuthzTimeout,
	}
}

// SetClientSourceResolver 设置已验证监听计划编译的来源取信策略。
func (e *ExtAuthz) SetClientSourceResolver(resolver *ClientSourceResolver) {
	if resolver != nil {
		e.source = resolver
	}
}

// SetUnitID 写入本入口的单元标识，供金丝雀分桶。
func (e *ExtAuthz) SetUnitID(unitID string) {
	e.unitID = strings.TrimSpace(unitID)
}

// ServeHTTP 实现 Envoy 超文本传输协议外部授权：成功状态放行、禁止访问状态拒绝，熔断后执行半开探测。
func (e *ExtAuthz) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	e.Checks++
	tripped := e.tripped
	admit := true
	if tripped {
		admit = e.tryHalfOpenLocked(e.now())
	}
	e.mu.Unlock()
	if tripped && !admit {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"code":"unavailable","message":"ext_authz circuit open"}` + "\n"))
		return
	}

	body, oversize, total := readInspectionBody(r)
	if envoyAuthBodyPartial(r.Header) {
		oversize = true
		if total <= int64(len(body)) {
			total = int64(len(body)) + 1
		}
	}
	headers := flattenHeaders(r.Header)
	delete(headers, envoyAuthPartialBodyHeader)
	if r.Host != "" {
		headers["Host"] = r.Host
	}
	hdr := r.Header.Clone()
	hdr.Del(envoyAuthPartialBodyHeader)
	if r.Host != "" {
		hdr.Set("Host", r.Host)
	}
	view := CanonicalizeHTTP(r.Method, r.URL.Path, r.URL.RawQuery, hdr, body, DefaultInspectionProfile())
	if oversize {
		MarkBodyPartial(&view, int64(len(body)), total)
	}
	if r.ContentLength > 0 && len(body) == 0 {
		for i := range view.Coverage {
			if view.Coverage[i].Target == commonv1.InspectionSurface_INSPECTION_SURFACE_BODY {
				view.Coverage[i].Status = commonv1.CoverageStatus_COVERAGE_STATUS_ABSENT
				view.Coverage[i].Inspected = 0
			}
		}
		e.mu.Lock()
		e.LastSkipReason = "coverage"
		e.mu.Unlock()
	}
	req := Request{
		AssetID: e.assetID,
		UnitID:  e.unitID,
		Method:  r.Method,
		Path:    r.URL.Path,
		Query:   r.URL.RawQuery,
		Headers: headers,
		Body:    body,
	}
	req.ClientAddress = e.source.Resolve(r.RemoteAddr, r.Header)

	posture := commonv1.IngressPosture_INGRESS_POSTURE_EXT_AUTHZ
	if oversize && len(body) > 0 {
		e.record(false)
		code, _ := HTTPStatus(StatusInput{Posture: posture, Oversize: true, BodyPresent: true, View: view})
		w.WriteHeader(code)
		_, _ = w.Write([]byte(`{"blocked":true,"reason":"oversize"}` + "\n"))
		return
	}
	if view.Rejected {
		e.record(false)
		code, _ := HTTPStatus(StatusInput{Posture: posture, View: view})
		w.WriteHeader(code)
		_, _ = w.Write([]byte(`{"blocked":true,"reason":"rejected"}` + "\n"))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), e.timeout)
	defer cancel()
	done := make(chan Action, 1)
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				done <- ActionAllow
			}
		}()
		done <- e.gate(view, req)
	}()
	var action Action
	select {
	case action = <-done:
		e.record(false)
	case <-ctx.Done():
		e.record(true)
		code, _ := HTTPStatus(StatusInput{Posture: posture, ExtAuthzTimeout: true, View: view})
		w.WriteHeader(code)
		return
	}
	code, _ := HTTPStatus(StatusInput{Posture: posture, GateAction: action, View: view})
	if action == ActionBlock {
		w.WriteHeader(code)
		_, _ = w.Write([]byte(`{"blocked":true}` + "\n"))
		return
	}
	w.WriteHeader(code)
}

func envoyAuthBodyPartial(headers http.Header) bool {
	for _, value := range headers.Values(envoyAuthPartialBodyHeader) {
		for part := range strings.SplitSeq(value, ",") {
			if strings.EqualFold(strings.TrimSpace(part), "true") {
				return true
			}
		}
	}
	return false
}

func (e *ExtAuthz) tryHalfOpenLocked(now time.Time) bool {
	cut := now.Add(-time.Second)
	kept := e.halfOpen[:0]
	for _, t := range e.halfOpen {
		if !t.Before(cut) {
			kept = append(kept, t)
		}
	}
	e.halfOpen = kept
	if len(e.halfOpen) >= int(kernel.ExtAuthzHalfOpenPerSec) {
		return false
	}
	e.halfOpen = append(e.halfOpen, now)
	return true
}

func (e *ExtAuthz) record(timeout bool) {
	now := e.now()
	e.mu.Lock()
	defer e.mu.Unlock()
	if timeout {
		e.Timeouts++
	}
	e.events = append(e.events, timeoutSample{at: now, timeout: timeout})
	cut := now.Add(-kernel.ExtAuthzTimeoutRateWindow)
	kept := e.events[:0]
	var total, timed int
	for _, s := range e.events {
		if s.at.Before(cut) {
			continue
		}
		kept = append(kept, s)
		total++
		if s.timeout {
			timed++
		}
	}
	e.events = kept
	const minSamples = 20
	rate := 0.0
	if total > 0 {
		rate = float64(timed) / float64(total)
	}
	if !e.tripped && total >= minSamples && rate >= kernel.ExtAuthzTimeoutRateTrip {
		e.tripped = true
		e.Trips++
		e.belowSince = time.Time{}
		return
	}
	if e.tripped {
		if rate <= kernel.ExtAuthzTimeoutRateRecover {
			if e.belowSince.IsZero() {
				e.belowSince = now
			}
			if now.Sub(e.belowSince) >= kernel.ExtAuthzTimeoutRateRecoverHold {
				e.tripped = false
				e.belowSince = time.Time{}
			}
		} else {
			e.belowSince = time.Time{}
		}
	}
}
