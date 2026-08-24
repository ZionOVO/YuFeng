package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"yufeng/lib/dataplane"
	"yufeng/lib/edgeclient"
	"yufeng/lib/edgecore"
	"yufeng/lib/kernel"
	"yufeng/lib/observability"
	artifactv1 "yufeng/proto/gen/artifactv1"
	commonv1 "yufeng/proto/gen/commonv1"
	telemetryv1 "yufeng/proto/gen/telemetryv1"
	unitv1 "yufeng/proto/gen/unitv1"
)

var errListenAddressChanged = errors.New("listen address changed; restart edge to rebind")

type edgeBinding struct {
	handler http.Handler
	proxy   *edgecore.ReleaseProxy
	plan    *artifactv1.UnitListenPlan
}

type modelTrafficSender interface {
	SubmitTraffic(context.Context, *edgecore.ModelIngressItem) error
}

type edgeObservation struct {
	request    edgecore.Request
	decision   edgecore.Decision
	requestID  string
	trafficKey string
}

type edgeRuntime struct {
	set             *edgecore.ReleaseSet
	unitID          string
	assetID         string
	spool           *edgeclient.Spool
	source          edgecore.SourcePseudonymizer
	reviewMu        sync.Mutex
	reviewVault     *edgecore.EvidenceVault
	reviewSpool     *edgeclient.ReviewSpool
	reviewCollector *edgecore.ReviewCollector
	reviewSeq       int64
	modelQueue      *edgecore.ModelIngressQueue
	modelSender     modelTrafficSender
	observations    chan edgeObservation
	droppedObserve  atomic.Uint64
	current         atomic.Pointer[edgeBinding]
}

// newEdgeRuntime 只在已经装载验签监听计划时构造业务运行时。
func newEdgeRuntime(set *edgecore.ReleaseSet, unitID, assetID string, spool *edgeclient.Spool, source edgecore.SourcePseudonymizer, senders ...modelTrafficSender) (*edgeRuntime, error) {
	r := &edgeRuntime{
		set: set, unitID: unitID, assetID: assetID, spool: spool, source: source,
		observations: make(chan edgeObservation, kernel.EdgeObservationQueueMax),
	}
	if len(senders) > 0 && senders[0] != nil {
		r.modelSender = senders[0]
		r.modelQueue = edgecore.NewModelIngressQueue()
	}
	plan := set.CurrentListenPlan()
	if plan == nil {
		return nil, errors.New("verified listen plan is required")
	}
	binding, err := r.buildBinding(plan)
	if err != nil {
		return nil, err
	}
	r.current.Store(binding)
	return r, nil
}

func (r *edgeRuntime) enableTrafficReview(vault *edgecore.EvidenceVault, spool *edgeclient.ReviewSpool) {
	r.reviewMu.Lock()
	r.reviewVault = vault
	r.reviewSpool = spool
	r.reviewMu.Unlock()
}

// ServeHTTP 把业务请求交给当前监听计划绑定的处理器；未就绪时失败关闭。
func (r *edgeRuntime) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	binding := r.current.Load()
	if binding == nil || binding.handler == nil {
		http.Error(w, "edge is not ready", http.StatusServiceUnavailable)
		return
	}
	binding.handler.ServeHTTP(w, req)
}

// buildBinding 从验签监听计划装配入口姿态、上游、来源解析与遥测处理链。
func (r *edgeRuntime) buildBinding(plan *artifactv1.UnitListenPlan) (*edgeBinding, error) {
	if err := edgecore.ValidateUnitListenPlan(plan); err != nil {
		return nil, err
	}
	upstream := &url.URL{Scheme: "http", Host: "127.0.0.1"}
	if plan.GetPosture() == commonv1.IngressPosture_INGRESS_POSTURE_REVERSE_PROXY {
		var err error
		upstream, err = url.Parse(plan.GetUpstreamUrl())
		if err != nil {
			return nil, err
		}
	}
	proxy := edgecore.NewReleaseProxy(r.set, nil, upstream, r.assetID)
	resolver, err := edgecore.NewClientSourceResolver(plan.GetClientSource().GetTrustedProxyCidrs())
	if err != nil {
		return nil, err
	}
	proxy.SetClientSourceResolver(resolver)
	proxy.SetUnitID(r.unitID)
	proxy.SetEvidence(edgecore.NewEvidenceRing())
	if r.modelQueue != nil {
		proxy.SetModelIngress(r.modelQueue)
	}
	trafficKey := plan.GetTrafficKey()
	proxy.SetObserver(func(req edgecore.Request, dec edgecore.Decision, requestID string) {
		observation := edgeObservation{request: req, decision: dec, requestID: requestID, trafficKey: trafficKey}
		select {
		case r.observations <- observation:
		default:
			r.droppedObserve.Add(1)
		}
	})
	var handler http.Handler = proxy
	if plan.GetPosture() == commonv1.IngressPosture_INGRESS_POSTURE_EXT_AUTHZ {
		ext := edgecore.NewExtAuthz(r.assetID, func(view edgecore.CanonicalView, req edgecore.Request) edgecore.Action {
			id, err := edgecore.NewRequestID()
			if err != nil {
				return edgecore.ActionAllow
			}
			return proxy.DecideRequest(context.Background(), req, string(id), view).Action
		})
		ext.SetUnitID(r.unitID)
		ext.SetClientSourceResolver(resolver)
		handler = ext
	}
	return &edgeBinding{handler: handler, proxy: proxy, plan: proto.Clone(plan).(*artifactv1.UnitListenPlan)}, nil
}

