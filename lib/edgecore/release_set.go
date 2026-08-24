package edgecore

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	artifactv1 "yufeng/proto/gen/artifactv1"
	commonv1 "yufeng/proto/gen/commonv1"

	"yufeng/lib/kernel"
)

// defaultReleaseTTL 是制品未携带 CreatedAt/Ttl 时的兜底有效期。
// 兜底方向必须收紧而非放宽：无限期会让撤销语义失效，中台签发的
// 制品应始终携带 Ttl，此值只防演示与手造数据。
const defaultReleaseTTL = 24 * time.Hour

// ReleaseObservation 是一次请求在单个发布上的裁决轨迹。
type ReleaseObservation struct {
	ReleaseID      string
	ArtifactID     string
	Mode           commonv1.ReleaseMode
	CanaryPercent  int32
	CanarySelected bool
	Matched        bool
}

// Decision 是一次请求的最终裁决。
type Decision struct {
	Action           Action
	WouldHaveBlocked bool
	Verdicts         []Verdict
	Observations     []ReleaseObservation
	Detections       []Detection
	Inspection       Inspection
	Posture          commonv1.IngressPosture
	GenerationID     string
	GenerationSeq    int64
}

// ReleaseCounter 是心跳上报用的单调计数快照。
type ReleaseCounter struct {
	ReleaseID           string
	ArtifactID          string
	Mode                commonv1.ReleaseMode
	RequestsTotal       uint64
	BlocksTotal         uint64
	ObserveTotal        uint64
	CanarySelectedTotal uint64
	P99Micros           uint64
}

type managedRelease struct {
	releaseID     string
	artifactID    string
	detector      *RuleDetector
	prefix        string
	policy        *artifactv1.PolicyCandidate
	shape         *compiledShape
	mode          commonv1.ReleaseMode
	canaryPercent int32
	expiresAt     time.Time
}

func (r managedRelease) ruleMatch(req Request) (string, bool) {
	if r.detector == nil {
		return "", false
	}
	if r.prefix != "" && !strings.HasPrefix(req.Path, r.prefix) {
		return "", false
	}
	return r.detector.Match(req)
}

// counter 用原子计数：请求路径每 release 递增，避免逐条目拿锁。
type counter struct {
	requests  atomic.Uint64
	blocks    atomic.Uint64
	observe   atomic.Uint64
	canarySel atomic.Uint64
	latMu     sync.Mutex
	latencies []uint64
}

// ReleaseSet 按 release 持有闸侧制品，并按世代清单选装同步眼睛。
// 所有方法并发安全。
type ReleaseSet struct {
	mu             sync.RWMutex
	releases       map[string]managedRelease
	counters       map[string]*counter
	inspectors     []Inspector
	inspectorByRel map[string]string
	mapper         *artifactv1.TaxonomyMapper
	profile        *artifactv1.HttpInspectionProfile
	digest         *artifactv1.EvidenceDigest
	evidencePol    *artifactv1.EvidencePolicy
	forward        *artifactv1.ForwardPolicy
	reviewPolicy   *artifactv1.TrafficReviewPolicy
	reviewDigest   string
	modelProfile   *artifactv1.ModelProfile
	modelDigest    string
	posture        commonv1.IngressPosture
	listen         *artifactv1.UnitListenPlan
	activeGen      *artifactv1.AssetGeneration
	edgeVersion    string
}

// NewReleaseSet 构造空制品集。
func NewReleaseSet(edgeVersion ...string) *ReleaseSet {
	version := kernel.MinimumEdgeVersion
	if len(edgeVersion) > 0 && strings.TrimSpace(edgeVersion[0]) != "" {
		version = strings.TrimSpace(edgeVersion[0])
	}
	return &ReleaseSet{
		releases:       map[string]managedRelease{},
		counters:       map[string]*counter{},
		inspectorByRel: map[string]string{},
		posture:        commonv1.IngressPosture_INGRESS_POSTURE_REVERSE_PROXY,
		edgeVersion:    version,
	}
}

// SetPosture 设置本进程入口姿态；形态不进资产世代。
func (s *ReleaseSet) SetPosture(p commonv1.IngressPosture) {
	s.mu.Lock()
	s.posture = ResolvePosture(p)
	s.mu.Unlock()
}

