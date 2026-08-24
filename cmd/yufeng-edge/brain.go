package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	artifactv1 "yufeng/proto/gen/artifactv1"
	assetv1 "yufeng/proto/gen/assetv1"
	eventv1 "yufeng/proto/gen/eventv1"
	registryv1 "yufeng/proto/gen/registryv1"

	"yufeng/lib/edgeclient"
	"yufeng/lib/edgecore"
	"yufeng/lib/kernel"
)

// releasePollInterval 与 uploadScanInterval 是中台连接模式两个后台循环的周期。
const (
	releasePollInterval = 2 * time.Second
	uploadScanInterval  = time.Second
)

// runBrainMode 以中台下发模式运行 edge：注册、拉取监听计划与发布、心跳计数、遥测上行。
// bootstrapToken 是部署级引导令牌：首次注册凭它放行，注册后凭会话令牌续用。
func runBrainMode(ctx context.Context, brainURL, adminAddr, unitID, unitVersion, bootstrapToken, dataDir, spoolDir string, pub ed25519.PublicKey, source edgecore.SourcePseudonymizer, hc *http.Client, modelSender modelTrafficSender) error {
	if dataDir == "" {
		dataDir = ".tmp"
	}
	if spoolDir == "" {
		spoolDir = filepath.Join(dataDir, "spool")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	set := edgecore.NewReleaseSet(unitVersion)
	cachePath := filepath.Join(dataDir, "generation-cache-"+unitID+".json")
	listenCachePath := filepath.Join(dataDir, "listen-plan-cache-"+unitID+".json")
	sessionPath := filepath.Join(dataDir, "session-"+unitID+".json")
	cachedGeneration := loadGenerationCache(cachePath, set, pub)
	cachedListenPlan := loadListenPlanCache(listenCachePath, set, pub, unitID)

	client := edgeclient.New(brainURL, hc)
	client.BootstrapToken = bootstrapToken
	var reg *edgeclient.Session
	prevSession := loadSession(sessionPath)
	var sessErr error
	if prevSession != nil && prevSession.Refresh != "" {
		if s, rerr := client.Refresh(ctx, prevSession.UnitID, prevSession.Refresh); rerr == nil {
			s.AssetID = prevSession.AssetID
			reg = s
		} else {
			sessErr = rerr
		}
	}
	if reg == nil && (prevSession == nil || prevSession.Refresh == "") {
		reg, sessErr = client.Register(ctx, &registryv1.RegisterRequest{
			UnitId: unitID, Kind: registryv1.UnitKind_UNIT_KIND_EDGE, Version: unitVersion,
			ContractVersion: "v1", Asset: assetFor(unitID), PubkeyHint: pubHint(pub), Capabilities: edgecore.ProducerCapabilities(),
		})
	}
	if err := decideOfflineStart(cachedGeneration, cachedListenPlan, sessErr); err != nil {
		return err
	}
	if sessErr != nil || reg == nil {
		log.Printf("中台不可达，使用本地已验证发布缓存继续服务: %v", sessErr)
		offlineAsset := unitID
		if prev := loadSession(sessionPath); prev != nil && prev.AssetID != "" {
			offlineAsset = prev.AssetID
		}
		return serveOffline(ctx, adminAddr, set, offlineAsset, client, sessionPath, cachePath, listenCachePath, pub, spoolDir, unitID, unitVersion, source, modelSender)
	}
	if err := saveSession(sessionPath, reg); err != nil {
		return fmt.Errorf("persist registered session: %w", err)
	}
	log.Printf("已注册中台：unit=%s asset=%s", reg.UnitID, reg.AssetID)
	if err := catchUpGenerations(ctx, client, reg, set, pub, cachePath); err != nil {
		if !cachedGeneration {
			return err
		}
		log.Printf("世代追赶失败: %v", err)
	}
	if err := waitForListenPlan(ctx, client, reg, set, pub, listenCachePath); err != nil {
		return fmt.Errorf("listen plan catch-up: %w", err)
	}
	log.Printf("初始世代装载完成：seq=%d", set.CurrentGenerationSeq())

	spool, err := edgeclient.NewSpool(filepath.Join(spoolDir, "edge-spool-"+unitID))
	if err != nil {
		return err
	}
	reviewVault, reviewSpool, err := openTrafficReview(dataDir, spoolDir, unitID)
	if err != nil {
		return fmt.Errorf("open traffic review storage: %w", err)
	}
	runtime, err := newEdgeRuntime(set, reg.UnitID, reg.AssetID, spool, source, modelSender)
	if err != nil {
		return err
	}
	runtime.enableTrafficReview(reviewVault, reviewSpool)
	runtime.startBackground(ctx)
	plan := runtime.plan()
	serveErr := make(chan error, 3)
	go func() { serveErr <- listenEdge(plan.GetListenAddress(), runtime) }()
	go func() { serveErr <- listenAdmin(adminAddr, runtime) }()

	go releaseLoop(ctx, client, reg, set, pub, cachePath, sessionPath)
	go listenPlanLoop(ctx, client, reg, set, runtime, pub, listenCachePath, sessionPath, serveErr)
	go heartbeatLoop(ctx, client, reg, set, runtime, sessionPath, unitVersion)
	go uploadLoop(ctx, client, reg, spool, sessionPath)
	go reviewDrainLoop(ctx, runtime)
	go reviewUploadLoop(ctx, client, reg, reviewSpool)
	if reviewVault != nil {
		go evidenceRequestLoop(ctx, client, reg, reviewVault)
		go evidenceVaultCleanupLoop(ctx, reviewVault)
	}

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
		return nil
	}
}

