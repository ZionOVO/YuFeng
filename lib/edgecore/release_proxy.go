package edgecore

import (
	"context"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	commonv1 "yufeng/proto/gen/commonv1"

	"yufeng/lib/kernel"
	"yufeng/lib/observability"
)

// DecisionObserver 观察每次请求的最终裁决；用于遥测上报与心跳计数。
type DecisionObserver func(req Request, dec Decision, requestID string)

// ReleaseProxy 是发布集驱动的入口壳：规范视图 → Inspect → Gate → 按姿态写状态码。
type ReleaseProxy struct {
	set        *ReleaseSet
	telemetry  *Telemetry
	rp         *httputil.ReverseProxy
	assetID    string
	unitID     string
	source     *ClientSourceResolver
	obsMu      sync.RWMutex
	observer   DecisionObserver
	evidence   *EvidenceRing
	modelQueue *ModelIngressQueue
	inflight   atomic.Int64
	winMu      sync.Mutex
	winReqs    uint64
	winRoutes  []string
}

// NewReleaseProxy 构造发布集驱动的代理。observer 经 SetObserver 注入，可为空。
func NewReleaseProxy(set *ReleaseSet, tel *Telemetry, upstream *url.URL, assetID string) *ReleaseProxy {
	rp := &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) {
		r.SetURL(upstream)
		r.Out.Host = upstream.Host
		r.SetXForwarded()
	},
	}
	rp.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `{"error":"upstream unavailable"}`+"\n")
	}
	source, _ := NewClientSourceResolver(nil)
	return &ReleaseProxy{set: set, telemetry: tel, rp: rp, assetID: assetID, source: source}
}

// SetClientSourceResolver 设置已验证监听计划编译的来源取信策略。
func (p *ReleaseProxy) SetClientSourceResolver(resolver *ClientSourceResolver) {
	if resolver != nil {
		p.source = resolver
	}
}

// SetObserver 设置裁决观察者；与 ServeHTTP 并发调用安全。
func (p *ReleaseProxy) SetObserver(o DecisionObserver) {
	p.obsMu.Lock()
	p.observer = o
	p.obsMu.Unlock()
}

func (p *ReleaseProxy) currentObserver() DecisionObserver {
	p.obsMu.RLock()
	defer p.obsMu.RUnlock()
	return p.observer
}

// SetEvidence 挂本地证据环缓冲；原文只留本进程 15 分钟。
func (p *ReleaseProxy) SetEvidence(r *EvidenceRing) {
	p.obsMu.Lock()
	p.evidence = r
	p.obsMu.Unlock()
}

// SetModelIngress 挂 Edge 到 ModelSide 的本地非阻塞流量队列。
func (p *ReleaseProxy) SetModelIngress(q *ModelIngressQueue) {
	p.obsMu.Lock()
	p.modelQueue = q
	p.obsMu.Unlock()
}

// SetUnitID 写入金丝雀分桶用的单元标识。
func (p *ReleaseProxy) SetUnitID(unitID string) {
	p.unitID = unitID
}

// SetPosture 设置入口姿态（不进世代）。
func (p *ReleaseProxy) SetPosture(posture commonv1.IngressPosture) {
	if p.set != nil {
		p.set.SetPosture(posture)
	}
}

// WindowSnapshot 取出并清零本心跳窗计数。
func (p *ReleaseProxy) WindowSnapshot() (reqs uint64, routes []string) {
	p.winMu.Lock()
	defer p.winMu.Unlock()
	reqs = p.winReqs
	routes = append([]string(nil), p.winRoutes...)
	p.winReqs = 0
	p.winRoutes = nil
	return reqs, routes
}

func (p *ReleaseProxy) noteWindow(method, path string) {
	p.winMu.Lock()
	p.winReqs++
	if len(p.winRoutes) < 32 {
		p.winRoutes = append(p.winRoutes, method+" "+path)
	}
	p.winMu.Unlock()
}