// startBackground 启动遥测落盘和模型发送壳；请求路径只做非阻塞入队。
func (r *edgeRuntime) startBackground(ctx context.Context) {
	go r.observationLoop(ctx)
	if r.modelQueue == nil || r.modelSender == nil {
		return
	}
	for range kernel.ModelSideIngressWorkers {
		go r.modelIngressLoop(ctx)
	}
}

func (r *edgeRuntime) observationLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case observation := <-r.observations:
			if r.observeTrafficReview(observation.request, observation.decision, observation.requestID) {
				continue
			}
			if r.spool == nil || !shouldPersistObservation(observation.decision) {
				continue
			}
			event := observationEvent(r.unitID, r.assetID, observation.requestID, observation.request, observation.decision, r.source)
			event.TrafficKey = observation.trafficKey
			if err := r.spool.Append(event); err != nil {
				log.Printf("遥测落盘失败: %v", err)
			}
		}
	}
}

func (r *edgeRuntime) modelIngressLoop(ctx context.Context) {
	for {
		item, ok := r.modelQueue.Take(ctx)
		if !ok {
			return
		}
		if err := r.modelSender.SubmitTraffic(ctx, item); err != nil {
			r.modelQueue.MarkDropped()
		}
	}
}

func (r *edgeRuntime) observeTrafficReview(req edgecore.Request, dec edgecore.Decision, requestID string) bool {
	r.reviewMu.Lock()
	defer r.reviewMu.Unlock()
	policy := r.set.TrafficReviewPolicy()
	if policy == nil || r.reviewSpool == nil {
		return false
	}
	if policy.GetMode() == artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_UNSPECIFIED || policy.GetMode() == artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_OFF {
		return false
	}
	if r.reviewCollector == nil || r.reviewSeq != dec.GenerationSeq {
		if r.reviewCollector != nil {
			windows, candidates, version := r.reviewCollector.PrepareFlush()
			if err := r.persistReviewLocked(windows, candidates); err != nil {
				log.Printf("流量审查世代切换快照落盘失败: %v", err)
				return true
			}
			r.reviewCollector.CommitDrain(version)
		}
		vault := r.reviewVault
		if policy.GetMode() >= artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_EVIDENCE_ON_APPROVAL {
			if vault == nil {
				log.Printf("流量审查证据库不可用")
			} else if err := vault.Configure(policy); err != nil {
				log.Printf("流量审查策略拒绝: %v", err)
				vault = nil
			}
		}
		collector, err := edgecore.NewReviewCollector(policy, r.set.TrafficReviewPolicyDigest(), vault)
		if err != nil {
			log.Printf("流量审查收集器创建失败: %v", err)
			return true
		}
		r.reviewCollector = collector
		r.reviewSeq = dec.GenerationSeq
	}
	r.reviewCollector.Observe(time.Now(), r.unitID, r.assetID, requestID, req, dec)
	return true
}

func (r *edgeRuntime) drainTrafficReview(now time.Time) {
	r.reviewMu.Lock()
	defer r.reviewMu.Unlock()
	if r.reviewCollector == nil {
		return
	}
	windows, candidates, version := r.reviewCollector.PrepareDrain(now)
	if err := r.persistReviewLocked(windows, candidates); err != nil {
		log.Printf("流量审查快照落盘失败: %v", err)
		return
	}
	r.reviewCollector.CommitDrain(version)
}

