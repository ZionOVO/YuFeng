package edgecore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"mime"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"yufeng/lib/kernel"
	artifactv1 "yufeng/proto/gen/artifactv1"
	commonv1 "yufeng/proto/gen/commonv1"
	eventv1 "yufeng/proto/gen/eventv1"
	telemetryv1 "yufeng/proto/gen/telemetryv1"
)

// ValidateTrafficReviewPolicy 拒绝越过产品硬上限的世代策略。
func ValidateTrafficReviewPolicy(policy *artifactv1.TrafficReviewPolicy) error {
	if policy == nil {
		return errors.New("policy is required")
	}
	if policy.GetMode() < artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_OFF || policy.GetMode() > artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_SHADOW_CANDIDATES {
		return errors.New("traffic review mode is invalid")
	}
	if policy.GetMode() == artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_OFF {
		return nil
	}
	if policy.GetWindowSeconds() != int32(kernel.TrafficReviewWindow/time.Second) {
		return errors.New("window_seconds must be 300")
	}
	if policy.GetTopRouteCells() < 1 || policy.GetTopRouteCells() > kernel.TrafficReviewTopRoutes {
		return errors.New("top_route_cells is out of range")
	}
	if policy.GetMaxCandidatesPerWindow() < 1 || policy.GetMaxCandidatesPerWindow() > kernel.TrafficReviewCandidatesPerWindow {
		return errors.New("max_candidates_per_window is out of range")
	}
	if policy.GetMaxEvidenceBytes() < 1 || policy.GetMaxEvidenceBytes() > kernel.TrafficReviewEvidenceBytes {
		return errors.New("max_evidence_bytes is out of range")
	}
	if policy.GetVaultMaxBytes() < int64(policy.GetMaxEvidenceBytes()) || policy.GetVaultMaxBytes() > kernel.TrafficReviewVaultBytes {
		return errors.New("vault_max_bytes is out of range")
	}
	if policy.GetEvidenceTtlSeconds() < int64(time.Hour/time.Second) || policy.GetEvidenceTtlSeconds() > int64(kernel.TrafficReviewEvidenceTTL/time.Second) {
		return errors.New("evidence_ttl_seconds is out of range")
	}
	return nil
}

type reviewRouteKey struct {
	method string
	route  string
}

type reviewRouteCount struct {
	requests   int64
	critical   int64
	blocked    int64
	incomplete int64
}

type reviewWindowState struct {
	start       time.Time
	end         time.Time
	unitID      string
	assetID     string
	generation  string
	genSeq      int64
	policy      string
	requests    int64
	critical    int64
	blocked     int64
	observed    int64
	incomplete  int64
	dropped     int64
	dropReasons map[string]int64
	routes      map[reviewRouteKey]*reviewRouteCount
	other       reviewRouteCount
	candidates  []*telemetryv1.ReviewCandidate
	evidence    map[string][]byte
}

// ReviewCollector 把逐请求观察压成固定窗口与少量代表候选。
type ReviewCollector struct {
	mu           sync.Mutex
	policy       *artifactv1.TrafficReviewPolicy
	digest       string
	vault        *EvidenceVault
	state        *reviewWindowState
	readyW       []*telemetryv1.TrafficWindow
	readyC       []*telemetryv1.ReviewCandidate
	readyVersion uint64
}

// NewReviewCollector 构造有界收集器；策略必须先通过世代校验。
func NewReviewCollector(policy *artifactv1.TrafficReviewPolicy, digest string, vault *EvidenceVault) (*ReviewCollector, error) {
	if err := ValidateTrafficReviewPolicy(policy); err != nil {
		return nil, err
	}
	return &ReviewCollector{policy: policy, digest: digest, vault: vault}, nil
}