// Posture 返回当前入口姿态。
func (s *ReleaseSet) Posture() commonv1.IngressPosture {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return ResolvePosture(s.posture)
}

// ApplyListenPlan 验签并按单调版本原子替换单元监听计划。
// persist 在激活前落盘；失败时保留当前计划。
func (s *ReleaseSet) ApplyListenPlan(plan *artifactv1.UnitListenPlan, pub ed25519.PublicKey, unitID string, persist ...func(*artifactv1.UnitListenPlan, *artifactv1.UnitListenPlan) error) error {
	if err := kernel.VerifyUnitListenPlan(plan, pub); err != nil {
		return err
	}
	if err := ValidateUnitListenPlan(plan); err != nil {
		return err
	}
	if strings.TrimSpace(unitID) == "" || plan.UnitId != unitID {
		return errors.New("unit listen plan target does not match this unit")
	}
	s.mu.RLock()
	var current *artifactv1.UnitListenPlan
	if s.listen != nil {
		current = proto.Clone(s.listen).(*artifactv1.UnitListenPlan)
	}
	s.mu.RUnlock()
	if current != nil {
		if plan.Version == current.Version && proto.Equal(plan, current) {
			return nil
		}
		if plan.Version <= current.Version {
			return errors.New("unit listen plan version must increase")
		}
		if plan.Version != current.Version+1 {
			return errors.New("unit listen plan version must not skip")
		}
	}
	if len(persist) > 0 && persist[0] != nil {
		if err := persist[0](current, plan); err != nil {
			return fmt.Errorf("persist unit listen plan before activation: %w", err)
		}
	}
	s.mu.Lock()
	s.listen = proto.Clone(plan).(*artifactv1.UnitListenPlan)
	s.posture = plan.Posture
	s.mu.Unlock()
	return nil
}

// CurrentListenPlan 返回当前已激活单元监听计划的副本。
func (s *ReleaseSet) CurrentListenPlan() *artifactv1.UnitListenPlan {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.listen == nil {
		return nil
	}
	return proto.Clone(s.listen).(*artifactv1.UnitListenPlan)
}

// Mapper 返回当前已装载的分类映射器。
func (s *ReleaseSet) Mapper() *artifactv1.TaxonomyMapper {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.mapper
}

// ForwardKind 返回世代转发策略（缺省等于今日打分路径）。
func (s *ReleaseSet) ForwardKind() commonv1.ForwardPolicyKind {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.forward == nil {
		return DefaultForwardKind(0)
	}
	return DefaultForwardKind(s.forward.Kind)
}

// EvidencePolicy 返回世代内证据可见档；缺省视为 home。
func (s *ReleaseSet) EvidencePolicy() *artifactv1.EvidencePolicy {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.evidencePol
}

// TrafficReviewPolicy 返回世代内已签名流量审查策略；未配置时返回 nil 以保留旧单元语义。
func (s *ReleaseSet) TrafficReviewPolicy() *artifactv1.TrafficReviewPolicy {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.reviewPolicy == nil {
		return nil
	}
	return proto.Clone(s.reviewPolicy).(*artifactv1.TrafficReviewPolicy)
}

// TrafficReviewPolicyDigest 返回当前签名策略制品的内容地址。
func (s *ReleaseSet) TrafficReviewPolicyDigest() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.reviewDigest
}

// ModelProfile 返回当前世代内已验签的模型档案及其制品内容地址。
// 返回值是副本，供请求壳构造本地旁路输入；该档案没有同步 Gate 权限。
func (s *ReleaseSet) ModelProfile() (*artifactv1.ModelProfile, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.modelProfile == nil {
		return nil, ""
	}
	return proto.Clone(s.modelProfile).(*artifactv1.ModelProfile), s.modelDigest
}

func (s *ReleaseSet) modelProfileSnapshot() (*artifactv1.ModelProfile, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.modelProfile, s.modelDigest
}

// Digest 返回证据摘要配置。
func (s *ReleaseSet) Digest() *artifactv1.EvidenceDigest {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.digest == nil {
		return DefaultEvidenceDigest()
	}
	return s.digest
}

// Inspectors 返回当前选装的同步眼睛（副本）。
func (s *ReleaseSet) Inspectors() []Inspector {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Inspector, len(s.inspectors))
	copy(out, s.inspectors)
	return out
}

