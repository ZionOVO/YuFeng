package edgecore

import (
	"context"
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
)

// NormalizedModelTrafficSchema 是 Edge 与 ModelSide 的规范流量契约版本。
const NormalizedModelTrafficSchema = "normalized-http/v1"

// ModelIngressItem 把流量与产生它的签名模型档案绑定，跨世代排队时不读取当前策略。
type ModelIngressItem struct {
	Profile *artifactv1.ModelProfile
	Traffic *modelsidev1.NormalizedTraffic
}

// ModelIngressQueue 是 Edge 到 ModelSide 的条目数与正文总字节双重有界队列。
type ModelIngressQueue struct {
	ch       chan *ModelIngressItem
	mu       sync.Mutex
	bytes    int64
	maxBytes int64
	dropped  atomic.Uint64
}

// NewModelIngressQueue 按平台预算构造非阻塞模型输入队列。
func NewModelIngressQueue() *ModelIngressQueue {
	return &ModelIngressQueue{
		ch:       make(chan *ModelIngressItem, kernel.ModelSideIngressQueueMax),
		maxBytes: kernel.ModelSideIngressQueueBytes,
	}
}

// Offer 转移一个规范流量正文的本地所有权；满时立即丢旁路并计数。
func (q *ModelIngressQueue) Offer(item *ModelIngressItem) bool {
	if q == nil || item == nil || item.Profile == nil || item.Traffic == nil {
		return false
	}
	size := int64(len(item.Traffic.GetBody()))
	q.mu.Lock()
	defer q.mu.Unlock()
	if size > q.maxBytes || q.bytes+size > q.maxBytes {
		q.dropped.Add(1)
		return false
	}
	select {
	case q.ch <- item:
		q.bytes += size
		return true
	default:
		q.dropped.Add(1)
		return false
	}
}

// Take 供后台发送器取走一项，并在离开队列时释放正文预算。
func (q *ModelIngressQueue) Take(ctx context.Context) (*ModelIngressItem, bool) {
	if q == nil {
		return nil, false
	}
	select {
	case <-ctx.Done():
		return nil, false
	case item := <-q.ch:
		q.mu.Lock()
		q.bytes -= int64(len(item.Traffic.GetBody()))
		if q.bytes < 0 {
			q.bytes = 0
		}
		q.mu.Unlock()
		return item, true
	}
}

// DropOldest 在请求过载时释放最旧旁路项；同步 Gate 不受影响。
func (q *ModelIngressQueue) DropOldest() {
	if q == nil {
		return
	}
	select {
	case item := <-q.ch:
		q.mu.Lock()
		q.bytes -= int64(len(item.Traffic.GetBody()))
		if q.bytes < 0 {
			q.bytes = 0
		}
		q.mu.Unlock()
		q.dropped.Add(1)
	default:
	}
}

// Dropped 返回因条目数或正文预算耗尽而丢弃的模型旁路数。
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
	return len(q.ch)
}

// QueuedBodyBytes 返回当前由队列持有的规范正文总字节数。
func (q *ModelIngressQueue) QueuedBodyBytes() int64 {
	if q == nil {
		return 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.bytes
}

// MarkDropped 记录后台传输失败；失败项不回到请求路径，也不转向 Brain。
func (q *ModelIngressQueue) MarkDropped() {
	if q != nil {
		q.dropped.Add(1)
	}
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
	return &ModelIngressItem{Profile: proto.Clone(profile).(*artifactv1.ModelProfile), Traffic: traffic}
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
