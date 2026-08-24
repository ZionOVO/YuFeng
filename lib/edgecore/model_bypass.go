package edgecore

import (
	"context"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"yufeng/lib/kernel"
	artifactv1 "yufeng/proto/gen/artifactv1"
	commonv1 "yufeng/proto/gen/commonv1"
	modelsidev1 "yufeng/proto/gen/modelsidev1"
	unitv1 "yufeng/proto/gen/unitv1"
)

// NormalizedModelTrafficSchema 是 Edge 与 ModelSide 的规范流量契约版本。
const NormalizedModelTrafficSchema = "normalized-http/v1"

// modelIngressRequestPathEvictionLimit 限制单次业务请求为窗口准入执行的旧项淘汰工作。
const modelIngressRequestPathEvictionLimit = 32

// ModelIngressItem 把流量与产生它的签名模型档案绑定，跨世代排队时不读取当前策略。
type ModelIngressItem struct {
	Profile *artifactv1.ModelProfile
	Traffic *modelsidev1.NormalizedTraffic

	entryBytes   uint64
	profileBytes uint64
	enqueuedAt   time.Time
}

type modelIngressProfileRef struct {
	queued   uint64
	inFlight uint64
	bytes    uint64
}

// ModelIngressBatch 是同一签名模型档案下的一次至多一次 ModelSide 提交。
type ModelIngressBatch struct {
	Profile       *artifactv1.ModelProfile
	ProfileDigest string
	Traffic       []*modelsidev1.NormalizedTraffic

	queue     *ModelIngressQueue
	items     []*ModelIngressItem
	completed atomic.Bool
}

// ModelIngressSnapshot 是 Edge 模型输入缓存窗口的无原文容量投影。
type ModelIngressSnapshot struct {
	Desired            *artifactv1.ModelIngressWindow
	Effective          *artifactv1.ModelIngressWindow
	State              unitv1.ModelIngressWindowState
	DegradationReasons []unitv1.ModelIngressDegradationReason
	QueuedItems        uint64
	QueuedBytes        uint64
	InFlightItems      uint64
	InFlightBytes      uint64
	OldestAge          time.Duration
	Drops              *unitv1.ModelIngressDropCounters
}

// Dropped 返回窗口生命周期内所有至多一次丢弃的总数。
func (s ModelIngressSnapshot) Dropped() uint64 {
	if s.Drops == nil {
		return 0
	}
	return s.Drops.GetEvictedOldest() + s.Drops.GetExpired() + s.Drops.GetItemTooLarge() +
		s.Drops.GetInFlightCapacity() + s.Drops.GetTransportFailed() + s.Drops.GetModelsideRejected() +
		s.Drops.GetAdmissionBudget()
}

// ModelIngressQueue 是 Edge 到 ModelSide 的条数、实际保留字节和排队年龄三重有界最新窗口。
type ModelIngressQueue struct {
	mu            sync.Mutex
	entries       []*ModelIngressItem
	head          int
	count         int
	queuedBytes   uint64
	inFlightItems uint64
	inFlightBytes uint64
	profileBytes  uint64
	profiles      map[string]*modelIngressProfileRef
	desired       *artifactv1.ModelIngressWindow
	effective     *artifactv1.ModelIngressWindow
	hardLimit     *artifactv1.ModelIngressWindow
	reasons       []unitv1.ModelIngressDegradationReason
	drops         unitv1.ModelIngressDropCounters
	dropped       atomic.Uint64
	notify        chan struct{}
	now           func() time.Time
}

// NewModelIngressQueue 按平台默认本机硬上限构造非阻塞模型输入窗口。
func NewModelIngressQueue() *ModelIngressQueue {
	queue, err := NewModelIngressQueueWithHardLimit(kernel.DefaultModelIngressHardLimit())
	if err != nil {
		panic(err)
	}
	return queue
}