// Apply 装载或卸载一个发布条目。retired 条目删除本地缓存；其余条目验签后覆盖。
func (s *ReleaseSet) Apply(item *artifactv1.ReleaseItem, pub ed25519.PublicKey) error {
	if item == nil || item.Artifact == nil {
		return errors.New("release item is empty")
	}
	if item.Retired {
		s.mu.Lock()
		if id, ok := s.inspectorByRel[item.ReleaseId]; ok {
			s.removeInspectorLocked(id)
			delete(s.inspectorByRel, item.ReleaseId)
		}
		delete(s.releases, item.ReleaseId)
		delete(s.counters, item.ReleaseId)
		s.mu.Unlock()
		return nil
	}
	a := item.Artifact
	if err := kernel.VerifyArtifact(a, pub); err != nil {
		return fmt.Errorf("release %s: %w", item.ReleaseId, err)
	}
	if err := s.applyMember(item, a); err != nil {
		return err
	}
	return nil
}

func (s *ReleaseSet) applyMember(item *artifactv1.ReleaseItem, a *artifactv1.Artifact) error {
	var detector *RuleDetector
	var policy *artifactv1.PolicyCandidate
	var shape *compiledShape
	switch {
	case a.Kind == artifactv1.Kind_KIND_RULE && a.PayloadSchema == RulePayloadSchema:
		rules, err := ParseRules(a.Payload)
		if err != nil {
			return err
		}
		base, err := NewRuleDetector(a.Id, rules)
		if err != nil {
			return err
		}
		detector = base
	case a.Kind == artifactv1.Kind_KIND_POLICY && a.PayloadSchema == PolicyPayloadSchema:
		var cand artifactv1.PolicyCandidate
		if err := protojson.Unmarshal(a.Payload, &cand); err != nil {
			return fmt.Errorf("release %s: policy payload: %w", item.ReleaseId, err)
		}
		if cand.Predicate == nil || len(cand.Predicate.DetectionKeys) == 0 {
			return fmt.Errorf("release %s: policy predicate detection_keys required", item.ReleaseId)
		}
		policy = &cand
	case a.Kind == artifactv1.Kind_KIND_DETECTOR_MANIFEST && (a.PayloadSchema == DetectorManifestSchema || a.PayloadSchema == ""):
		return s.installManifest(item.ReleaseId, a)
	case a.Kind == artifactv1.Kind_KIND_TAXONOMY_MAPPER && (a.PayloadSchema == TaxonomyMapperSchema || a.PayloadSchema == "taxonomy/v1"):
		var mapper artifactv1.TaxonomyMapper
		if err := protojson.Unmarshal(a.Payload, &mapper); err != nil {
			return fmt.Errorf("release %s: taxonomy payload: %w", item.ReleaseId, err)
		}
		s.mu.Lock()
		s.mapper = &mapper
		s.mu.Unlock()
		return nil
	case a.Kind == artifactv1.Kind_KIND_NORMALIZER_PROFILE:
		var prof artifactv1.HttpInspectionProfile
		if err := protojson.Unmarshal(a.Payload, &prof); err != nil {
			return fmt.Errorf("release %s: normalizer payload: %w", item.ReleaseId, err)
		}
		s.mu.Lock()
		s.profile = &prof
		s.mu.Unlock()
		return nil
	case a.Kind == artifactv1.Kind_KIND_EVIDENCE_POLICY:
		var pol artifactv1.EvidencePolicy
		if err := protojson.Unmarshal(a.Payload, &pol); err != nil {
			return fmt.Errorf("release %s: evidence policy: %w", item.ReleaseId, err)
		}
		s.mu.Lock()
		s.evidencePol = &pol
		s.mu.Unlock()
		return nil
	case a.Kind == artifactv1.Kind_KIND_EVIDENCE_DIGEST:
		var dig artifactv1.EvidenceDigest
		if err := protojson.Unmarshal(a.Payload, &dig); err != nil {
			return fmt.Errorf("release %s: evidence digest: %w", item.ReleaseId, err)
		}
		s.mu.Lock()
		s.digest = &dig
		s.mu.Unlock()
		return nil
	case a.Kind == artifactv1.Kind_KIND_FORWARD_POLICY:
		var fwd artifactv1.ForwardPolicy
		if err := protojson.Unmarshal(a.Payload, &fwd); err != nil {
			return fmt.Errorf("release %s: forward policy: %w", item.ReleaseId, err)
		}
		s.mu.Lock()
		s.forward = &fwd
		s.mu.Unlock()
		return nil
	case a.Kind == artifactv1.Kind_KIND_TRAFFIC_REVIEW_POLICY && a.PayloadSchema == TrafficReviewPolicySchema:
		var policy artifactv1.TrafficReviewPolicy
		if err := protojson.Unmarshal(a.Payload, &policy); err != nil {
			return fmt.Errorf("release %s: traffic review policy: %w", item.ReleaseId, err)
		}
		if err := ValidateTrafficReviewPolicy(&policy); err != nil {
			return fmt.Errorf("release %s: traffic review policy: %w", item.ReleaseId, err)
		}
		s.mu.Lock()
		s.reviewPolicy = &policy
		s.reviewDigest = a.GetId()
		s.mu.Unlock()
		return nil
	case a.Kind == artifactv1.Kind_KIND_MODEL_PROFILE && a.PayloadSchema == "model-profile/v1":
		var profile artifactv1.ModelProfile
		if err := protojson.Unmarshal(a.Payload, &profile); err != nil {
			return fmt.Errorf("release %s: model profile: %w", item.ReleaseId, err)
		}
		normalized, err := kernel.NormalizeModelProfile(&profile)
		if err != nil {
			return fmt.Errorf("release %s: model profile: %w", item.ReleaseId, err)
		}
		s.mu.Lock()
		s.modelProfile = normalized
		s.modelDigest = a.GetId()
		s.mu.Unlock()
		return nil
	case a.Kind == artifactv1.Kind_KIND_LISTEN_PLAN:
		return fmt.Errorf("release %s: unit listen plan requires independent unit-scoped activation", item.ReleaseId)
	case a.Kind == artifactv1.Kind_KIND_SHAPE:
		src, err := parseShapeSource(a.Payload)
		if err != nil {
			return fmt.Errorf("release %s: shape payload: %w", item.ReleaseId, err)
		}
		shape, err = compileShape(src)
		if err != nil {
			return fmt.Errorf("release %s: shape: %w", item.ReleaseId, err)
		}
	default:
		return fmt.Errorf("release %s: unsupported kind %s schema %s", item.ReleaseId, a.Kind, a.PayloadSchema)
	}
	if item.Mode == commonv1.ReleaseMode_RELEASE_MODE_CANARY {
		// 与 kernel.PromoteCanary 同一取值域：装载期再校验一次，
		// 防止中台之外的路径（手造快照、损坏缓存）把越界值放进数据面。
		if item.CanaryPercent < kernel.CanaryPercentMin || item.CanaryPercent > kernel.CanaryPercentMax {
			return fmt.Errorf("release %s: canary percent %d out of range [%d,%d]",
				item.ReleaseId, item.CanaryPercent, kernel.CanaryPercentMin, kernel.CanaryPercentMax)
		}
	}
	expires := time.Now().Add(defaultReleaseTTL)
	if a.CreatedAt != nil && a.Ttl != nil {
		expires = a.CreatedAt.AsTime().Add(a.Ttl.AsDuration())
	}
	if !expires.After(time.Now()) {
		return fmt.Errorf("release %s is expired", item.ReleaseId)
	}
	prefix := ""
	if a.Scope != nil {
		prefix = a.Scope.RouteSelector
	}
	s.mu.Lock()
	s.releases[item.ReleaseId] = managedRelease{
		releaseID: item.ReleaseId, artifactID: a.Id, detector: detector, prefix: prefix, policy: policy, shape: shape,
		mode: item.Mode, canaryPercent: item.CanaryPercent, expiresAt: expires,
	}
	if _, ok := s.counters[item.ReleaseId]; !ok {
		s.counters[item.ReleaseId] = &counter{}
	}
	s.mu.Unlock()
	return nil
}