// DecideRequest 依次执行检查与闸门裁决，并记录统计窗口、证据、旁路与观察者；本函数不写网络响应。
// 外部授权壳与反代壳共用，避免只换适配器就丢掉事件。
func (p *ReleaseProxy) DecideRequest(ctx context.Context, req Request, requestID string, view CanonicalView) Decision {
	dec := p.set.InspectThenGate(ctx, req, requestID, view)
	p.noteWindow(req.Method, req.Path)
	p.obsMu.RLock()
	ring := p.evidence
	modelQueue := p.modelQueue
	p.obsMu.RUnlock()
	if ring != nil {
		// 完整正文只转移给本地模型；智能代理证据环最多保留规范查询串。
		ring.Put(requestID, []byte(req.Query), time.Now())
	}
	if modelQueue != nil {
		profile, profileDigest := p.set.modelProfileSnapshot()
		item := NewNormalizedModelTraffic(requestID, req.UnitID, p.assetID, dec.GenerationID, dec.GenerationSeq,
			profile, profileDigest, view, time.Now())
		if item != nil {
			modelQueue.Offer(item)
			// 无论队列是否接受，正文都不得再进入 Brain 遥测或智能代理采样。
			req.Body = nil
		}
	}
	if o := p.currentObserver(); o != nil {
		o(req, dec, requestID)
	}
	return dec
}

// ServeHTTP 走 Inspect + Gate；只有闸且拦截姿态才能 403。
func (p *ReleaseProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, span := observability.Start(r.Context(), "edgecore.ReleaseProxy")
	defer span.End()
	r = r.WithContext(ctx)

	n := p.inflight.Add(1)
	defer p.inflight.Add(-1)
	if n > int64(kernel.EdgeInFlight) {
		// 过载先丢旁路；观察壳不得 503。
		p.obsMu.RLock()
		modelQueue := p.modelQueue
		p.obsMu.RUnlock()
		if modelQueue != nil {
			modelQueue.DropOldest()
		}
		code, _ := HTTPStatus(StatusInput{Posture: p.set.Posture(), InFlightExceeded: true})
		if Observes(p.set.Posture()) {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = io.WriteString(w, `{"error":"too many in-flight requests"}`+"\n")
		return
	}

	body, oversize, total := readInspectionBody(r)
	headers := flattenHeaders(r.Header)
	if r.Host != "" {
		headers["Host"] = r.Host
	}
	req := Request{AssetID: p.assetID, UnitID: p.unitID, Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Headers: headers, Body: body}
	req.ClientAddress = p.source.Resolve(r.RemoteAddr, r.Header)
	requestID, err := NewRequestID()
	if err != nil {
		code, _ := HTTPStatus(StatusInput{Posture: p.set.Posture(), MissingRequestID: true})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = io.WriteString(w, `{"error":"request id unavailable"}`+"\n")
		return
	}
	w.Header().Set("X-Request-Id", string(requestID))
	started := time.Now()
	hdr := r.Header.Clone()
	if r.Host != "" {
		hdr.Set("Host", r.Host)
	}
	view := CanonicalizeHTTP(req.Method, req.Path, req.Query, hdr, body, p.set.profileOrDefault())
	if oversize {
		MarkBodyPartial(&view, int64(len(body)), total)
	}

	engineCrash := false
	dec := p.DecideRequest(r.Context(), req, string(requestID), view)
	if dec.GenerationID != "" {
		w.Header().Set("X-Yufeng-Generation-Id", dec.GenerationID)
		w.Header().Set("X-Yufeng-Generation-Seq", strconv.FormatInt(dec.GenerationSeq, 10))
	}
	if coverageHasError(dec.Inspection.Coverage) && len(dec.Detections) == 0 && !view.Rejected && !oversize {
		engineCrash = true
	}
	code, would := HTTPStatus(StatusInput{
		Posture: p.set.Posture(), GateAction: dec.Action, View: view,
		Oversize: oversize, BodyPresent: r.ContentLength > 0 || len(body) > 0,
		EngineCrash: engineCrash,
	})
	if would {
		dec.WouldHaveBlocked = true
	}
	if Observes(p.set.Posture()) {
		dec.Action = ActionAllow
		code = http.StatusOK
	}

	elapsed := uint64(time.Since(started).Microseconds())
	if elapsed == 0 {
		elapsed = 1
	}
	for _, o := range dec.Observations {
		p.set.RecordLatency(o.ReleaseID, elapsed)
	}
	if p.telemetry != nil {
		_ = p.telemetry.Record(req, Result{Verdicts: dec.Verdicts}, dec.Action)
	}
	if code != http.StatusOK {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		if code == http.StatusForbidden {
			_, _ = io.WriteString(w, `{"blocked":true,"requestId":"`+string(requestID)+`"}`+"\n")
			return
		}
		_, _ = io.WriteString(w, `{"error":"inspection incomplete"}`+"\n")
		return
	}
	if Observes(p.set.Posture()) {
		w.WriteHeader(http.StatusOK)
		return
	}
	if p.rp == nil {
		w.WriteHeader(http.StatusOK)
		return
	}
	p.rp.ServeHTTP(w, r)
}