// Observe 记录一次裁决；候选原文只写边缘加密证据库。
func (c *ReviewCollector) Observe(now time.Time, unitID, assetID, requestID string, req Request, dec Decision) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.policy.GetMode() == artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_OFF {
		return
	}
	c.ensureWindow(now, unitID, assetID, dec)
	s := c.state
	s.requests++
	critical, incomplete := reviewFlags(dec)
	if critical {
		s.critical++
	}
	if dec.Action == ActionBlock {
		s.blocked++
	}
	if dec.Action == ActionObserve || dec.WouldHaveBlocked {
		s.observed++
	}
	if incomplete {
		s.incomplete++
	}
	routeTemplate := TrafficRouteTemplate(req.Path)
	key := reviewRouteKey{method: strings.ToUpper(req.Method), route: routeTemplate}
	cell := s.routes[key]
	if cell == nil {
		if len(s.routes) < int(c.policy.GetTopRouteCells()) {
			cell = &reviewRouteCount{}
			s.routes[key] = cell
		} else {
			evictedKey, evicted := leastFrequentReviewRoute(s.routes)
			s.other.requests += evicted.requests
			s.other.critical += evicted.critical
			s.other.blocked += evicted.blocked
			s.other.incomplete += evicted.incomplete
			delete(s.routes, evictedKey)
			cell = &reviewRouteCount{}
			s.routes[key] = cell
		}
	}
	cell.requests++
	if critical {
		cell.critical++
	}
	if dec.Action == ActionBlock {
		cell.blocked++
	}
	if incomplete {
		cell.incomplete++
	}
	c.maybeCandidate(now, requestID, routeTemplate, req, dec, critical, incomplete)
}

func leastFrequentReviewRoute(routes map[reviewRouteKey]*reviewRouteCount) (reviewRouteKey, *reviewRouteCount) {
	var selected reviewRouteKey
	var count *reviewRouteCount
	for key, candidate := range routes {
		if count == nil || candidate.requests < count.requests ||
			(candidate.requests == count.requests && key.method+" "+key.route < selected.method+" "+selected.route) {
			selected, count = key, candidate
		}
	}
	return selected, count
}

func (c *ReviewCollector) ensureWindow(now time.Time, unitID, assetID string, dec Decision) {
	window := time.Duration(c.policy.GetWindowSeconds()) * time.Second
	start := now.UTC().Truncate(window)
	if c.state != nil && c.state.start.Equal(start) && c.state.unitID == unitID && c.state.assetID == assetID &&
		c.state.generation == dec.GenerationID && c.state.genSeq == dec.GenerationSeq {
		return
	}
	if c.state != nil {
		w, candidates := c.freeze(c.state)
		c.readyW = append(c.readyW, w)
		c.readyC = append(c.readyC, candidates...)
		c.readyVersion++
	}
	c.state = &reviewWindowState{
		start: start, end: start.Add(window), unitID: unitID, assetID: assetID,
		generation: dec.GenerationID, genSeq: dec.GenerationSeq, policy: c.digest,
		routes:      make(map[reviewRouteKey]*reviewRouteCount),
		dropReasons: make(map[string]int64), evidence: make(map[string][]byte),
	}
}

func (c *ReviewCollector) maybeCandidate(now time.Time, requestID, routeTemplate string, req Request, dec Decision, critical, incomplete bool) {
	s := c.state
	if c.policy.GetMode() < artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_REDACTED_CASES {
		return
	}
	risk, reasons := reviewRisk(dec, critical, incomplete)
	baseline := risk == 0
	if baseline && hasReviewBaseline(s.candidates) {
		return
	}
	key := req.Method + "\x00" + routeTemplate + "\x00" + strings.Join(reasons, ",")
	if hasReviewCandidateKey(s.candidates, key) {
		return
	}
	candidate := &telemetryv1.ReviewCandidate{
		CandidateId: newEventID(), WindowId: reviewWindowID(s), UnitId: s.unitID, AssetId: s.assetID,
		OccurredAt: timestamppb.New(now), RequestId: requestID, Method: req.Method, RouteTemplate: routeTemplate,
		RiskScore: risk, RiskReasons: reasons, GenerationId: dec.GenerationID, GenerationSeq: dec.GenerationSeq,
		Baseline: baseline, ReviewMode: c.policy.GetMode(), EvidenceExpiresAt: timestamppb.New(now.Add(time.Duration(c.policy.GetEvidenceTtlSeconds()) * time.Second)),
		Evidence: &eventv1.EvidenceProjection{Fields: map[string]string{"method": req.Method, "route_template": routeTemplate}},
	}
	slot, accepted := reviewCandidateSlot(s, candidate, int(c.policy.GetMaxCandidatesPerWindow()))
	if !accepted {
		return
	}
	if c.policy.GetMode() >= artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_EVIDENCE_ON_APPROVAL {
		document := controlledTrafficEvidence(req, dec, routeTemplate)
		doc, err := marshalBoundedEvidence(document, int(c.policy.GetMaxEvidenceBytes()))
		if err != nil {
			recordEvidenceDrop(s, "encoding_failed")
			return
		}
		s.evidence[candidate.GetCandidateId()] = doc
	}
	if slot == len(s.candidates) {
		s.candidates = append(s.candidates, candidate)
	} else {
		delete(s.evidence, s.candidates[slot].GetCandidateId())
		s.candidates[slot] = candidate
	}
}