func releaseLoop(ctx context.Context, client *edgeclient.Client, reg *edgeclient.Session, set *edgecore.ReleaseSet, pub ed25519.PublicKey, cachePath, sessionPath string) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(releasePollInterval):
		}
		if err := client.EnsureAccess(ctx, reg); err != nil {
			log.Printf("刷新访问令牌失败: %v", err)
			continue
		}
		if err := saveSession(sessionPath, reg); err != nil {
			log.Printf("会话持久化失败: %v", err)
		}
		if err := catchUpGenerations(ctx, client, reg, set, pub, cachePath); err != nil {
			log.Printf("世代追赶失败: %v", err)
		}
	}
}

func catchUpGenerations(ctx context.Context, client *edgeclient.Client, sess *edgeclient.Session, set *edgecore.ReleaseSet, pub ed25519.PublicKey, cachePath string) error {
	if sess == nil || sess.AssetID == "" {
		return nil
	}
	gens, err := client.ListGenerations(ctx, sess, sess.AssetID, set.CurrentGenerationSeq())
	if err != nil {
		return err
	}
	for _, g := range gens {
		if err := set.ApplyGeneration(g, pub, func(previous, current *artifactv1.AssetGeneration) error {
			return saveGenerationCache(cachePath, previous, current)
		}); err != nil {
			return err
		}
	}
	return nil
}

func shouldPersistObservation(dec edgecore.Decision) bool {
	if len(dec.Detections) > 0 || dec.WouldHaveBlocked || dec.Action != edgecore.ActionAllow {
		return true
	}
	return sampleNoDetection()
}

func heartbeatLoop(ctx context.Context, client *edgeclient.Client, reg *edgeclient.Session, set *edgecore.ReleaseSet, runtime *edgeRuntime, sessionPath, unitVersion string) {
	// generation 用启动时间戳即可满足"同单元内单调"：中台只靠它区分
	// 重启纪元，不要求跨重启连续。
	generation := uint64(time.Now().UnixNano())
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(reg.Snapshot().HeartbeatInterval):
		}
		counters := set.Counters()
		reqs, routes := runtime.windowSnapshot()
		plan := runtime.plan()
		if plan == nil {
			continue
		}
		currentGeneration := set.CurrentGeneration()
		req := &registryv1.HeartbeatRequest{
			UnitId: reg.UnitID, Generation: generation,
			Posture: plan.GetPosture(), TrafficKey: plan.GetTrafficKey(), WindowRequests: reqs, RouteTemplates: routes,
			Capabilities: edgecore.ProducerCapabilities(), ProducerHealth: runtime.producerHealth(), Version: unitVersion,
			CurrentListenPlanVersion: plan.GetVersion(),
		}
		if currentGeneration != nil {
			req.CurrentGenerationId = currentGeneration.GetGenerationId()
			req.CurrentGenerationSeq = currentGeneration.GetGenerationSeq()
		}
		for _, c := range counters {
			req.ReleaseCounters = append(req.ReleaseCounters, &registryv1.ReleaseCounter{
				ReleaseId: c.ReleaseID, ArtifactId: c.ArtifactID, Mode: c.Mode,
				RequestsTotal: c.RequestsTotal, BlocksTotal: c.BlocksTotal,
				ObserveTotal: c.ObserveTotal, CanarySelectedTotal: c.CanarySelectedTotal,
				LatencyP99Micros: c.P99Micros,
			})
		}
		if err := client.EnsureAccess(ctx, reg); err != nil {
			log.Printf("刷新访问令牌失败: %v", err)
			continue
		}
		if err := saveSession(sessionPath, reg); err != nil {
			log.Printf("会话持久化失败: %v", err)
		}
		if _, err := client.Heartbeat(ctx, reg, req); err != nil {
			log.Printf("心跳失败: %v", err)
		}
	}
}

