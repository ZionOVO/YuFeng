package edgecore

import (
	"strings"

	artifactv1 "yufeng/proto/gen/artifactv1"
	commonv1 "yufeng/proto/gen/commonv1"
)

// Detection 是同步发现（不含拦截裁决）。
type Detection struct {
	InspectorID    string
	RuleID         string
	Class          commonv1.AttackClass
	Score          float64
	Location       commonv1.InspectionSurface
	Selector       string
	Phase          string
	Version        string
	ManifestDigest string
	ProfileDigest  string
}

// Inspection 是 Evaluate 的输出：只含发现与覆盖度。
type Inspection struct {
	Detections []Detection
	Coverage   []Coverage
	Rejected   bool
}

// EvaluateView 只出发现与覆盖度，不给出 block。
func EvaluateView(view CanonicalView, detections []Detection) Inspection {
	return Inspection{Detections: detections, Coverage: view.Coverage, Rejected: view.Rejected}
}

// CoverageError 把检查面标成 ERROR，不得记成无发现。
func CoverageError(surface commonv1.InspectionSurface) Coverage {
	return Coverage{Target: surface, Status: commonv1.CoverageStatus_COVERAGE_STATUS_ERROR}
}

// IsNoDetection 在覆盖度为 ERROR 时为假。
func IsNoDetection(cov []Coverage, found []Detection) bool {
	for _, c := range cov {
		if c.Status == commonv1.CoverageStatus_COVERAGE_STATUS_ERROR {
			return false
		}
	}
	return len(found) == 0
}

// ShouldSkipBodyPolicy 在 body 非 FULL 且策略依赖 body 时跳过。
func ShouldSkipBodyPolicy(view CanonicalView) bool {
	st := CoverageOf(view.Coverage, commonv1.InspectionSurface_INSPECTION_SURFACE_BODY)
	return st != commonv1.CoverageStatus_COVERAGE_STATUS_FULL
}

// GateOrder 按检测键策略 → 演示 KIND_RULE → 形状规则裁决。
func GateOrder(policyBlock, ruleBlock, shapeBlock bool) Action {
	if policyBlock {
		return ActionBlock
	}
	if ruleBlock {
		return ActionBlock
	}
	if shapeBlock {
		return ActionBlock
	}
	return ActionAllow
}

// PolicyCandidateBlocks 按范围 ∧ 检测键 ∧ 覆盖度判定策略是否应拦截。引擎命中本身不拦截。
func PolicyCandidateBlocks(cand *artifactv1.PolicyCandidate, found []Detection, view CanonicalView) bool {
	return PolicyCandidateBlocksOn(cand, found, view, Request{})
}

// PolicyCandidateApplies 判断请求是否落在候选策略的确定性范围内。
// 发布前回放用它排除范围外样本，避免把“本策略不覆盖”误记为漏拦。
func PolicyCandidateApplies(cand *artifactv1.PolicyCandidate, req Request, view CanonicalView) bool {
	return cand != nil && PolicyScopeMatches(cand.GetScope(), req, view)
}

// PolicyCandidateBlocksOn 带请求身份做范围匹配。
func PolicyCandidateBlocksOn(cand *artifactv1.PolicyCandidate, found []Detection, view CanonicalView, req Request) bool {
	if cand == nil || cand.Predicate == nil {
		return false
	}
	if cand.Action == "log" {
		return false
	}
	if !PolicyScopeMatches(cand.Scope, req, view) {
		return false
	}
	if cand.Predicate.MinAnomalyScore > 0 && maxDetectionScore(found) < cand.Predicate.MinAnomalyScore {
		return false
	}
	requireFull := cand.Predicate.CoverageRequirement == commonv1.CoverageStatus_COVERAGE_STATUS_FULL
	hit := false
	for _, key := range cand.Predicate.DetectionKeys {
		if key == nil {
			continue
		}
		if PolicyMatchesDetectionKey(found, key, requireFull, view.Coverage) {
			hit = true
			break
		}
	}
	if cand.Predicate.RequireMatchPresent || len(cand.Predicate.DetectionKeys) > 0 {
		if !cand.Predicate.RequireMatchPresent && requireFull {
			return !hit
		}
		return hit
	}
	return false
}