var (
	reviewStaticPathSegment  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._~-]{0,63}$`)
	reviewNumericPathSegment = regexp.MustCompile(`^[0-9]+$`)
	reviewHexPathSegment     = regexp.MustCompile(`(?i)^[0-9a-f-]{12,}$`)
)

// TrafficRouteTemplate 把可能携标识符的请求路径收成稳定、无查询参数的统计路由。
func TrafficRouteTemplate(path string) string {
	path = strings.SplitN(path, "?", 2)[0]
	if path == "" || path == "/" {
		return "/"
	}
	segments := strings.Split(path, "/")
	for index, segment := range segments {
		switch {
		case segment == "":
		case reviewNumericPathSegment.MatchString(segment):
			segments[index] = ":number"
		case reviewHexPathSegment.MatchString(segment):
			segments[index] = ":id"
		case !reviewStaticPathSegment.MatchString(segment):
			segments[index] = ":value"
		}
	}
	normalized := strings.Join(segments, "/")
	if !strings.HasPrefix(normalized, "/") {
		normalized = "/" + normalized
	}
	if len(normalized) > 512 {
		return normalized[:512]
	}
	return normalized
}

func hasReviewCandidateKey(candidates []*telemetryv1.ReviewCandidate, key string) bool {
	for _, candidate := range candidates {
		candidateKey := candidate.GetMethod() + "\x00" + candidate.GetRouteTemplate() + "\x00" + strings.Join(candidate.GetRiskReasons(), ",")
		if candidateKey == key {
			return true
		}
	}
	return false
}

type trafficEvidenceDocument struct {
	Method        string                 `json:"method"`
	RouteTemplate string                 `json:"route_template"`
	ContentType   string                 `json:"content_type,omitempty"`
	ContentLength int                    `json:"content_length,omitempty"`
	Fields        []trafficEvidenceField `json:"fields,omitempty"`
}

type trafficEvidenceField struct {
	Selector string `json:"selector"`
	Surface  string `json:"surface"`
	Length   int    `json:"length"`
	Charset  string `json:"charset"`
	Digest   string `json:"digest"`
	Value    string `json:"value,omitempty"`
}

func controlledTrafficEvidence(req Request, dec Decision, routeTemplate string) trafficEvidenceDocument {
	contentType := trafficContentType(req.Headers)
	document := trafficEvidenceDocument{
		Method: strings.ToUpper(req.Method), RouteTemplate: routeTemplate,
		ContentType: contentType, ContentLength: len(req.Body),
	}
	seen := make(map[string]bool)
	for _, detection := range dec.Detections {
		selector := strings.ToLower(strings.TrimSpace(detection.Selector))
		if selector == "" || seen[selector] || len(document.Fields) >= shapeMaxSelectors {
			continue
		}
		value, surface, ok := controlledSelectorValue(selector, req, contentType)
		if !ok {
			continue
		}
		seen[selector] = true
		sum := sha256.Sum256([]byte(value))
		field := trafficEvidenceField{
			Selector: truncateEvidenceString(selector, 128), Surface: surface,
			Length: len(value), Charset: evidenceCharset(value), Digest: "sha256:" + hex.EncodeToString(sum[:]),
		}
		if !sensitiveEvidenceField(selector, value) {
			field.Value = truncateEvidenceString(value, 128)
		}
		document.Fields = append(document.Fields, field)
	}
	return document
}

func trafficContentType(headers map[string]string) string {
	var raw string
	for name, value := range headers {
		if strings.EqualFold(name, "content-type") {
			raw = value
			break
		}
	}
	mediaType, _, err := mime.ParseMediaType(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(mediaType)
}

func controlledSelectorValue(selector string, req Request, contentType string) (string, string, bool) {
	kind, name, ok := strings.Cut(selector, ".")
	if !ok || name == "" {
		return "", "", false
	}
	switch kind {
	case "query":
		values, err := url.ParseQuery(req.Query)
		if err != nil || !values.Has(name) {
			return "", "", false
		}
		return values.Get(name), "query", true
	case "json":
		if contentType != "application/json" && !strings.HasSuffix(contentType, "+json") {
			return "", "", false
		}
		value, found := jsonField(req.Body, name)
		return value, "body", found
	case "body", "arg":
		switch {
		case contentType == "application/json" || strings.HasSuffix(contentType, "+json"):
			value, found := jsonField(req.Body, name)
			return value, "body", found
		case contentType == "application/x-www-form-urlencoded":
			values, err := url.ParseQuery(string(req.Body))
			if err != nil || !values.Has(name) {
				return "", "", false
			}
			return values.Get(name), "body", true
		default:
			return "", "", false
		}
	default:
		return "", "", false
	}
}

var evidenceCredentialName = regexp.MustCompile(`(?i)(authorization|cookie|token|secret|password|passwd|api[_-]?key|client[_-]?cert|session|credential)`)

func sensitiveEvidenceField(selector, value string) bool {
	if evidenceCredentialName.MatchString(selector) || trafficBearer.MatchString(value) || trafficJWT.MatchString(value) ||
		trafficPEMMaterial.MatchString(value) || trafficSecretPattern.MatchString(value) {
		return true
	}
	if len(value) < 24 {
		return false
	}
	classes := 0
	for _, class := range []string{"abcdefghijklmnopqrstuvwxyz", "ABCDEFGHIJKLMNOPQRSTUVWXYZ", "0123456789", "+/_=-."} {
		if strings.ContainsAny(value, class) {
			classes++
		}
	}
	return classes >= 3 && !strings.ContainsAny(value, " \t\r\n")
}

func evidenceCharset(value string) string {
	switch {
	case value == "":
		return "empty"
	case regexp.MustCompile(`^[0-9]+$`).MatchString(value):
		return "digit"
	case regexp.MustCompile(`^[A-Za-z]+$`).MatchString(value):
		return "alpha"
	case regexp.MustCompile(`^[[:print:]]+$`).MatchString(value):
		return "ascii_print"
	default:
		return "unicode_or_binary"
	}
}

func truncateEvidenceString(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func marshalBoundedEvidence(document trafficEvidenceDocument, limit int) ([]byte, error) {
	if limit <= 0 {
		return nil, errors.New("evidence byte limit is invalid")
	}
	for {
		raw, err := json.Marshal(document)
		if err != nil {
			return nil, err
		}
		if len(raw) <= limit {
			return raw, nil
		}
		excess := len(raw) - limit
		switch {
		case len(document.Fields) > 0 && document.Fields[len(document.Fields)-1].Value != "":
			document.Fields[len(document.Fields)-1].Value = ""
		case len(document.Fields) > 0:
			document.Fields = document.Fields[:len(document.Fields)-1]
		case len(document.RouteTemplate) > 0:
			document.RouteTemplate = shrinkEvidenceString(document.RouteTemplate, excess)
		case len(document.Method) > 0:
			document.Method = shrinkEvidenceString(document.Method, excess)
		default:
			return nil, errors.New("evidence byte limit cannot fit structural envelope")
		}
	}
}

func shrinkEvidenceString(value string, remove int) string {
	if remove < 1 {
		remove = 1
	}
	runes := []rune(value)
	if remove >= len(runes) {
		return ""
	}
	return string(runes[:len(runes)-remove])
}

func recordEvidenceDrop(state *reviewWindowState, reason string) {
	state.dropped++
	state.dropReasons[reason]++
}

func hasReviewBaseline(candidates []*telemetryv1.ReviewCandidate) bool {
	for _, candidate := range candidates {
		if candidate.GetBaseline() {
			return true
		}
	}
	return false
}

func reviewCandidateSlot(state *reviewWindowState, candidate *telemetryv1.ReviewCandidate, limit int) (int, bool) {
	if len(state.candidates) < limit {
		return len(state.candidates), true
	}
	lowest := -1
	for index, existing := range state.candidates {
		if existing.GetBaseline() {
			continue
		}
		if lowest < 0 || existing.GetRiskScore() < state.candidates[lowest].GetRiskScore() {
			lowest = index
		}
	}
	if candidate.GetBaseline() && lowest >= 0 {
		return lowest, true
	}
	if lowest >= 0 && candidate.GetRiskScore() > state.candidates[lowest].GetRiskScore() {
		return lowest, true
	}
	return 0, false
}

var (
	trafficBearer        = regexp.MustCompile(`(?i)(bearer\s+)[a-z0-9._~+/=-]+`)
	trafficSecretPattern = regexp.MustCompile(`(?i)((?:access[_-]?token|refresh[_-]?token|password|secret|api[_-]?key|cookie|authorization|client[_-]?cert(?:ificate)?)["']?\s*[:=]\s*["']?)[^"'&\s,}]+`)
	trafficJWT           = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}(?:\.[A-Za-z0-9_-]{8,})?\b`)
	trafficPEMMaterial   = regexp.MustCompile(`(?s)-----BEGIN (?:CERTIFICATE|[^-]*PRIVATE KEY)-----.*?-----END (?:CERTIFICATE|[^-]*PRIVATE KEY)-----`)
)