// NewModelIngressQueueWithHardLimit 构造本机硬上限固定、中央期望值可重配的模型输入窗口。
func NewModelIngressQueueWithHardLimit(hardLimit *artifactv1.ModelIngressWindow) (*ModelIngressQueue, error) {
	normalized, err := kernel.NormalizeModelIngressWindow(hardLimit)
	if err != nil {
		return nil, err
	}
	queue := &ModelIngressQueue{
		entries:   make([]*ModelIngressItem, int(normalized.GetMaxItems())),
		profiles:  make(map[string]*modelIngressProfileRef),
		hardLimit: normalized,
		notify:    make(chan struct{}, 1),
		now:       time.Now,
	}
	if err := queue.Configure(kernel.DefaultModelIngressWindow()); err != nil {
		return nil, err
	}
	return queue, nil
}

// Configure 原子应用签名中央期望值；缩容只改变准入上限，不批量清空现有流量。
func (q *ModelIngressQueue) Configure(desired *artifactv1.ModelIngressWindow) error {
	if q == nil {
		return nil
	}
	normalized, err := kernel.NormalizeModelIngressWindow(desired)
	if err != nil {
		return err
	}
	q.mu.Lock()
	q.desired = normalized
	q.effective = &artifactv1.ModelIngressWindow{
		MaxItems:         min(normalized.GetMaxItems(), q.hardLimit.GetMaxItems()),
		MaxRetainedBytes: min(normalized.GetMaxRetainedBytes(), q.hardLimit.GetMaxRetainedBytes()),
		MaxQueueAge:      normalized.GetMaxQueueAge(),
	}
	if q.hardLimit.GetMaxQueueAge().AsDuration() < normalized.GetMaxQueueAge().AsDuration() {
		q.effective.MaxQueueAge = q.hardLimit.GetMaxQueueAge()
	}
	q.effective = proto.Clone(q.effective).(*artifactv1.ModelIngressWindow)
	q.reasons = q.reasons[:0]
	if normalized.GetMaxItems() > q.hardLimit.GetMaxItems() {
		q.reasons = append(q.reasons, unitv1.ModelIngressDegradationReason_MODEL_INGRESS_DEGRADATION_REASON_MAX_ITEMS)
	}
	if normalized.GetMaxRetainedBytes() > q.hardLimit.GetMaxRetainedBytes() {
		q.reasons = append(q.reasons, unitv1.ModelIngressDegradationReason_MODEL_INGRESS_DEGRADATION_REASON_MAX_RETAINED_BYTES)
	}
	if normalized.GetMaxQueueAge().AsDuration() > q.hardLimit.GetMaxQueueAge().AsDuration() {
		q.reasons = append(q.reasons, unitv1.ModelIngressDegradationReason_MODEL_INGRESS_DEGRADATION_REASON_MAX_QUEUE_AGE)
	}
	q.signalLocked()
	q.mu.Unlock()
	return nil
}

// Offer 转移一个规范流量正文的本地所有权；容量不足时淘汰最旧可排队项。
func (q *ModelIngressQueue) Offer(item *ModelIngressItem) bool {
	if q == nil || item == nil || item.Profile == nil || item.Traffic == nil {
		return false
	}
	digest := strings.TrimSpace(item.Traffic.GetModelProfileDigest())
	if digest == "" {
		return false
	}
	now := q.now()
	item.entryBytes = modelIngressTrafficRetainedBytes(item.Traffic)
	item.profileBytes = modelIngressProfileRetainedBytes(item.Profile)
	item.enqueuedAt = now
	q.mu.Lock()
	defer q.mu.Unlock()
	evicted := q.evictExpiredLocked(now, modelIngressRequestPathEvictionLimit)
	if item.entryBytes+item.profileBytes > q.effective.GetMaxRetainedBytes() {
		q.recordDropLocked(&q.drops.ItemTooLarge, 1)
		return false
	}
	for !q.fitsLocked(item, digest) && q.count > 0 && evicted < modelIngressRequestPathEvictionLimit {
		q.evictOldestLocked(&q.drops.EvictedOldest)
		evicted++
	}
	if !q.fitsLocked(item, digest) {
		if q.count == 0 || !q.exceedsEffectiveLocked() || !q.fitsWindowLocked(item, digest, q.hardLimit) {
			if q.count == 0 {
				q.recordDropLocked(&q.drops.InFlightCapacity, 1)
			} else {
				q.recordDropLocked(&q.drops.AdmissionBudget, 1)
			}
			return false
		}
	}
	tail := (q.head + q.count) % len(q.entries)
	q.entries[tail] = item
	q.count++
	q.queuedBytes += item.entryBytes
	ref := q.profiles[digest]
	if ref == nil {
		ref = &modelIngressProfileRef{bytes: item.profileBytes}
		q.profiles[digest] = ref
		q.profileBytes += item.profileBytes
	}
	ref.queued++
	q.signalLocked()
	return true
}

