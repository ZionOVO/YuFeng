package runtime

import (
	"errors"
	"strings"
	"time"

	"yufeng/lib/kernel"
	eventv1 "yufeng/proto/gen/eventv1"
	workerv1 "yufeng/proto/gen/workerv1"
)

const investigationPurpose = "investigation"
const trafficCasePurpose = "traffic_case"

// WorkInput 是 yufeng-agentd 经本地监督套接字交给短命执行实例的类型化输入。
type WorkInput struct {
	Purpose                string               `json:"purpose,omitempty"`
	Ticket                 *eventv1.CheckTicket `json:"ticket,omitempty"`
	TicketDigest           string               `json:"ticket_digest,omitempty"`
	ClusterID              string               `json:"cluster_id,omitempty"`
	CaseID                 string               `json:"case_id,omitempty"`
	ReviewCandidateID      string               `json:"review_candidate_id,omitempty"`
	SensitiveContentRef    string               `json:"sensitive_content_ref,omitempty"`
	EvidenceApprovalID     string               `json:"evidence_approval_id,omitempty"`
	SensitiveContentDigest string               `json:"sensitive_content_digest,omitempty"`
	SensitiveMaxBytes      int64                `json:"sensitive_max_bytes,omitempty"`
	SensitiveExpiresAt     time.Time            `json:"sensitive_expires_at,omitempty"`
	ThreadID               string               `json:"thread_id,omitempty"`
	TurnID                 string               `json:"turn_id,omitempty"`
	StepID                 string               `json:"step_id,omitempty"`
	GenerationID           string               `json:"generation_id,omitempty"`
	ExpectedItemSequence   int64                `json:"expected_item_sequence,omitempty"`
	LeaseID                string               `json:"lease_id,omitempty"`
	LeaseEpoch             int64                `json:"lease_epoch,omitempty"`
}

// WorkInputFromProto 把网络工作项收窄成本地输入；普通执行实例返回零值。
func WorkInputFromProto(input *workerv1.InvestigationInput) WorkInput {
	if input == nil {
		return WorkInput{}
	}
	purpose := investigationPurpose
	if input.GetCaseId() != "" {
		purpose = trafficCasePurpose
	}
	out := WorkInput{Purpose: purpose, Ticket: input.GetTicket(), TicketDigest: input.GetTicketDigest(), ClusterID: input.GetClusterId(),
		CaseID: input.GetCaseId(), ReviewCandidateID: input.GetReviewCandidateId(), SensitiveContentRef: input.GetSensitiveContentRef(),
		EvidenceApprovalID: input.GetEvidenceApprovalId(), SensitiveContentDigest: input.GetSensitiveContentDigest(), SensitiveMaxBytes: input.GetSensitiveMaxBytes()}
	if input.GetSensitiveExpiresAt() != nil && input.GetSensitiveExpiresAt().IsValid() {
		out.SensitiveExpiresAt = input.GetSensitiveExpiresAt().AsTime()
	}
	return out
}

// IsInvestigation 报告输入是否属于只读调查执行实例。
func (i WorkInput) IsInvestigation() bool {
	return i.Purpose == investigationPurpose || i.Purpose == trafficCasePurpose
}

// IsTrafficCase 报告输入是否属于案件敏感流量调查。
func (i WorkInput) IsTrafficCase() bool { return i.Purpose == trafficCasePurpose }

// Validate 校验本地调查输入的必要坐标。
func (i WorkInput) Validate() error {
	if i.Purpose == "" {
		if i.Ticket != nil || i.TicketDigest != "" || i.ClusterID != "" || i.CaseID != "" || i.SensitiveContentRef != "" {
			return errors.New("work input purpose is missing")
		}
		return nil
	}
	if !i.IsInvestigation() {
		return errors.New("work input purpose is invalid")
	}
	if i.IsTrafficCase() {
		if strings.TrimSpace(i.CaseID) == "" || strings.TrimSpace(i.SensitiveContentRef) == "" ||
			strings.TrimSpace(i.EvidenceApprovalID) == "" || strings.TrimSpace(i.SensitiveContentDigest) == "" ||
			i.SensitiveMaxBytes <= 0 || !i.SensitiveExpiresAt.After(time.Now()) || strings.TrimSpace(i.ThreadID) == "" ||
			strings.TrimSpace(i.TurnID) == "" || strings.TrimSpace(i.StepID) == "" || i.ExpectedItemSequence <= 0 ||
			strings.TrimSpace(i.LeaseID) == "" || i.LeaseEpoch <= 0 {
			return errors.New("traffic case investigation input is incomplete")
		}
		return nil
	}
	if i.Ticket == nil || strings.TrimSpace(i.Ticket.GetEventId()) == "" || strings.TrimSpace(i.TicketDigest) == "" {
		return errors.New("investigation input is incomplete")
	}
	digest, err := kernel.CheckTicketDigest(i.Ticket)
	if err != nil || digest != i.TicketDigest {
		return errors.New("investigation ticket digest mismatch")
	}
	return nil
}

// InvestigationOutputDigest 返回调查只读结果摘要序列的确定性摘要。
func InvestigationOutputDigest(reads []*workerv1.InvestigationToolRead) string {
	return kernel.InvestigationOutputDigest(reads)
}

// ValidateInvestigationReceipt 校验成功回执只能描述当前票据的只读调用。
func ValidateInvestigationReceipt(input WorkInput, receipt *workerv1.InvestigationReceipt) error {
	return kernel.ValidateInvestigationReceipt(&workerv1.InvestigationInput{
		Ticket: input.Ticket, TicketDigest: input.TicketDigest, ClusterId: input.ClusterID,
	}, receipt)
}