func reviewFlags(dec Decision) (critical, incomplete bool) {
	critical = len(dec.Detections) > 0 || dec.WouldHaveBlocked || dec.Action != ActionAllow
	for _, coverage := range dec.Inspection.Coverage {
		if coverage.Status != commonv1.CoverageStatus_COVERAGE_STATUS_FULL && coverage.Status != commonv1.CoverageStatus_COVERAGE_STATUS_ABSENT {
			incomplete = true
			critical = true
		}
	}
	return critical, incomplete
}

func reviewRisk(dec Decision, critical, incomplete bool) (float64, []string) {
	var score float64
	var reasons []string
	if len(dec.Detections) > 0 {
		score = 80
		reasons = append(reasons, "sync_detection")
		if dec.Action == ActionAllow {
			reasons = append(reasons, "suspected_miss")
		}
	}
	if dec.WouldHaveBlocked || dec.Action == ActionObserve {
		if score < 75 {
			score = 75
		}
		reasons = append(reasons, "unmitigated")
	}
	if dec.Action == ActionBlock {
		if score < 70 {
			score = 70
		}
		reasons = append(reasons, "blocked")
	}
	if incomplete {
		if score < 60 {
			score = 60
		}
		reasons = append(reasons, "inspection_incomplete")
	}
	for _, detection := range dec.Detections {
		if detection.Class == commonv1.AttackClass_ATTACK_CLASS_UNMAPPED && !slices.Contains(reasons, "unmapped_detection") {
			reasons = append(reasons, "unmapped_detection")
		}
		if detection.Score > 0 && !slices.Contains(reasons, "anomaly_score") {
			reasons = append(reasons, "anomaly_score")
		}
		if candidate := 80 + detection.Score*20; candidate > score {
			score = candidate
		}
	}
	if critical && score == 0 {
		score = 50
		reasons = append(reasons, "critical")
	}
	return score, reasons
}