func (s *ReleaseSet) installManifest(releaseID string, a *artifactv1.Artifact) error {
	var man artifactv1.DetectorManifest
	if err := protojson.Unmarshal(a.Payload, &man); err != nil {
		return fmt.Errorf("release %s: detector manifest: %w", releaseID, err)
	}
	ins, err := CompileInspector(&man)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if prev, ok := s.inspectorByRel[releaseID]; ok {
		s.removeInspectorLocked(prev)
	}
	s.inspectors = append(s.inspectors, ins)
	s.inspectorByRel[releaseID] = ins.ID()
	return nil
}

func (s *ReleaseSet) removeInspectorLocked(id string) {
	out := s.inspectors[:0]
	removed := false
	for _, ins := range s.inspectors {
		if !removed && ins.ID() == id {
			removed = true
			continue
		}
		out = append(out, ins)
	}
	s.inspectors = out
}

func (s *ReleaseSet) profileOrDefault() *artifactv1.HttpInspectionProfile {
	s.mu.RLock()
	p := s.profile
	s.mu.RUnlock()
	if p == nil {
		return DefaultInspectionProfile()
	}
	return p
}

// CurrentGenerationSeq 返回当前已装载世代序号；未装载为 0。
func (s *ReleaseSet) CurrentGenerationSeq() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.activeGen == nil {
		return 0
	}
	return s.activeGen.GenerationSeq
}

