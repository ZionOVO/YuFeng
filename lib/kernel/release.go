package kernel

import (
	"crypto/ed25519"
	"errors"
	"fmt"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	artifactv1 "yufeng/proto/gen/artifactv1"
	commonv1 "yufeng/proto/gen/commonv1"
)

// 发布生命周期类型状态机：draft → signed → shadow → canary → enforce → retired。
// 单机单元不足时另有合法边 shadow → enforce（PromoteEnforce）。
// 非法转换在编译期不可达——每个状态都是独立类型，推进方法只存在于合法源状态。
//
// [发布]: ../../docs/glossary.md#release

// Release 是所有生命周期状态的公共观察接口。
type Release interface {
	ReleaseID() string
	State() commonv1.ReleaseState
	Artifact() *artifactv1.Artifact
}

// Draft 是提案阶段：无签名、无最终 artifact_id。
type Draft struct {
	ID        string
	Envelope  *artifactv1.Artifact
	CreatedBy string
}

// Signed 是门禁通过后的已签名状态：回放报告已写入，artifact_id 已定。
type Signed struct {
	ID       string
	Envelope *artifactv1.Artifact
}

// Shadow 是只记录不生效的观察状态。
type Shadow struct {
	ID       string
	Envelope *artifactv1.Artifact
}

// Canary 是小比例生效状态。
type Canary struct {
	ID            string
	Envelope      *artifactv1.Artifact
	CanaryPercent int32
}

// Enforce 是全量生效状态。
type Enforce struct {
	ID       string
	Envelope *artifactv1.Artifact
}

// Retired 是终态。
type Retired struct {
	ID       string
	Envelope *artifactv1.Artifact
	Reason   commonv1.RetireReason
}

// ReleaseID 返回草稿的发布标识。
func (d *Draft) ReleaseID() string { return d.ID }

// State 返回草稿发布状态。
func (d *Draft) State() commonv1.ReleaseState { return commonv1.ReleaseState_RELEASE_STATE_DRAFT }

// Artifact 返回草稿持有的制品信封。
func (d *Draft) Artifact() *artifactv1.Artifact { return d.Envelope }

// ReleaseID 返回已签名发布的标识。
func (s *Signed) ReleaseID() string { return s.ID }

// State 返回已签名发布状态。
func (s *Signed) State() commonv1.ReleaseState { return commonv1.ReleaseState_RELEASE_STATE_SIGNED }

// Artifact 返回已签名发布持有的制品信封。
func (s *Signed) Artifact() *artifactv1.Artifact { return s.Envelope }

// ReleaseID 返回观察发布的标识。
func (s *Shadow) ReleaseID() string { return s.ID }

// State 返回观察发布状态。
func (s *Shadow) State() commonv1.ReleaseState { return commonv1.ReleaseState_RELEASE_STATE_SHADOW }

// Artifact 返回观察发布持有的制品信封。
func (s *Shadow) Artifact() *artifactv1.Artifact { return s.Envelope }

// ReleaseID 返回小比例发布的标识。
func (c *Canary) ReleaseID() string { return c.ID }

// State 返回小比例发布状态。
func (c *Canary) State() commonv1.ReleaseState { return commonv1.ReleaseState_RELEASE_STATE_CANARY }

// Artifact 返回小比例发布持有的制品信封。
func (c *Canary) Artifact() *artifactv1.Artifact { return c.Envelope }

// ReleaseID 返回全量生效发布的标识。
func (e *Enforce) ReleaseID() string { return e.ID }

// State 返回全量生效发布状态。
func (e *Enforce) State() commonv1.ReleaseState { return commonv1.ReleaseState_RELEASE_STATE_ENFORCE }

// Artifact 返回全量生效发布持有的制品信封。
func (e *Enforce) Artifact() *artifactv1.Artifact { return e.Envelope }

// ReleaseID 返回已退役发布的标识。
func (r *Retired) ReleaseID() string { return r.ID }

// State 返回已退役发布状态。
func (r *Retired) State() commonv1.ReleaseState { return commonv1.ReleaseState_RELEASE_STATE_RETIRED }

// Artifact 返回已退役发布持有的制品信封。
func (r *Retired) Artifact() *artifactv1.Artifact { return r.Envelope }

// NewDraft 构造提案。门禁前的制品必须无签名且 id 为空。
func NewDraft(releaseID string, a *artifactv1.Artifact, createdBy string) (*Draft, error) {
	if releaseID == "" {
		return nil, errors.New("release id is empty")
	}
	if a == nil {
		return nil, errors.New("artifact is nil")
	}
	if a.Signature != nil && len(a.Signature.Sig) > 0 {
		return nil, errors.New("draft artifact must not be signed")
	}
	if a.Id != "" {
		return nil, errors.New("draft artifact id must be empty before gate")
	}
	if a.ReplayReport != nil {
		return nil, errors.New("draft artifact must not carry replay report")
	}
	return &Draft{ID: releaseID, Envelope: proto.Clone(a).(*artifactv1.Artifact), CreatedBy: createdBy}, nil
}

// GateResult 是门禁动作的结果：通过则 Signed 非空，失败则 Draft 非空并保留报告。
type GateResult struct {
	Passed bool
	Draft  *Draft
	Signed *Signed
	Report *artifactv1.ReplayReport
}