// TakeBatch 等待并租出同一模型档案的一批流量；租出项仍计入窗口在途预算。
func (q *ModelIngressQueue) TakeBatch(ctx context.Context, maxItems int, maxBytes uint64, maxWait time.Duration) (*ModelIngressBatch, bool) {
	if q == nil {
		return nil, false
	}
	if maxItems < 1 || maxBytes == 0 {
		return nil, false
	}
	var deadline time.Time
	for {
		now := q.now()
		q.mu.Lock()
		q.evictExpiredLocked(now, modelIngressRequestPathEvictionLimit)
		if q.headExpiredLocked(now) {
			q.mu.Unlock()
			runtime.Gosched()
			continue
		}
		if q.count > 0 {
			if maxWait <= 0 {
				batch := q.leaseBatchLocked(maxItems, maxBytes)
				q.mu.Unlock()
				return batch, batch != nil
			}
			if deadline.IsZero() {
				deadline = now.Add(maxWait)
			}
			if q.batchReadyLocked(maxItems, maxBytes) || !now.Before(deadline) {
				batch := q.leaseBatchLocked(maxItems, maxBytes)
				q.mu.Unlock()
				return batch, batch != nil
			}
		}
		q.mu.Unlock()

		var timer <-chan time.Time
		var stop func() bool
		if !deadline.IsZero() {
			remaining := deadline.Sub(now)
			if remaining <= 0 {
				continue
			}
			t := time.NewTimer(remaining)
			timer = t.C
			stop = t.Stop
		}
		select {
		case <-ctx.Done():
			if stop != nil {
				stop()
			}
			return nil, false
		case <-q.notify:
			if stop != nil {
				stop()
			}
		case <-timer:
		}
	}
}

// CompleteBatch 释放在途预算，并把传输失败或 ModelSide 拒绝计入至多一次丢弃。
func (q *ModelIngressQueue) CompleteBatch(batch *ModelIngressBatch, accepted uint32, transportFailed bool) {
	if q == nil || batch == nil || batch.queue != q || !batch.completed.CompareAndSwap(false, true) {
		return
	}
	q.mu.Lock()
	count := uint64(len(batch.items))
	if transportFailed {
		q.recordDropLocked(&q.drops.TransportFailed, count)
	} else if uint64(accepted) < count {
		q.recordDropLocked(&q.drops.ModelsideRejected, count-uint64(accepted))
	}
	for _, item := range batch.items {
		digest := item.Traffic.GetModelProfileDigest()
		q.inFlightItems--
		q.inFlightBytes -= item.entryBytes
		ref := q.profiles[digest]
		if ref != nil {
			ref.inFlight--
			q.removeProfileIfUnusedLocked(digest, ref)
		}
	}
	q.signalLocked()
	q.mu.Unlock()
}

// Take 保留单项消费者兼容入口；新发送壳应使用 TakeBatch 保持在途记账。
func (q *ModelIngressQueue) Take(ctx context.Context) (*ModelIngressItem, bool) {
	batch, ok := q.TakeBatch(ctx, 1, kernel.ModelIngressBatchMaxBytes, 0)
	if !ok {
		return nil, false
	}
	item := batch.items[0]
	q.CompleteBatch(batch, 1, false)
	return item, true
}