// CurrentGeneration 返回当前已装载世代信封。
func (s *ReleaseSet) CurrentGeneration() *artifactv1.AssetGeneration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.activeGen == nil {
		return nil
	}
	return proto.Clone(s.activeGen).(*artifactv1.AssetGeneration)
}

// Inspect 跑世代清单选装的同步眼睛，只出发现与覆盖度。
func (s *ReleaseSet) Inspect(ctx context.Context, view CanonicalView) Inspection {
	s.mu.RLock()
	inspectors := append([]Inspector(nil), s.inspectors...)
	mapper := s.mapper
	s.mu.RUnlock()
	return inspectWith(ctx, inspectors, mapper, InspectionInput{View: view})
}

func inspectWith(ctx context.Context, inspectors []Inspector, mapper *artifactv1.TaxonomyMapper, input InspectionInput) Inspection {
	view := input.View
	var parts []Inspection
	for _, ins := range inspectors {
		p, err := ins.Inspect(ctx, input)
		if err != nil {
			p.Coverage = append(append([]Coverage(nil), view.Coverage...), CoverageError(commonv1.InspectionSurface_INSPECTION_SURFACE_QUERY))
		}
		parts = append(parts, p)
	}
	out := MergeInspections(view, parts)
	out.Detections = ApplyTaxonomy(out.Detections, mapper)
	return out
}

// Replay 是不带来源元数据的回放入口：同一视图 + 同一世代 + 同一入口姿态 + 同一 unit_id ⇒ 同一发现、覆盖度与闸门动作。
func (s *ReleaseSet) Replay(view CanonicalView) (Inspection, Action) {
	dec := s.InspectThenGate(context.Background(), RequestFromView(view), "replay", view)
	return dec.Inspection, dec.Action
}

// Check 先 Inspect 再 Gate。requestID 用于 canary 分桶。
func (s *ReleaseSet) Check(ctx context.Context, req Request, requestID string) Decision {
	view := Canonicalize(req.Method, req.Path, req.Query, req.Headers, req.Body, s.profileOrDefault())
	return s.InspectThenGate(ctx, req, requestID, view)
}

// InspectThenGate 在同一份快照上先检查再裁决，避免两次读锁之间切代。
func (s *ReleaseSet) InspectThenGate(ctx context.Context, req Request, requestID string, view CanonicalView) Decision {
	s.mu.RLock()
	inspectors := append([]Inspector(nil), s.inspectors...)
	mapper := s.mapper
	releases := make([]managedRelease, 0, len(s.releases))
	counters := make(map[string]*counter, len(s.releases))
	now := time.Now()
	for id, r := range s.releases {
		if r.expiresAt.After(now) {
			releases = append(releases, r)
			counters[id] = s.counters[id]
		}
	}
	posture := ResolvePosture(s.posture)
	gen := s.activeGen
	s.mu.RUnlock()
	insp := inspectWith(ctx, inspectors, mapper, InspectionInput{View: view, ClientAddress: req.ClientAddress})
	dec := gateWith(releases, posture, req, requestID, insp, view, counters)
	if gen != nil {
		dec.GenerationID = gen.GenerationId
		dec.GenerationSeq = gen.GenerationSeq
	}
	return dec
}

