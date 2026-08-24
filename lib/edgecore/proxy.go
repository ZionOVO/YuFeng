package edgecore

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync/atomic"

	commonv1 "yufeng/proto/gen/commonv1"

	"yufeng/lib/kernel"
)

// Proxy 是数据面的独立反向代理形态：请求先经过检查与裁决，
// 拦截时返回禁止访问，放行时转发上游。外部授权形态由 ExternalAuthorizationHandler 承载。
type Proxy struct {
	engine    *Engine
	mode      commonv1.ReleaseMode
	telemetry *Telemetry
	rp        *httputil.ReverseProxy
	// assetID 是本代理保护的资产标识，进遥测；正式来源是注册时中台分配。
	assetID  string
	posture  commonv1.IngressPosture
	inflight atomic.Int64
}

// NewProxy 构造代理。upstream 为转发目标，assetID 进遥测的 asset_id 字段。
func NewProxy(engine *Engine, mode commonv1.ReleaseMode, tel *Telemetry, upstream *url.URL, assetID string) *Proxy {
	rp := &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) {
		r.SetURL(upstream)
		r.Out.Host = upstream.Host
		r.SetXForwarded()
	},
	}
	return &Proxy{engine: engine, mode: mode, telemetry: tel, rp: rp, assetID: assetID, posture: commonv1.IngressPosture_INGRESS_POSTURE_REVERSE_PROXY}
}

// SetPosture 设置入口姿态。
func (p *Proxy) SetPosture(posture commonv1.IngressPosture) {
	p.posture = ResolvePosture(posture)
}

// ServeHTTP 执行请求体边界检查、同步检查、签名策略裁决、遥测记录与上游转发。
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	n := p.inflight.Add(1)
	defer p.inflight.Add(-1)
	if n > int64(kernel.EdgeInFlight) {
		code, _ := HTTPStatus(StatusInput{Posture: p.posture, InFlightExceeded: true})
		if Observes(p.posture) {
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
	req := Request{
		AssetID: p.assetID,
		Method:  r.Method,
		Path:    r.URL.Path,
		Query:   r.URL.RawQuery,
		Headers: headers,
		Body:    body,
	}
	req.ClientAddress, _ = parsePeerAddress(r.RemoteAddr)
	view := Canonicalize(req.Method, req.Path, req.Query, headers, body, DefaultInspectionProfile())
	if oversize {
		MarkBodyPartial(&view, int64(len(body)), total)
	}
	v, matched := p.engine.MatchRules(req)
	res := Result{}
	if matched {
		res.Verdicts = []Verdict{v}
	}
	action := ActionAllow
	if matched {
		action = actionForMode(p.mode, true, true)
	}
	if Observes(p.posture) && action == ActionBlock {
		action = ActionAllow
	}
	code, _ := HTTPStatus(StatusInput{
		Posture: p.posture, GateAction: action, View: view,
		Oversize: oversize, BodyPresent: r.ContentLength > 0 || len(body) > 0,
	})

	if p.telemetry != nil {
		_ = p.telemetry.Record(req, res, action)
	}

	if code != http.StatusOK {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		if code == http.StatusForbidden {
			_, _ = io.WriteString(w, `{"blocked":true,"reason":"matched detection rule"}`+"\n")
			return
		}
		_, _ = io.WriteString(w, `{"error":"inspection incomplete"}`+"\n")
		return
	}
	if Observes(p.posture) {
		w.WriteHeader(http.StatusOK)
		return
	}
	p.rp.ServeHTTP(w, r)
}

// readInspectionBody 读进引擎体上限并回填，同时报告是否超体。
func readInspectionBody(r *http.Request) (body []byte, oversize bool, total int64) {
	if r.Body == nil || r.Body == http.NoBody {
		return nil, false, 0
	}
	limit := kernel.EngineBodyLimitBytes
	if r.ContentLength > int64(limit) {
		oversize = true
		total = r.ContentLength
	}
	buf, _ := io.ReadAll(io.LimitReader(r.Body, int64(limit+1)))
	n := len(buf)
	r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(buf), r.Body))
	if n > limit {
		oversize = true
		if total == 0 {
			total = int64(n)
		}
		buf = buf[:limit]
	} else if total == 0 {
		total = int64(n)
	}
	return buf, oversize, total
}

func flattenHeaders(h http.Header) map[string]string {
	m := make(map[string]string, len(h))
	for k, v := range h {
		if len(v) > 0 {
			m[k] = v[0]
		}
	}
	return m
}