// DropOldest 在请求过载时释放最旧旁路项；同步 Gate 不受影响。
func (q *ModelIngressQueue) DropOldest() {
	if q == nil {
		return
	}
	q.mu.Lock()
	if q.count > 0 {
		q.evictOldestLocked(&q.drops.EvictedOldest)
	}
	q.mu.Unlock()
}

// Dropped 返回窗口生命周期内所有至多一次丢弃的模型旁路总数。
func (q *ModelIngressQueue) Dropped() uint64 {
	if q == nil {
		return 0
	}
	return q.dropped.Load()
}

// Depth 返回当前排队条目数，供心跳与容量证据读取。
func (q *ModelIngressQueue) Depth() int {
	if q == nil {
		return 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.count
}

// QueuedBodyBytes 返回当前排队项持有的实际保留字节，名称为旧监控调用方保留。
func (q *ModelIngressQueue) QueuedBodyBytes() int64 {
	if q == nil {
		return 0
	}
	snapshot := q.Snapshot()
	return int64(snapshot.QueuedBytes)
}

// MarkDropped 记录后台传输失败；失败项不回到请求路径，也不转向 Brain。
func (q *ModelIngressQueue) MarkDropped() {
	if q != nil {
		q.mu.Lock()
		q.recordDropLocked(&q.drops.TransportFailed, 1)
		q.mu.Unlock()
	}
}

// Snapshot 返回容量、收敛、降级和丢弃投影，不复制任何请求原文。
func (q *ModelIngressQueue) Snapshot() ModelIngressSnapshot {
	if q == nil {
		return ModelIngressSnapshot{State: unitv1.ModelIngressWindowState_MODEL_INGRESS_WINDOW_STATE_DISABLED}
	}
	now := q.now()
	q.cleanupExpired(now)
	q.mu.Lock()
	defer q.mu.Unlock()
	queuedBytes, inFlightBytes := q.partitionedBytesLocked()
	snapshot := ModelIngressSnapshot{
		Desired:            proto.Clone(q.desired).(*artifactv1.ModelIngressWindow),
		Effective:          proto.Clone(q.effective).(*artifactv1.ModelIngressWindow),
		State:              q.stateLocked(now),
		DegradationReasons: append([]unitv1.ModelIngressDegradationReason(nil), q.reasons...),
		QueuedItems:        uint64(q.count),
		QueuedBytes:        queuedBytes,
		InFlightItems:      q.inFlightItems,
		InFlightBytes:      inFlightBytes,
		Drops:              proto.Clone(&q.drops).(*unitv1.ModelIngressDropCounters),
	}
	if q.count > 0 {
		age := now.Sub(q.entries[q.head].enqueuedAt)
		if age > 0 {
			snapshot.OldestAge = age
		}
	}
	return snapshot
}

func (q *ModelIngressQueue) fitsLocked(item *ModelIngressItem, digest string) bool {
	return q.fitsWindowLocked(item, digest, q.effective)
}

func (q *ModelIngressQueue) fitsWindowLocked(item *ModelIngressItem, digest string, window *artifactv1.ModelIngressWindow) bool {
	if uint64(q.count)+q.inFlightItems+1 > uint64(window.GetMaxItems()) {
		return false
	}
	bytes := q.totalBytesLocked() + item.entryBytes
	if q.profiles[digest] == nil {
		bytes += item.profileBytes
	}
	return bytes <= window.GetMaxRetainedBytes()
}

func (q *ModelIngressQueue) exceedsEffectiveLocked() bool {
	return uint64(q.count)+q.inFlightItems > uint64(q.effective.GetMaxItems()) ||
		q.totalBytesLocked() > q.effective.GetMaxRetainedBytes()
}

func (q *ModelIngressQueue) evictExpiredLocked(now time.Time, limit int) int {
	if q.effective == nil {
		return 0
	}
	evicted := 0
	ageLimit := q.effective.GetMaxQueueAge().AsDuration()
	for q.count > 0 {
		if limit > 0 && evicted >= limit {
			return evicted
		}
		age := now.Sub(q.entries[q.head].enqueuedAt)
		if age <= ageLimit {
			return evicted
		}
		q.evictOldestLocked(&q.drops.Expired)
		evicted++
	}
	return evicted
}

func (q *ModelIngressQueue) headExpiredLocked(now time.Time) bool {
	return q.count > 0 && now.Sub(q.entries[q.head].enqueuedAt) > q.effective.GetMaxQueueAge().AsDuration()
}

func (q *ModelIngressQueue) cleanupExpired(now time.Time) {
	for {
		q.mu.Lock()
		q.evictExpiredLocked(now, modelIngressRequestPathEvictionLimit)
		more := q.headExpiredLocked(now)
		q.mu.Unlock()
		if !more {
			return
		}
		runtime.Gosched()
	}
}

func (q *ModelIngressQueue) evictOldestLocked(counter *uint64) {
	item := q.entries[q.head]
	q.entries[q.head] = nil
	q.head = (q.head + 1) % len(q.entries)
	q.count--
	q.queuedBytes -= item.entryBytes
	digest := item.Traffic.GetModelProfileDigest()
	ref := q.profiles[digest]
	if ref != nil {
		ref.queued--
		q.removeProfileIfUnusedLocked(digest, ref)
	}
	q.recordDropLocked(counter, 1)
}

func (q *ModelIngressQueue) batchReadyLocked(maxItems int, maxBytes uint64) bool {
	if q.count == 0 {
		return false
	}
	first := q.entries[q.head]
	digest := first.Traffic.GetModelProfileDigest()
	bytes := first.profileBytes
	for index := 0; index < q.count && index < maxItems; index++ {
		item := q.entries[(q.head+index)%len(q.entries)]
		if item.Traffic.GetModelProfileDigest() != digest {
			return true
		}
		if bytes+item.entryBytes > maxBytes {
			return true
		}
		bytes += item.entryBytes
		if index+1 == maxItems {
			return true
		}
	}
	return false
}

func (q *ModelIngressQueue) leaseBatchLocked(maxItems int, maxBytes uint64) *ModelIngressBatch {
	for q.count > 0 {
		first := q.entries[q.head]
		if first.profileBytes+first.entryBytes <= maxBytes {
			break
		}
		q.evictOldestLocked(&q.drops.ItemTooLarge)
	}
	if q.count == 0 {
		return nil
	}
	first := q.entries[q.head]
	digest := first.Traffic.GetModelProfileDigest()
	batch := &ModelIngressBatch{Profile: first.Profile, ProfileDigest: digest, queue: q}
	bytes := first.profileBytes
	for q.count > 0 && len(batch.items) < maxItems {
		item := q.entries[q.head]
		if item.Traffic.GetModelProfileDigest() != digest || (len(batch.items) > 0 && bytes+item.entryBytes > maxBytes) {
			break
		}
		q.entries[q.head] = nil
		q.head = (q.head + 1) % len(q.entries)
		q.count--
		q.queuedBytes -= item.entryBytes
		q.inFlightItems++
		q.inFlightBytes += item.entryBytes
		ref := q.profiles[digest]
		ref.queued--
		ref.inFlight++
		batch.items = append(batch.items, item)
		batch.Traffic = append(batch.Traffic, item.Traffic)
		bytes += item.entryBytes
	}
	return batch
}

func (q *ModelIngressQueue) totalBytesLocked() uint64 {
	return q.queuedBytes + q.inFlightBytes + q.profileBytes
}

func (q *ModelIngressQueue) partitionedBytesLocked() (uint64, uint64) {
	queued := q.queuedBytes
	inFlight := q.inFlightBytes
	for _, ref := range q.profiles {
		if ref.queued > 0 {
			queued += ref.bytes
		} else {
			inFlight += ref.bytes
		}
	}
	return queued, inFlight
}

func (q *ModelIngressQueue) stateLocked(now time.Time) unitv1.ModelIngressWindowState {
	if uint64(q.count)+q.inFlightItems > uint64(q.effective.GetMaxItems()) || q.totalBytesLocked() > q.effective.GetMaxRetainedBytes() {
		return unitv1.ModelIngressWindowState_MODEL_INGRESS_WINDOW_STATE_CONVERGING
	}
	if q.count > 0 && now.Sub(q.entries[q.head].enqueuedAt) > q.effective.GetMaxQueueAge().AsDuration() {
		return unitv1.ModelIngressWindowState_MODEL_INGRESS_WINDOW_STATE_CONVERGING
	}
	if len(q.reasons) > 0 {
		return unitv1.ModelIngressWindowState_MODEL_INGRESS_WINDOW_STATE_DEGRADED
	}
	return unitv1.ModelIngressWindowState_MODEL_INGRESS_WINDOW_STATE_APPLIED
}

func (q *ModelIngressQueue) removeProfileIfUnusedLocked(digest string, ref *modelIngressProfileRef) {
	if ref.queued == 0 && ref.inFlight == 0 {
		q.profileBytes -= ref.bytes
		delete(q.profiles, digest)
	}
}

func (q *ModelIngressQueue) recordDropLocked(counter *uint64, count uint64) {
	*counter += count
	q.dropped.Add(count)
}

func (q *ModelIngressQueue) signalLocked() {
	select {
	case q.notify <- struct{}{}:
	default:
	}
}

func modelIngressItemRetainedBytes(item *ModelIngressItem) uint64 {
	if item == nil {
		return 0
	}
	return modelIngressTrafficRetainedBytes(item.Traffic) + modelIngressProfileRetainedBytes(item.Profile)
}

func modelIngressTrafficRetainedBytes(traffic *modelsidev1.NormalizedTraffic) uint64 {
	if traffic == nil {
		return 0
	}
	bytes := uint64(256 + cap(traffic.GetBody()))
	bytes += modelIngressStringBytes(
		traffic.GetSchemaVersion(), traffic.GetRequestId(), traffic.GetUnitId(), traffic.GetAssetId(),
		traffic.GetGenerationId(), traffic.GetModelProfileId(), traffic.GetModelProfileDigest(),
		traffic.GetMethod(), traffic.GetRoute(), traffic.GetContentType(),
	)
	bytes += uint64(cap(traffic.GetHeaders())+cap(traffic.GetQueryParameters())+cap(traffic.GetCoverage())) * 8
	for _, values := range traffic.GetHeaders() {
		bytes += modelIngressStringValuesRetainedBytes(values)
	}
	for _, values := range traffic.GetQueryParameters() {
		bytes += modelIngressStringValuesRetainedBytes(values)
	}
	for _, coverage := range traffic.GetCoverage() {
		bytes += 64 + uint64(len(coverage.GetParserProfileDigest()))
	}
	return bytes
}

func modelIngressProfileRetainedBytes(profile *artifactv1.ModelProfile) uint64 {
	if profile == nil {
		return 0
	}
	bytes := uint64(192)
	bytes += modelIngressStringBytes(profile.GetProfileId(), profile.GetModelGroup(), profile.GetModelType(), profile.GetModelVersion())
	bytes += uint64(cap(profile.GetAllowedHeaders())) * 16
	for _, header := range profile.GetAllowedHeaders() {
		bytes += uint64(len(header))
	}
	return bytes
}

func modelIngressStringValuesRetainedBytes(values *modelsidev1.StringValues) uint64 {
	if values == nil {
		return 0
	}
	bytes := uint64(48 + len(values.GetName()) + cap(values.GetValues())*16)
	for _, value := range values.GetValues() {
		bytes += uint64(len(value))
	}
	return bytes
}

func modelIngressStringBytes(values ...string) uint64 {
	var bytes uint64
	for _, value := range values {
		bytes += uint64(len(value))
	}
	return bytes
}

// NewNormalizedModelTraffic 把同步检查完成后的规范视图转换为 ModelSide 输入。
// Body 直接取得 view.Body 的唯一模型副本；调用方成功入队后不得再用于遥测或智能代理采样。
func NewNormalizedModelTraffic(requestID, unitID, assetID string, generationID string, generationSeq int64, profile *artifactv1.ModelProfile, profileDigest string, view CanonicalView, now time.Time) *ModelIngressItem {
	if profile == nil || strings.TrimSpace(profileDigest) == "" || strings.TrimSpace(generationID) == "" || generationSeq <= 0 {
		return nil
	}
	body := view.Body
	bodyTruncated := false
	if max := int(profile.GetMaxBodyBytes()); max > 0 && len(body) > max {
		body = body[:max]
		bodyTruncated = true
	}
	traffic := &modelsidev1.NormalizedTraffic{
		SchemaVersion: NormalizedModelTrafficSchema,
		RequestId:     requestID, UnitId: unitID, AssetId: assetID,
		GenerationId: generationID, GenerationSeq: generationSeq,
		ModelProfileId: profile.GetProfileId(), ModelProfileDigest: profileDigest,
		Method: strings.ToUpper(strings.TrimSpace(view.Method)), Route: view.Path,
		Headers:         allowedModelHeaders(view.Headers, profile.GetAllowedHeaders()),
		QueryParameters: modelQueryParameters(view), Body: body,
		ContentType: firstHeaderValue(view.Headers, "content-type"),
		BodyLength:  modelBodyLength(view), BodyTruncated: bodyTruncated || modelBodyPartial(view),
		Coverage: modelCoverage(view), OccurredAt: timestamppb.New(now),
	}
	return &ModelIngressItem{Profile: profile, Traffic: traffic}
}

func allowedModelHeaders(headers map[string][]string, allowed []string) []*modelsidev1.StringValues {
	names := append([]string(nil), allowed...)
	sort.Strings(names)
	out := make([]*modelsidev1.StringValues, 0, len(names))
	for _, name := range names {
		values := headerValuesFold(headers, name)
		if len(values) > 0 {
			out = append(out, &modelsidev1.StringValues{Name: strings.ToLower(name), Values: append([]string(nil), values...)})
		}
	}
	return out
}

func modelQueryParameters(view CanonicalView) []*modelsidev1.StringValues {
	names := make([]string, 0, len(view.Query))
	for name := range view.Query {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]*modelsidev1.StringValues, 0, len(names))
	for _, name := range names {
		out = append(out, &modelsidev1.StringValues{Name: name, Values: append([]string(nil), view.Query[name]...)})
	}
	return out
}