// CheckWithDetections 用已有发现走闸（策略 → KIND_RULE → 形状）。
func (s *ReleaseSet) CheckWithDetections(ctx context.Context, req Request, requestID string, found []Detection, view CanonicalView) Decision {
	return s.Gate(ctx, req, requestID, found, view)
}

// Gate 唯一持有 Action。观察壳把本会 403 收成 Allow 并标 WouldHaveBlocked。
func (s *ReleaseSet) Gate(ctx context.Context, req Request, requestID string, found []Detection, view CanonicalView) Decision {
	_ = ctx
	now := time.Now()
	s.mu.RLock()
	posture := ResolvePosture(s.posture)
	releases := make([]managedRelease, 0, len(s.releases))
	counters := make(map[string]*counter, len(s.releases))
	for id, r := range s.releases {
		if r.expiresAt.After(now) {
			releases = append(releases, r)
			counters[id] = s.counters[id]
		}
	}
	s.mu.RUnlock()
	insp := Inspection{Detections: found, Coverage: view.Coverage, Rejected: view.Rejected}
	return gateWith(releases, posture, req, requestID, insp, view, counters)
}

func gateWith(releases []managedRelease, posture commonv1.IngressPosture, req Request, requestID string, insp Inspection, view CanonicalView, counters map[string]*counter) Decision {
	_ = requestID
	dec := Decision{
		Action: ActionAllow, Detections: insp.Detections, Posture: posture,
		Inspection: insp,
	}
	policyBlock, ruleBlock, shapeBlock := false, false, false
	countBlock := Intercepts(posture)
	combined := ActionAllow
	for _, r := range releases {
		var matched bool
		var v Verdict
		switch {
		case r.policy != nil:
			matched = PolicyCandidateBlocksOn(r.policy, insp.Detections, view, req)
		case r.detector != nil:
			var ruleID string
			ruleID, matched = r.ruleMatch(req)
			if matched {
				v = Verdict{DetectorID: r.artifactID, Action: ActionObserve, RuleID: ruleID, Confidence: 1, Message: "matched rule " + ruleID}
			}
		case r.shape != nil:
			matched = r.shape.Violates(req, view)
			if matched {
				v = Verdict{DetectorID: r.artifactID, Action: ActionObserve, RuleID: "shape", Confidence: 1, Message: "request shape violated"}
			}
		default:
			continue
		}
		// 金丝雀只按 unit_id 分桶；缺单元不得退化成整资产 0/100。
		selected := r.mode == commonv1.ReleaseMode_RELEASE_MODE_CANARY && req.UnitID != "" && CanarySelectedUnit(req.UnitID, r.releaseID, r.canaryPercent)
		action := actionForMode(r.mode, matched, selected)
		if r.policy != nil && action == ActionBlock {
			policyBlock = true
		}
		if r.detector != nil && action == ActionBlock {
			ruleBlock = true
		}
		if r.shape != nil && action == ActionBlock {
			shapeBlock = true
		}
		combined = worseAction(combined, action)
		if matched || action != ActionAllow {
			if v.RuleID != "" {
				dec.Verdicts = append(dec.Verdicts, v)
			}
		}
		dec.Observations = append(dec.Observations, ReleaseObservation{
			ReleaseID: r.releaseID, ArtifactID: r.artifactID, Mode: r.mode,
			CanaryPercent: r.canaryPercent, CanarySelected: selected, Matched: matched,
		})
		accounted := action
		if !countBlock && action == ActionBlock {
			accounted = ActionObserve
		}
		accountRelease(counters[r.releaseID], accounted, selected)
	}
	// 拦截只认策略 → 演示规则 → 形状；shadow / 未抽中 canary 仍要标观察。
	blocked := GateOrder(policyBlock, ruleBlock, shapeBlock)
	if blocked == ActionBlock {
		dec.Action = ActionBlock
	} else {
		dec.Action = combined
	}
	dec.WouldHaveBlocked = dec.Action == ActionBlock
	if Observes(posture) {
		dec.Action = ActionAllow
	}
	return dec
}