// PolicyScopeMatches 判定策略范围是否覆盖本次请求。空字段不限制。
func PolicyScopeMatches(scope *artifactv1.PolicyScope, req Request, view CanonicalView) bool {
	if scope == nil {
		return true
	}
	if scope.AssetId != "" && req.AssetID != "" && scope.AssetId != req.AssetID {
		return false
	}
	if len(scope.Hosts) > 0 {
		host := view.Host
		if host == "" && req.Headers != nil {
			host = req.Headers["host"]
			if host == "" {
				host = req.Headers["Host"]
			}
		}
		if i := strings.IndexByte(host, ':'); i >= 0 {
			host = host[:i]
		}
		if !stringInFold(scope.Hosts, host) {
			return false
		}
	}
	path := view.Path
	if path == "" {
		path = req.Path
	}
	if scope.RouteTemplate != "" && !routeTemplateMatch(scope.RouteTemplate, path) {
		return false
	}
	if scope.PathPrefix != "" && !strings.HasPrefix(path, scope.PathPrefix) {
		return false
	}
	method := view.Method
	if method == "" {
		method = strings.ToUpper(req.Method)
	}
	if len(scope.Methods) > 0 && !stringInFold(scope.Methods, method) {
		return false
	}
	if len(scope.ContentTypes) > 0 {
		ct := headerFirst(view, req, "content-type")
		if i := strings.IndexByte(ct, ';'); i >= 0 {
			ct = strings.TrimSpace(ct[:i])
		}
		if !stringInFold(scope.ContentTypes, ct) {
			return false
		}
	}
	for _, sel := range scope.TargetSelectors {
		if _, ok := selectorValue(sel, req, view); !ok {
			return false
		}
	}
	return true
}

func headerFirst(view CanonicalView, req Request, name string) string {
	if vs := view.Headers[httpCanonical(name)]; len(vs) > 0 {
		return vs[0]
	}
	if req.Headers != nil {
		if v := req.Headers[name]; v != "" {
			return v
		}
		if v := req.Headers[httpCanonical(name)]; v != "" {
			return v
		}
	}
	return ""
}

func maxDetectionScore(found []Detection) float64 {
	var max float64
	for _, d := range found {
		if d.Score > max {
			max = d.Score
		}
	}
	return max
}

// PolicyMatchesKey 按规则标识与检查面匹配（测试与旧调用方）。
func PolicyMatchesKey(found []Detection, wantRule string, loc commonv1.InspectionSurface, requireFull bool, cov []Coverage) bool {
	return PolicyMatchesDetectionKey(found, &commonv1.DetectionKey{RuleId: wantRule, TargetLocation: loc}, requireFull, cov)
}

// PolicyMatchesDetectionKey 按完整检测键匹配；未填字段为通配。
func PolicyMatchesDetectionKey(found []Detection, key *commonv1.DetectionKey, requireFull bool, cov []Coverage) bool {
	if key == nil {
		return false
	}
	loc := key.TargetLocation
	if loc != commonv1.InspectionSurface_INSPECTION_SURFACE_UNSPECIFIED {
		if requireFull && CoverageOf(cov, loc) != commonv1.CoverageStatus_COVERAGE_STATUS_FULL {
			return false
		}
		if loc == commonv1.InspectionSurface_INSPECTION_SURFACE_BODY && CoverageOf(cov, loc) == commonv1.CoverageStatus_COVERAGE_STATUS_ABSENT {
			return false
		}
	}
	for _, d := range found {
		if detectionMatchesKey(d, key) {
			return true
		}
	}
	return false
}

func detectionMatchesKey(d Detection, key *commonv1.DetectionKey) bool {
	if key.RuleId != "" && d.RuleID != key.RuleId {
		return false
	}
	if key.DetectorId != "" && d.InspectorID != key.DetectorId {
		return false
	}
	if key.DetectorVersion != "" && d.Version != key.DetectorVersion {
		return false
	}
	if key.DetectorManifestDigest != "" && d.ManifestDigest != key.DetectorManifestDigest {
		return false
	}
	if key.Phase != "" && d.Phase != key.Phase {
		return false
	}
	if key.TargetLocation != commonv1.InspectionSurface_INSPECTION_SURFACE_UNSPECIFIED && d.Location != key.TargetLocation {
		return false
	}
	if key.TargetSelector != "" && d.Selector != key.TargetSelector {
		return false
	}
	if key.NormalizationProfileDigest != "" && d.ProfileDigest != key.NormalizationProfileDigest {
		return false
	}
	return key.RuleId != "" || key.DetectorId != ""
}

// AutoCanaryAllowed 在绑定单元数不足以形成非 0/100 分桶时禁止自动 canary。
func AutoCanaryAllowed(boundUnits int, percent int32) bool {
	if percent <= 0 {
		return false
	}
	need := int((100 + percent - 1) / percent)
	return boundUnits >= need
}