// Drain 返回已结束窗口；now 越过当前窗口时即使没有新请求也会冻结。
func (c *ReviewCollector) Drain(now time.Time) ([]*telemetryv1.TrafficWindow, []*telemetryv1.ReviewCandidate) {
	windows, candidates, version := c.PrepareDrain(now)
	c.CommitDrain(version)
	return windows, candidates
}

// PrepareDrain 冻结到期窗口但保留内存副本，只有 Spool 持久化成功后才能确认。
func (c *ReviewCollector) PrepareDrain(now time.Time) ([]*telemetryv1.TrafficWindow, []*telemetryv1.ReviewCandidate, uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != nil && !now.Before(c.state.end) {
		w, candidates := c.freeze(c.state)
		c.readyW = append(c.readyW, w)
		c.readyC = append(c.readyC, candidates...)
		c.readyVersion++
		c.state = nil
	}
	return append([]*telemetryv1.TrafficWindow(nil), c.readyW...),
		append([]*telemetryv1.ReviewCandidate(nil), c.readyC...), c.readyVersion
}

// CommitDrain 删除与已持久化快照完全一致的内存窗口；版本变化时保持数据等待下次重试。
func (c *ReviewCollector) CommitDrain(version uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if version != c.readyVersion {
		return
	}
	c.readyW, c.readyC = nil, nil
}

// Flush 冻结当前未结束窗口；仅在签名策略或资产世代切换时使用。
func (c *ReviewCollector) Flush() ([]*telemetryv1.TrafficWindow, []*telemetryv1.ReviewCandidate) {
	windows, candidates, version := c.PrepareFlush()
	c.CommitDrain(version)
	return windows, candidates
}

// PrepareFlush 冻结当前窗口并等待调用方完成原子 Spool 持久化。
func (c *ReviewCollector) PrepareFlush() ([]*telemetryv1.TrafficWindow, []*telemetryv1.ReviewCandidate, uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != nil {
		window, candidates := c.freeze(c.state)
		c.readyW = append(c.readyW, window)
		c.readyC = append(c.readyC, candidates...)
		c.readyVersion++
		c.state = nil
	}
	return append([]*telemetryv1.TrafficWindow(nil), c.readyW...),
		append([]*telemetryv1.ReviewCandidate(nil), c.readyC...), c.readyVersion
}

