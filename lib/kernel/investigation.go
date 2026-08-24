package kernel

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	workerv1 "yufeng/proto/gen/workerv1"
)

// InvestigationOutputDigest 返回调查只读结果摘要序列的确定性摘要。
func InvestigationOutputDigest(reads []*workerv1.InvestigationToolRead) string {
	h := sha256.New()
	for _, read := range reads {
		if read == nil {
			continue
		}
		_, _ = h.Write([]byte(read.GetToolName()))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(read.GetResultDigest()))
		_, _ = h.Write([]byte{0})
	}
	return fmt.Sprintf("sha256:%x", h.Sum(nil))
}

// ValidateInvestigationReceipt 校验成功回执只能描述当前票据的只读调用。
func ValidateInvestigationReceipt(input *workerv1.InvestigationInput, receipt *workerv1.InvestigationReceipt) error {
	if input == nil || input.GetTicket() == nil || input.GetTicket().GetEventId() == "" || input.GetTicketDigest() == "" {
		return errors.New("investigation input is incomplete")
	}
	digest, err := CheckTicketDigest(input.GetTicket())
	if err != nil || digest != input.GetTicketDigest() {
		return errors.New("investigation ticket digest mismatch")
	}
	if receipt == nil || receipt.GetStatus() != "succeeded" || receipt.GetEventId() != input.GetTicket().GetEventId() || receipt.GetTicketDigest() != input.GetTicketDigest() {
		return errors.New("investigation receipt does not match work input")
	}
	if receipt.GetErrorCode() != "" || receipt.GetMessageDigest() != "" {
		return errors.New("successful investigation receipt contains failure fields")
	}
	wantCluster := input.GetClusterId() != ""
	seenTicket, seenCluster := false, false
	for _, read := range receipt.GetReads() {
		if read == nil || !validInvestigationDigest(read.GetResultDigest()) {
			return errors.New("investigation read digest is invalid")
		}
		switch read.GetToolName() {
		case "ticket.get":
			if seenTicket {
				return errors.New("investigation ticket read is duplicated")
			}
			seenTicket = true
		case "cluster.get":
			if seenCluster || !wantCluster {
				return errors.New("investigation cluster read is invalid")
			}
			seenCluster = true
		default:
			return errors.New("investigation receipt contains non-read-only tool")
		}
	}
	if !seenTicket || seenCluster != wantCluster || receipt.GetOutputDigest() != InvestigationOutputDigest(receipt.GetReads()) {
		return errors.New("investigation receipt is incomplete")
	}
	return nil
}

// validInvestigationDigest 判断调查摘要是否为完整的安全哈希算法 256 位十六进制值。
func validInvestigationDigest(value string) bool {
	if len(value) != len("sha256:")+sha256.Size*2 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}
