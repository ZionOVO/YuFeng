package edgecore

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "yufeng/proto/gen/commonv1"
	eventv1 "yufeng/proto/gen/eventv1"
)

// Telemetry 把本地演示事件逐行追加为 JavaScript 对象表示法记录。
// 该写入器只记录方法、路径、已脱敏查询串与命中规则，不记录请求体或请求头；
// 生产路径使用 TrafficEvent 构造带覆盖度、世代轨迹和来源假名的上行事件。
type Telemetry struct {
	mu sync.Mutex
	w  io.Writer
}

// NewTelemetry 构造遥测写入器；w 为 nil 时遥测静默丢弃（测试用）。
func NewTelemetry(w io.Writer) *Telemetry {
	if w == nil {
		return &Telemetry{}
	}
	return &Telemetry{w: w}
}

// Record 记录一次请求的处置结果。
func (t *Telemetry) Record(req Request, res Result, action Action) error {
	if t.w == nil {
		return nil
	}
	e := &eventv1.Event{
		Id:         newEventID(),
		OccurredAt: timestamppb.Now(),
		AssetId:    req.AssetID,
		Source:     "yufeng-edge",
		Kind:       eventv1.Kind_KIND_TRAFFIC,
		Verdict:    verdictOf(action),
		Traffic: &eventv1.Event_Http{
			Http: &eventv1.Http{
				Method:        req.Method,
				Path:          req.Path,
				QueryRedacted: RedactQuery(req.Query),
			},
		},
	}
	for _, v := range res.Verdicts {
		if v.RuleID == "" && v.Message == "" {
			continue // 放行结论无内容，不产生空检测记录
		}
		e.Detections = append(e.Detections, &eventv1.Detection{
			DetectorId: v.DetectorID,
			RuleId:     v.RuleID,
			Confidence: v.Confidence,
			Message:    v.Message,
			Tier:       commonv1.Tier_TIER_L1_TRAFFIC,
		})
	}
	line, err := protojson.Marshal(e)
	if err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	_, err = fmt.Fprintf(t.w, "%s\n", line)
	return err
}

// newEventID 生成 128 位随机十六进制事件标识——纳秒时间戳在并发下有碰撞风险。
func newEventID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand 失败几乎不可能发生；退回时间戳保证可用
		return fmt.Sprintf("evt-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// TrafficEvent 把一次数据面裁决编成生产流量事件：检测键进 detections，来源只进假名。
func TrafficEvent(unitID, assetID, requestID string, req Request, dec Decision, source SourcePseudonymizer) *eventv1.Event {
	e := &eventv1.Event{
		Id: newEventID(), OccurredAt: timestamppb.Now(),
		UnitId: unitID, AssetId: assetID, RequestId: requestID,
		Source: "yufeng-edge", Kind: eventv1.Kind_KIND_TRAFFIC,
		Verdict:          verdictOf(dec.Action),
		WouldHaveBlocked: dec.WouldHaveBlocked,
		IngressPosture:   dec.Posture,
		Observation:      observationOf(dec),
		TrafficKey:       firstNonEmpty(assetID, req.AssetID),
		GenerationId:     dec.GenerationID,
		GenerationSeq:    dec.GenerationSeq,
		Traffic: &eventv1.Event_Http{Http: &eventv1.Http{
			Method: req.Method, Path: req.Path, QueryRedacted: RedactQuery(req.Query),
			SrcPseudonym: source.Pseudonym(req.ClientAddress),
		}},
	}
	for _, c := range dec.Inspection.Coverage {
		e.Coverage = append(e.Coverage, &commonv1.InspectionCoverage{
			Target: c.Target, Status: c.Status,
			InspectedBytes: c.Inspected, TotalBytesKnown: c.Total,
		})
	}
	for _, d := range dec.Detections {
		id := d.InspectorID
		if id == "" {
			id = "inspector"
		}
		e.Detections = append(e.Detections, &eventv1.Detection{
			DetectorId: id, RuleId: d.RuleID, Confidence: 1,
			Message: "sync detection", Tier: commonv1.Tier_TIER_L1_TRAFFIC,
			AttackClass: d.Class, AnomalyScore: d.Score,
			Key: &commonv1.DetectionKey{
				DetectorId: id, DetectorVersion: d.Version, DetectorManifestDigest: d.ManifestDigest,
				RuleId: d.RuleID, Phase: d.Phase, TargetLocation: d.Location,
				TargetSelector: d.Selector, NormalizationProfileDigest: d.ProfileDigest,
			},
		})
	}
	if len(e.Detections) == 0 {
		for _, v := range dec.Verdicts {
			if v.RuleID == "" && v.Message == "" {
				continue
			}
			e.Detections = append(e.Detections, &eventv1.Detection{
				DetectorId: v.DetectorID, RuleId: v.RuleID, Confidence: v.Confidence,
				Message: v.Message, Tier: commonv1.Tier_TIER_L1_TRAFFIC,
			})
		}
	}
	for _, o := range dec.Observations {
		e.ReleaseTraces = append(e.ReleaseTraces, &eventv1.ReleaseTrace{
			ReleaseId: o.ReleaseID, ArtifactId: o.ArtifactID, Mode: o.Mode,
			CanaryPercent: o.CanaryPercent, CanarySelected: o.CanarySelected, Matched: o.Matched,
		})
	}
	return e
}

func verdictOf(action Action) eventv1.Verdict {
	switch action {
	case ActionBlock:
		return eventv1.Verdict_VERDICT_BLOCK
	case ActionObserve:
		return eventv1.Verdict_VERDICT_OBSERVE
	default:
		return eventv1.Verdict_VERDICT_ALLOW
	}
}

func observationOf(dec Decision) commonv1.ObservationState {
	if coverageHasError(dec.Inspection.Coverage) {
		return commonv1.ObservationState_OBSERVATION_STATE_INSPECTION_ERROR
	}
	for _, c := range dec.Inspection.Coverage {
		if c.Status != commonv1.CoverageStatus_COVERAGE_STATUS_FULL && c.Status != commonv1.CoverageStatus_COVERAGE_STATUS_ABSENT {
			return commonv1.ObservationState_OBSERVATION_STATE_INSPECTION_PARTIAL
		}
	}
	if len(dec.Detections) > 0 {
		return commonv1.ObservationState_OBSERVATION_STATE_SYNC_DETECTED
	}
	return commonv1.ObservationState_OBSERVATION_STATE_SYNC_NO_DETECTION
}

func firstNonEmpty(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}