func (r *edgeRuntime) persistReviewLocked(windows []*telemetryv1.TrafficWindow, candidates []*telemetryv1.ReviewCandidate) error {
	if r.reviewSpool == nil {
		return errors.New("traffic review spool is unavailable")
	}
	if err := r.reviewSpool.AppendWindows(windows); err != nil {
		return fmt.Errorf("persist traffic windows: %w", err)
	}
	if err := r.reviewSpool.AppendCandidates(candidates); err != nil {
		return fmt.Errorf("persist review candidates: %w", err)
	}
	return nil
}

func (r *edgeRuntime) producerHealth() *unitv1.ProducerHealth {
	health := &unitv1.ProducerHealth{HealthyProjectionVersions: append([]string(nil), edgecore.ProducerCapabilities().GetProjectionVersions()...)}
	if r.spool != nil {
		health.BufferedCriticalEvents, health.BufferedOrdinarySamples, health.DroppedCriticalEvents, health.DroppedOrdinarySamples = r.spool.ProductionStats()
	}
	if r.modelQueue != nil {
		health.DroppedLocalBypassItems = r.modelQueue.Dropped()
	}
	health.DroppedOrdinarySamples += r.droppedObserve.Load()
	return health
}

func (r *edgeRuntime) applyPlan(plan *artifactv1.UnitListenPlan, pub ed25519.PublicKey, cachePath string) error {
	if _, err := r.buildBinding(plan); err != nil {
		return err
	}
	previous := r.set.CurrentListenPlan()
	if err := r.set.ApplyListenPlan(plan, pub, r.unitID, func(old, current *artifactv1.UnitListenPlan) error {
		return saveListenPlanCache(cachePath, old, current)
	}); err != nil {
		return err
	}
	binding, err := r.buildBinding(plan)
	if err != nil {
		return err
	}
	r.current.Store(binding)
	if previous != nil && previous.GetListenAddress() != plan.GetListenAddress() {
		return errListenAddressChanged
	}
	return nil
}

func (r *edgeRuntime) plan() *artifactv1.UnitListenPlan {
	binding := r.current.Load()
	if binding == nil || binding.plan == nil {
		return nil
	}
	return proto.Clone(binding.plan).(*artifactv1.UnitListenPlan)
}

func (r *edgeRuntime) windowSnapshot() (uint64, []string) {
	binding := r.current.Load()
	if binding == nil || binding.proxy == nil {
		return 0, nil
	}
	return binding.proxy.WindowSnapshot()
}

func (r *edgeRuntime) readyState() dataplane.EdgeState {
	plan := r.plan()
	gen := r.set.CurrentGeneration()
	if plan == nil || gen == nil {
		return dataplane.EdgeState{}
	}
	return dataplane.EdgeState{
		Ready: true, UnitID: r.unitID, Posture: plan.GetPosture().String(), TrafficKey: plan.GetTrafficKey(),
		GenerationID: gen.GetGenerationId(), GenerationSeq: gen.GetGenerationSeq(), ListenVersion: plan.GetVersion(),
	}
}

func edgeAdminHandler(runtime *edgeRuntime) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/ready", func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		state := runtime.readyState()
		w.Header().Set("Content-Type", "application/json")
		if !state.Ready {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(state)
	})
	mux.Handle("/", observability.Handler(nil, "edge", "v1"))
	return mux
}

func listenAdmin(addr string, runtime *edgeRuntime) error {
	if addr == "" {
		return nil
	}
	srv := &http.Server{Addr: addr, Handler: edgeAdminHandler(runtime), ReadHeaderTimeout: kernel.HTTPReadHeaderTimeout}
	return srv.ListenAndServe()
}

func saveListenPlanCache(path string, previous, current *artifactv1.UnitListenPlan) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	var payload bytes.Buffer
	for _, plan := range []*artifactv1.UnitListenPlan{previous, current} {
		if plan == nil || plan.GetUnitId() == "" {
			continue
		}
		raw, err := protojson.Marshal(plan)
		if err != nil {
			return err
		}
		payload.Write(raw)
		payload.WriteByte('\n')
	}
	return atomicWritePrivate(path, ".listen-plan-cache-*", payload.Bytes())
}

func loadListenPlanCache(path string, set *edgecore.ReleaseSet, pub ed25519.PublicKey, unitID string) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	loaded := false
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var plan artifactv1.UnitListenPlan
		if err := protojson.Unmarshal(line, &plan); err != nil {
			continue
		}
		if err := set.ApplyListenPlan(&plan, pub, unitID); err != nil {
			continue
		}
		loaded = true
	}
	return loaded
}