func actionForMode(mode commonv1.ReleaseMode, matched, selected bool) Action {
	if !matched {
		return ActionAllow
	}
	switch mode {
	case commonv1.ReleaseMode_RELEASE_MODE_ENFORCE:
		return ActionBlock
	case commonv1.ReleaseMode_RELEASE_MODE_CANARY:
		if selected {
			return ActionBlock
		}
		return ActionObserve
	default:
		return ActionObserve
	}
}

func accountRelease(c *counter, action Action, selected bool) {
	if c == nil {
		return
	}
	c.requests.Add(1)
	if action == ActionBlock {
		c.blocks.Add(1)
	}
	if action == ActionObserve {
		c.observe.Add(1)
	}
	if selected {
		c.canarySel.Add(1)
	}
}

// ApplySnapshot 完整验证后再原子替换。任一成员损坏则保留上一代。
func (s *ReleaseSet) ApplySnapshot(items []*artifactv1.ReleaseItem, pub ed25519.PublicKey) error {
	next := NewReleaseSet(s.edgeVersion)
	s.mu.RLock()
	next.posture = s.posture
	next.listen = s.listen
	s.mu.RUnlock()
	for _, item := range items {
		if item == nil {
			continue
		}
		if err := next.Apply(item, pub); err != nil {
			return err
		}
	}
	s.swapFrom(next, nil)
	return nil
}

// ApplyGeneration 验签并编译完整世代后原子装载成员。
// persist 在激活前持久化当前与新世代；失败时保留当前世代。
func (s *ReleaseSet) ApplyGeneration(gen *artifactv1.AssetGeneration, pub ed25519.PublicKey, persist ...func(*artifactv1.AssetGeneration, *artifactv1.AssetGeneration) error) error {
	store := &GenerationStore{}
	var current *artifactv1.AssetGeneration
	s.mu.RLock()
	if cur := s.activeGen; cur != nil {
		current = proto.Clone(cur).(*artifactv1.AssetGeneration)
		store.current = current
	}
	s.mu.RUnlock()
	if err := store.Load(gen, pub); err != nil {
		return err
	}
	next := NewReleaseSet(s.edgeVersion)
	s.mu.RLock()
	next.posture = s.posture
	next.listen = s.listen
	s.mu.RUnlock()
	for _, item := range gen.Members {
		if item == nil {
			continue
		}
		if err := next.Apply(item, pub); err != nil {
			return err
		}
	}
	if err := validateGenerationDependencies(gen, next); err != nil {
		return err
	}
	if len(persist) > 0 && persist[0] != nil {
		if err := persist[0](current, gen); err != nil {
			return fmt.Errorf("persist generation before activation: %w", err)
		}
	}
	s.swapFrom(next, store.Current())
	return nil
}

func validateGenerationDependencies(gen *artifactv1.AssetGeneration, next *ReleaseSet) error {
	if gen.GetMinEdgeVersion() == "" {
		return errors.New("generation min_edge_version is required")
	}
	if !edgeVersionCompatible(next.edgeVersion, gen.GetMinEdgeVersion()) {
		return fmt.Errorf("edge version %q does not satisfy minimum %q", next.edgeVersion, gen.GetMinEdgeVersion())
	}
	manifestDigests := map[string]bool{}
	profilePresent := false
	for _, item := range gen.GetMembers() {
		if item == nil || item.GetArtifact() == nil {
			continue
		}
		a := item.GetArtifact()
		switch a.GetKind() {
		case artifactv1.Kind_KIND_DETECTOR_MANIFEST:
			var manifest artifactv1.DetectorManifest
			if err := protojson.Unmarshal(a.GetPayload(), &manifest); err != nil {
				return err
			}
			if manifest.GetTarballSha256() != "" {
				manifestDigests[manifest.GetTarballSha256()] = true
			}
		case artifactv1.Kind_KIND_NORMALIZER_PROFILE:
			profilePresent = true
		}
	}
	profileDigest := ""
	if profilePresent {
		profileDigest = inspectionProfileDigest(next.profile)
	}
	for _, release := range next.releases {
		if release.policy == nil {
			continue
		}
		deps := release.policy.GetDependencies()
		if deps == nil || deps.GetDetectorManifestDigest() == "" || deps.GetNormalizerProfileDigest() == "" || deps.GetMinEdgeVersion() == "" {
			return fmt.Errorf("release %s: policy dependencies are incomplete", release.releaseID)
		}
		if !manifestDigests[deps.GetDetectorManifestDigest()] {
			return fmt.Errorf("release %s: detector manifest dependency mismatch", release.releaseID)
		}
		if profileDigest == "" || deps.GetNormalizerProfileDigest() != profileDigest {
			return fmt.Errorf("release %s: normalizer profile dependency mismatch", release.releaseID)
		}
		if deps.GetMinEdgeVersion() != gen.GetMinEdgeVersion() {
			return fmt.Errorf("release %s: minimum edge version dependency mismatch", release.releaseID)
		}
	}
	return nil
}