func uploadLoop(ctx context.Context, client *edgeclient.Client, reg *edgeclient.Session, spool *edgeclient.Spool, sessionPath string) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(uploadScanInterval):
		}
		files, err := spool.Files()
		if err != nil {
			continue
		}
		for _, file := range files {
			events, err := spool.ReadEvents(file)
			if err != nil {
				// 半写分段无法解析：隔离保留证据，同时不让它卡死队列。
				if qErr := spool.Quarantine(file); qErr != nil {
					log.Printf("隔离损坏分段 %s 失败: %v", filepath.Base(file), qErr)
				} else {
					log.Printf("分段 %s 无法解析，已隔离: %v", filepath.Base(file), err)
				}
				continue
			}
			if len(events) == 0 {
				// 空分段没有内容可丢，直接清掉。
				if err := spool.Remove(file); err != nil {
					log.Printf("删除空分段 %s 失败: %v", filepath.Base(file), err)
				}
				continue
			}
			if err := client.EnsureAccess(ctx, reg); err != nil {
				log.Printf("刷新访问令牌失败: %v", err)
				continue
			}
			if err := saveSession(sessionPath, reg); err != nil {
				log.Printf("会话持久化失败: %v", err)
			}
			if err := uploadEventBatches(ctx, client, reg, spool, file, events); err != nil {
				log.Printf("遥测上传失败（保留 %s）: %v", filepath.Base(file), err)
			}
		}
	}
}

// saveGenerationCache 原子持久化上一份与当前完整签名世代。
// 持久化是激活新世代的前置条件，失败时调用方必须继续使用当前世代。
func saveGenerationCache(path string, previous, current *artifactv1.AssetGeneration) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	var payload bytes.Buffer
	for _, gen := range []*artifactv1.AssetGeneration{previous, current} {
		if gen == nil || gen.GetGenerationId() == "" {
			continue
		}
		raw, err := protojson.Marshal(gen)
		if err != nil {
			return err
		}
		if int64(payload.Len()+len(raw)+1) > kernel.EdgeCacheDiskBytes {
			return fmt.Errorf("generation cache exceeds %d bytes", kernel.EdgeCacheDiskBytes)
		}
		payload.Write(raw)
		payload.WriteByte('\n')
	}
	return atomicWritePrivate(path, ".generation-cache-*", payload.Bytes())
}

// loadGenerationCache 逐份验签、编译并装载本地完整世代。
// 当前缓存损坏时保留前一份已验证世代；缓存从不作为信任来源。
func loadGenerationCache(path string, set *edgecore.ReleaseSet, pub ed25519.PublicKey) bool {
	info, err := os.Stat(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("世代缓存读取失败（按空集启动）: %v", err)
		}
		return false
	}
	if info.Size() > kernel.EdgeCacheDiskBytes {
		log.Printf("世代缓存超过上限（按空集启动）: %d", info.Size())
		return false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		log.Printf("世代缓存读取失败（按空集启动）: %v", err)
		return false
	}
	loaded := false
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var gen artifactv1.AssetGeneration
		if err := protojson.Unmarshal(line, &gen); err != nil {
			log.Printf("世代缓存解析失败（保留已装载上一份）: %v", err)
			continue
		}
		if err := set.ApplyGeneration(&gen, pub); err != nil {
			log.Printf("世代缓存装载失败（保留已装载上一份）: %v", err)
			continue
		}
		loaded = true
	}
	if loaded {
		log.Printf("从本地缓存装载世代：seq=%d", set.CurrentGenerationSeq())
	}
	return loaded
}

func listenEdge(addr string, h http.Handler) error {
	return kernel.NewProductionHTTPServer(addr, h).ListenAndServe()
}

func uploadEventBatches(ctx context.Context, client *edgeclient.Client, reg *edgeclient.Session, spool *edgeclient.Spool, file string, events []*eventv1.Event) error {
	max := kernel.UploadBatchMax
	if max <= 0 {
		max = 100
	}
	if len(events) <= max {
		resp, err := client.UploadEvents(ctx, reg, events)
		return edgeclient.ApplyUploadAck(spool, file, events, resp, err)
	}
	var retry, permanent []*eventv1.Event
	for i := 0; i < len(events); i += max {
		end := i + max
		if end > len(events) {
			end = len(events)
		}
		chunk := events[i:end]
		resp, err := client.UploadEvents(ctx, reg, chunk)
		if err != nil {
			return err
		}
		batchRetry, batchPermanent := edgeclient.PartitionUploadAck(chunk, resp)
		retry = append(retry, batchRetry...)
		permanent = append(permanent, batchPermanent...)
	}
	return spool.ResolveUpload(file, retry, permanent)
}

