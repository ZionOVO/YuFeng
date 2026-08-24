package kernel

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"google.golang.org/protobuf/proto"

	eventv1 "yufeng/proto/gen/eventv1"
)

// CheckTicketDigest 返回不可变检查票据的确定性内容摘要。
func CheckTicketDigest(ticket *eventv1.CheckTicket) (string, error) {
	if ticket == nil {
		return "", errors.New("check ticket is nil")
	}
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(ticket)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