func edgeVersionCompatible(actual, required string) bool {
	actual = strings.TrimPrefix(strings.TrimSpace(actual), "v")
	required = strings.TrimPrefix(strings.TrimSpace(required), "v")
	if required == "dev" {
		return true
	}
	parse := func(raw string) ([3]int, bool) {
		var out [3]int
		chunks := strings.FieldsFunc(raw, func(r rune) bool { return r == '-' || r == '+' })
		if len(chunks) == 0 {
			return out, false
		}
		core := chunks[0]
		parts := strings.Split(core, ".")
		if len(parts) != len(out) {
			return out, false
		}
		for i := range parts {
			v, err := strconv.Atoi(parts[i])
			if err != nil || v < 0 {
				return out, false
			}
			out[i] = v
		}
		return out, true
	}
	a, aok := parse(actual)
	r, rok := parse(required)
	if !aok || !rok {
		return actual == required
	}
	for i := range a {
		if a[i] != r[i] {
			return a[i] > r[i]
		}
	}
	return true
}

func (s *ReleaseSet) swapFrom(next *ReleaseSet, active *artifactv1.AssetGeneration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	oldCounters := s.counters
	s.releases = next.releases
	s.inspectors = next.inspectors
	s.inspectorByRel = next.inspectorByRel
	s.mapper = next.mapper
	s.profile = next.profile
	s.digest = next.digest
	s.evidencePol = next.evidencePol
	s.forward = next.forward
	s.reviewPolicy = next.reviewPolicy
	s.reviewDigest = next.reviewDigest
	s.modelProfile = next.modelProfile
	s.modelDigest = next.modelDigest
	if active != nil {
		s.activeGen = active
	}
	s.counters = map[string]*counter{}
	for id := range s.releases {
		if c, ok := oldCounters[id]; ok {
			s.counters[id] = c
		} else {
			s.counters[id] = &counter{}
		}
	}
}

// Counters 返回单调计数快照（不清零）。
func (s *ReleaseSet) Counters() []ReleaseCounter {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ReleaseCounter, 0, len(s.releases))
	for id, r := range s.releases {
		c := s.counters[id]
		if c == nil {
			continue
		}
		out = append(out, ReleaseCounter{
			ReleaseID: id, ArtifactID: r.artifactID, Mode: r.mode,
			RequestsTotal: c.requests.Load(), BlocksTotal: c.blocks.Load(),
			ObserveTotal: c.observe.Load(), CanarySelectedTotal: c.canarySel.Load(),
			P99Micros: c.p99(),
		})
	}
	return out
}

const latencySampleCap = 256

// RecordLatency 记录一次请求在该发布上的时延，供心跳上报第 99 百分位。
func (s *ReleaseSet) RecordLatency(releaseID string, micros uint64) {
	s.mu.RLock()
	c := s.counters[releaseID]
	s.mu.RUnlock()
	if c == nil {
		return
	}
	c.latMu.Lock()
	if len(c.latencies) >= latencySampleCap {
		c.latencies = c.latencies[1:]
	}
	c.latencies = append(c.latencies, micros)
	c.latMu.Unlock()
}

func (c *counter) p99() uint64 {
	c.latMu.Lock()
	defer c.latMu.Unlock()
	n := len(c.latencies)
	if n == 0 {
		return 0
	}
	cp := append([]uint64(nil), c.latencies...)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	idx := (n * 99) / 100
	if idx >= n {
		idx = n - 1
	}
	return cp[idx]
}

func worseAction(a, b Action) Action {
	if a == ActionBlock || b == ActionBlock {
		return ActionBlock
	}
	if a == ActionObserve || b == ActionObserve {
		return ActionObserve
	}
	return ActionAllow
}