func serveOffline(ctx context.Context, adminAddr string, set *edgecore.ReleaseSet, assetID string, client *edgeclient.Client, sessionPath, cachePath, listenCachePath string, pub ed25519.PublicKey, spoolDir, unitID, unitVersion string, source edgecore.SourcePseudonymizer, modelSender modelTrafficSender) error {
	var spool *edgeclient.Spool
	if spoolDir != "" {
		s, err := edgeclient.NewSpool(filepath.Join(spoolDir, "edge-spool-"+unitID))
		if err != nil {
			return fmt.Errorf("open offline telemetry spool: %w", err)
		}
		spool = s
	}
	runtime, err := newEdgeRuntime(set, unitID, assetID, spool, source, modelSender)
	if err != nil {
		return err
	}
	reviewVault, reviewSpool, err := openTrafficReview(filepath.Dir(sessionPath), spoolDir, unitID)
	if err != nil {
		return fmt.Errorf("open offline traffic review storage: %w", err)
	}
	runtime.enableTrafficReview(reviewVault, reviewSpool)
	runtime.startBackground(ctx)
	plan := runtime.plan()
	serveErr := make(chan error, 3)
	go func() { serveErr <- listenEdge(plan.GetListenAddress(), runtime) }()
	go func() { serveErr <- listenAdmin(adminAddr, runtime) }()
	go reviewDrainLoop(ctx, runtime)
	if reviewVault != nil {
		go evidenceVaultCleanupLoop(ctx, reviewVault)
	}
	go reconnectLoop(ctx, client, sessionPath, cachePath, listenCachePath, set, pub, runtime, spool, unitID, unitVersion, assetID, serveErr)
	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
		return nil
	}
}

func reconnectLoop(ctx context.Context, client *edgeclient.Client, sessionPath, cachePath, listenCachePath string, set *edgecore.ReleaseSet, pub ed25519.PublicKey, runtime *edgeRuntime, spool *edgeclient.Spool, unitID, unitVersion, assetID string, failures chan<- error) {
	if client == nil {
		<-ctx.Done()
		return
	}
	backoff := time.Second
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
		var reg *edgeclient.Session
		prev := loadSession(sessionPath)
		if prev != nil && prev.Refresh != "" {
			if s, err := client.Refresh(ctx, prev.UnitID, prev.Refresh); err == nil {
				s.AssetID = prev.AssetID
				if s.AssetID == "" {
					s.AssetID = assetID
				}
				reg = s
			} else {
				log.Printf("离线恢复刷新失败（不回退引导令牌）: %v", err)
				continue
			}
		}
		if reg == nil && (prev == nil || prev.Refresh == "") {
			s, err := client.Register(ctx, &registryv1.RegisterRequest{
				UnitId: unitID, Kind: registryv1.UnitKind_UNIT_KIND_EDGE, Version: unitVersion,
				ContractVersion: "v1", Asset: assetFor(unitID), PubkeyHint: pubHint(pub), Capabilities: edgecore.ProducerCapabilities(),
			})
			if err != nil {
				log.Printf("离线恢复注册失败: %v", err)
				continue
			}
			reg = s
		}
		if err := saveSession(sessionPath, reg); err != nil {
			log.Printf("离线恢复会话持久化失败: %v", err)
		}
		log.Printf("中台已恢复：unit=%s asset=%s", reg.UnitID, reg.AssetID)
		if err := catchUpGenerations(ctx, client, reg, set, pub, cachePath); err != nil {
			log.Printf("恢复后世代追赶失败: %v", err)
		}
		if err := catchUpListenPlans(ctx, client, reg, set, runtime, pub, listenCachePath); err != nil {
			if errors.Is(err, errListenAddressChanged) {
				failures <- err
				return
			}
			log.Printf("恢复后监听计划追赶失败: %v", err)
		}
		go releaseLoop(ctx, client, reg, set, pub, cachePath, sessionPath)
		go listenPlanLoop(ctx, client, reg, set, runtime, pub, listenCachePath, sessionPath, failures)
		go heartbeatLoop(ctx, client, reg, set, runtime, sessionPath, unitVersion)
		if spool != nil {
			go uploadLoop(ctx, client, reg, spool, sessionPath)
		}
		if runtime.reviewSpool != nil {
			go reviewUploadLoop(ctx, client, reg, runtime.reviewSpool)
		}
		if runtime.reviewVault != nil {
			go evidenceRequestLoop(ctx, client, reg, runtime.reviewVault)
		}
		return
	}
}

func assetFor(unitID string) *assetv1.Asset {
	return &assetv1.Asset{Id: unitID, DisplayName: unitID}
}

func pubHint(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:])
}

func observationEvent(unitID, assetID, requestID string, req edgecore.Request, dec edgecore.Decision, source edgecore.SourcePseudonymizer) *eventv1.Event {
	return edgecore.TrafficEvent(unitID, assetID, requestID, req, dec, source)
}

func sampleNoDetection() bool {
	return rand.Float64() < kernel.NoDetectionSampleRate
}