func (c *ReviewCollector) freeze(s *reviewWindowState) (*telemetryv1.TrafficWindow, []*telemetryv1.ReviewCandidate) {
	candidates := append([]*telemetryv1.ReviewCandidate(nil), s.candidates...)
	if c.policy.GetMode() >= artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_EVIDENCE_ON_APPROVAL {
		if c.vault == nil {
			for range candidates {
				recordEvidenceDrop(s, "vault_unavailable")
			}
			candidates = nil
		}
		selected := make([]*telemetryv1.ReviewCandidate, 0, len(candidates))
		for _, candidate := range candidates {
			raw := s.evidence[candidate.GetCandidateId()]
			occurredAt := s.start
			if candidate.GetOccurredAt() != nil && candidate.GetOccurredAt().IsValid() {
				occurredAt = candidate.GetOccurredAt().AsTime()
			}
			handle, digest, expires, err := c.vault.PutRisk(raw, candidate.GetRiskScore(), occurredAt)
			if err != nil {
				switch {
				case errors.Is(err, ErrEvidenceVaultLowRiskCapacity):
					recordEvidenceDrop(s, "low_risk_capacity_reserved")
				case errors.Is(err, ErrEvidenceVaultCapacity):
					recordEvidenceDrop(s, "vault_capacity_exhausted")
				default:
					recordEvidenceDrop(s, "vault_unavailable")
				}
				continue
			}
			candidate.EvidenceHandle, candidate.EvidenceDigest = handle, digest
			candidate.EvidenceExpiresAt = timestamppb.New(expires)
			selected = append(selected, candidate)
		}
		candidates = selected
	}
	type pair struct {
		key   reviewRouteKey
		count *reviewRouteCount
	}
	pairs := make([]pair, 0, len(s.routes))
	for key, count := range s.routes {
		pairs = append(pairs, pair{key: key, count: count})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].count.requests == pairs[j].count.requests {
			return pairs[i].key.method+" "+pairs[i].key.route < pairs[j].key.method+" "+pairs[j].key.route
		}
		return pairs[i].count.requests > pairs[j].count.requests
	})
	window := &telemetryv1.TrafficWindow{
		WindowId: reviewWindowID(s), UnitId: s.unitID, AssetId: s.assetID,
		WindowStart: timestamppb.New(s.start), WindowEnd: timestamppb.New(s.end),
		GenerationId: s.generation, GenerationSeq: s.genSeq, PolicyDigest: s.policy,
		RequestCount: s.requests, CriticalCount: s.critical, BlockedCount: s.blocked,
		ObservedCount: s.observed, IncompleteCount: s.incomplete, EvidenceDroppedCount: s.dropped,
		EvidenceDropReasons: s.dropReasons,
		Other: &telemetryv1.TrafficRouteCell{
			RequestCount: s.other.requests, CriticalCount: s.other.critical,
			BlockedCount: s.other.blocked, IncompleteCount: s.other.incomplete,
		},
		ReviewMode: c.policy.GetMode(),
	}
	limit := int(c.policy.GetTopRouteCells())
	for i, pair := range pairs {
		cell := &telemetryv1.TrafficRouteCell{Method: pair.key.method, RouteTemplate: pair.key.route,
			RequestCount: pair.count.requests, CriticalCount: pair.count.critical,
			BlockedCount: pair.count.blocked, IncompleteCount: pair.count.incomplete}
		if i < limit {
			window.RouteCells = append(window.RouteCells, cell)
			continue
		}
		window.Other.RequestCount += cell.RequestCount
		window.Other.CriticalCount += cell.CriticalCount
		window.Other.BlockedCount += cell.BlockedCount
		window.Other.IncompleteCount += cell.IncompleteCount
	}
	return window, candidates
}

func reviewWindowID(s *reviewWindowState) string {
	value := s.unitID + "\x00" + s.assetID + "\x00" + s.start.Format(time.RFC3339Nano) + "\x00" +
		s.generation + "\x00" + strconv.FormatInt(s.genSeq, 10) + "\x00" + s.policy
	sum := sha256.Sum256([]byte(value))
	return "tw-" + hex.EncodeToString(sum[:])
}