// GatePassed 是门禁通过判据的唯一出处（回放器与门禁共用，禁止两地维护）：
// 至少拦住一条恶意样本，且良性与管理面样本零误拦。
// 只看计数，不看报告自带的 Passed 标志——标志由本谓词产出，不能再作为输入。
func GatePassed(report *artifactv1.ReplayReport) bool {
	return report.MaliciousTotal > 0 &&
		report.MaliciousBlocked == report.MaliciousTotal &&
		report.BenignBlocked == 0 &&
		report.ManagementBlocked == 0
}

// Gate 在副本上执行门禁判定；通过时写入回放报告、计算全信封 id 并签名。
func (d *Draft) Gate(report *artifactv1.ReplayReport, key ed25519.PrivateKey) (GateResult, error) {
	if report == nil {
		return GateResult{}, errors.New("replay report is nil")
	}
	res := GateResult{Report: proto.Clone(report).(*artifactv1.ReplayReport)}
	passed := report.Passed && GatePassed(report)
	if !passed {
		draft, err := NewDraft(d.ID, d.Envelope, d.CreatedBy)
		if err != nil {
			return res, err
		}
		draft.Envelope.ReplayReport = res.Report
		res.Draft = draft
		return res, nil
	}
	envelope := proto.Clone(d.Envelope).(*artifactv1.Artifact)
	envelope.ReplayReport = res.Report
	envelope.CreatedBy = d.CreatedBy
	if envelope.CreatedAt == nil {
		envelope.CreatedAt = timestamppb.Now()
	}
	if err := SignArtifact(envelope, key); err != nil {
		return res, fmt.Errorf("sign gated artifact: %w", err)
	}
	res.Passed = true
	res.Signed = &Signed{ID: d.ID, Envelope: envelope}
	return res, nil
}

// GateWithSigner 与 Gate 相同，但经 Signer 签发（生产套接字）。
func (d *Draft) GateWithSigner(report *artifactv1.ReplayReport, s Signer) (GateResult, error) {
	if report == nil {
		return GateResult{}, errors.New("replay report is nil")
	}
	res := GateResult{Report: proto.Clone(report).(*artifactv1.ReplayReport)}
	passed := report.Passed && GatePassed(report)
	if !passed {
		draft, err := NewDraft(d.ID, d.Envelope, d.CreatedBy)
		if err != nil {
			return res, err
		}
		draft.Envelope.ReplayReport = res.Report
		res.Draft = draft
		return res, nil
	}
	envelope := proto.Clone(d.Envelope).(*artifactv1.Artifact)
	envelope.ReplayReport = res.Report
	envelope.CreatedBy = d.CreatedBy
	if envelope.CreatedAt == nil {
		envelope.CreatedAt = timestamppb.Now()
	}
	if err := SignArtifactWithSigner(envelope, s); err != nil {
		return res, fmt.Errorf("sign gated artifact: %w", err)
	}
	res.Passed = true
	res.Signed = &Signed{ID: d.ID, Envelope: envelope}
	return res, nil
}

// StartShadow 推进到影子状态。
func (s *Signed) StartShadow() *Shadow {
	return &Shadow{ID: s.ID, Envelope: proto.Clone(s.Envelope).(*artifactv1.Artifact)}
}

// PromoteCanary 推进到小比例状态。
func (s *Shadow) PromoteCanary(percent int32) (*Canary, error) {
	if percent < CanaryPercentMin || percent > CanaryPercentMax {
		return nil, fmt.Errorf("canary percent %d out of range [%d,%d]", percent, CanaryPercentMin, CanaryPercentMax)
	}
	return &Canary{ID: s.ID, Envelope: proto.Clone(s.Envelope).(*artifactv1.Artifact), CanaryPercent: percent}, nil
}

// PromoteEnforce 是单元数不足以分桶时的直达边：shadow → enforce。
// 常见路径仍是 Canary.PromoteEnforce；本方法不是第二条状态机。
func (s *Shadow) PromoteEnforce() *Enforce {
	return &Enforce{ID: s.ID, Envelope: proto.Clone(s.Envelope).(*artifactv1.Artifact)}
}

// PromoteEnforce 推进到全量状态。
func (c *Canary) PromoteEnforce() *Enforce {
	return &Enforce{ID: c.ID, Envelope: proto.Clone(c.Envelope).(*artifactv1.Artifact)}
}

// Active 表示可以回滚或退休的非终态发布。
type Active interface {
	Release
}

// RetireActive 把 shadow/canary/enforce 退到终态。
func RetireActive(active Active, reason commonv1.RetireReason) (*Retired, error) {
	switch reason {
	case commonv1.RetireReason_RETIRE_REASON_ROLLBACK,
		commonv1.RetireReason_RETIRE_REASON_MANUAL,
		commonv1.RetireReason_RETIRE_REASON_TTL,
		commonv1.RetireReason_RETIRE_REASON_SUPERSEDED:
	default:
		return nil, errors.New("unknown retire reason")
	}
	return &Retired{
		ID:       active.ReleaseID(),
		Envelope: proto.Clone(active.Artifact()).(*artifactv1.Artifact),
		Reason:   reason,
	}, nil
}

// ActiveOf 把具体状态收窄为 Active 接口，便于统一回滚/退休。
func ActiveOf(r Release) (Active, error) {
	switch r.State() {
	case commonv1.ReleaseState_RELEASE_STATE_SHADOW,
		commonv1.ReleaseState_RELEASE_STATE_CANARY,
		commonv1.ReleaseState_RELEASE_STATE_ENFORCE:
		return r.(Active), nil
	default:
		return nil, fmt.Errorf("release %s in state %s is not active", r.ReleaseID(), r.State())
	}
}
