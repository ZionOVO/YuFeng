package edgecore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
	"time"
	"unicode"

	"yufeng/lib/kernel"
	artifactv1 "yufeng/proto/gen/artifactv1"
	commonv1 "yufeng/proto/gen/commonv1"
	eventv1 "yufeng/proto/gen/eventv1"
)

// EvidencePolicySchema / EvidenceDigestSchema / ForwardPolicySchema 是世代成员载荷标识。
const (
	EvidencePolicySchema      = "evidence-policy/v1"
	EvidenceDigestSchema      = "evidence-digest/v1"
	ForwardPolicySchema       = "forward-policy/v1"
	TrafficReviewPolicySchema = "traffic-review-policy/v1"
	DetectorManifestSchema    = "detector-manifest/v1"
	TaxonomyMapperSchema      = "taxonomy/v1"
	NormalizerSchema          = "normalizer/v1"
)

// DefaultEvidenceDigest 是未装载摘要制品时的家用投影。
func DefaultEvidenceDigest() *artifactv1.EvidenceDigest {
	return &artifactv1.EvidenceDigest{
		Algorithm:    commonv1.EvidenceDigestAlgorithm_EVIDENCE_DIGEST_ALGORITHM_SPAN_SHA256,
		MaxSpanBytes: 256,
		Fields:       []string{"method", "route_template", "selector", "span_hash", "charset_class"},
	}
}

// DefaultTrafficReviewPolicy 返回默认关闭且预置生产硬上限的流量审查策略。
func DefaultTrafficReviewPolicy() *artifactv1.TrafficReviewPolicy {
	return &artifactv1.TrafficReviewPolicy{
		Mode:                   artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_OFF,
		WindowSeconds:          int32(kernel.TrafficReviewWindow / time.Second),
		TopRouteCells:          kernel.TrafficReviewTopRoutes,
		MaxCandidatesPerWindow: kernel.TrafficReviewCandidatesPerWindow,
		MaxEvidenceBytes:       kernel.TrafficReviewEvidenceBytes,
		VaultMaxBytes:          kernel.TrafficReviewVaultBytes,
		EvidenceTtlSeconds:     int64(kernel.TrafficReviewEvidenceTTL / time.Second),
	}
}

// ProjectEventEvidence 按历史世代摘要配置从已脱敏事件投影远端票据证据。
func ProjectEventEvidence(event *eventv1.Event, digest *artifactv1.EvidenceDigest) (*eventv1.EvidenceProjection, error) {
	if event == nil || event.GetHttp() == nil || strings.TrimSpace(event.GetHttp().GetMethod()) == "" || strings.TrimSpace(event.GetHttp().GetPath()) == "" {
		return nil, errors.New("http event projection is incomplete")
	}
	query, err := url.ParseQuery(event.GetHttp().GetQueryRedacted())
	if err != nil {
		return nil, errors.New("redacted query is invalid")
	}
	found := make([]Detection, 0, len(event.GetDetections()))
	for _, detection := range event.GetDetections() {
		if detection == nil {
			continue
		}
		key := detection.GetKey()
		found = append(found, Detection{
			InspectorID: detection.GetDetectorId(), RuleID: detection.GetRuleId(),
			Class: detection.GetAttackClass(), Score: detection.GetAnomalyScore(),
			Location: key.GetTargetLocation(), Selector: key.GetTargetSelector(),
		})
	}
	return ProjectEvidence(CanonicalView{
		Method: event.GetHttp().GetMethod(), Path: event.GetHttp().GetPath(), Query: query,
	}, digest, found), nil
}

// DefaultForwardKind 把边缘本地未指定策略收成家用打分路径。
func DefaultForwardKind(k commonv1.ForwardPolicyKind) commonv1.ForwardPolicyKind {
	if k == commonv1.ForwardPolicyKind_FORWARD_POLICY_KIND_UNSPECIFIED {
		return commonv1.ForwardPolicyKind_FORWARD_POLICY_KIND_WORKER_SCORE
	}
	return k
}