func modelCoverage(view CanonicalView) []*commonv1.InspectionCoverage {
	out := make([]*commonv1.InspectionCoverage, 0, len(view.Coverage))
	for _, coverage := range view.Coverage {
		out = append(out, &commonv1.InspectionCoverage{
			Target: coverage.Target, Status: coverage.Status, InspectedBytes: coverage.Inspected,
			TotalBytesKnown: coverage.Total, ParserProfileDigest: view.ProfileDigest,
		})
	}
	return out
}

func modelBodyLength(view CanonicalView) int64 {
	for _, coverage := range view.Coverage {
		if coverage.Target == commonv1.InspectionSurface_INSPECTION_SURFACE_BODY {
			if coverage.Total > 0 {
				return coverage.Total
			}
			return int64(len(view.Body))
		}
	}
	return int64(len(view.Body))
}

func modelBodyPartial(view CanonicalView) bool {
	for _, coverage := range view.Coverage {
		if coverage.Target == commonv1.InspectionSurface_INSPECTION_SURFACE_BODY &&
			coverage.Status == commonv1.CoverageStatus_COVERAGE_STATUS_PARTIAL {
			return true
		}
	}
	return false
}

func firstHeaderValue(headers map[string][]string, name string) string {
	values := headerValuesFold(headers, name)
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func headerValuesFold(headers map[string][]string, name string) []string {
	for key, values := range headers {
		if strings.EqualFold(key, name) {
			return values
		}
	}
	return nil
}