// ProjectEvidence 按摘要算法投影特征，禁止拷贝请求原文。
func ProjectEvidence(view CanonicalView, digest *artifactv1.EvidenceDigest, found []Detection) *eventv1.EvidenceProjection {
	if digest == nil {
		digest = DefaultEvidenceDigest()
	}
	want := map[string]bool{}
	for _, f := range digest.Fields {
		want[strings.ToLower(f)] = true
	}
	if len(want) == 0 {
		for _, f := range DefaultEvidenceDigest().Fields {
			want[f] = true
		}
	}
	fields := map[string]string{}
	if want["method"] {
		fields["method"] = view.Method
	}
	if want["route_template"] {
		fields["route_template"] = view.Path
	}
	if want["selector"] && len(found) > 0 {
		fields["selector"] = found[0].Location.String()
	}
	if want["span_hash"] {
		span := view.Method + " " + view.Path
		if view.Query != nil {
			span += "?" + view.Query.Encode()
		}
		max := int(digest.MaxSpanBytes)
		if max <= 0 {
			max = 256
		}
		if len(span) > max {
			span = span[:max]
		}
		sum := sha256.Sum256([]byte(span))
		fields["span_hash"] = hex.EncodeToString(sum[:])
	}
	if want["charset_class"] {
		fields["charset_class"] = charsetClass(view)
	}
	return &eventv1.EvidenceProjection{
		Algorithm:    digest.Algorithm,
		MaxSpanBytes: digest.MaxSpanBytes,
		Fields:       fields,
	}
}

func charsetClass(view CanonicalView) string {
	var b strings.Builder
	b.WriteString(view.Path)
	if view.Query != nil {
		b.WriteString(view.Query.Encode())
	}
	s := b.String()
	hasDigit, hasAlpha, hasOther := false, false, false
	for _, r := range s {
		switch {
		case unicode.IsDigit(r):
			hasDigit = true
		case unicode.IsLetter(r):
			hasAlpha = true
		case r == '/' || r == '?' || r == '=' || r == '&' || r == '-' || r == '_' || r == '.':
		default:
			hasOther = true
		}
	}
	switch {
	case hasOther:
		return "mixed"
	case hasAlpha && hasDigit:
		return "alnum"
	case hasDigit:
		return "digit"
	default:
		return "alpha"
	}
}

// TicketFromView 编一份边缘本地异步旁路任务。
// 远端检查票据只能由中台按已接受事件与历史世代冻结，不得使用本函数派发。
func TicketFromView(eventID, assetID, unitID string, view CanonicalView, found []Detection, digest *artifactv1.EvidenceDigest, forward commonv1.ForwardPolicyKind, posture commonv1.IngressPosture) *eventv1.CheckTicket {
	cov := make([]*commonv1.InspectionCoverage, 0, len(view.Coverage))
	for _, c := range view.Coverage {
		cov = append(cov, &commonv1.InspectionCoverage{
			Target: c.Target, Status: c.Status,
			InspectedBytes: c.Inspected, TotalBytesKnown: c.Total,
		})
	}
	dets := make([]*eventv1.Detection, 0, len(found))
	for _, d := range found {
		dets = append(dets, &eventv1.Detection{
			DetectorId: d.InspectorID, RuleId: d.RuleID, Confidence: 1,
			AttackClass: d.Class, AnomalyScore: d.Score,
			Key: &commonv1.DetectionKey{DetectorId: d.InspectorID, RuleId: d.RuleID, TargetLocation: d.Location},
		})
	}
	return &eventv1.CheckTicket{
		EventId: eventID, AssetId: assetID, UnitId: unitID,
		Posture: posture, Coverage: cov, Detections: dets,
		Evidence:      ProjectEvidence(view, digest, found),
		RouteTemplate: view.Path, Method: view.Method,
		Forward: DefaultForwardKind(forward),
	}
}
