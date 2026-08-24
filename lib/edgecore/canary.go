package edgecore

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
)

// RequestID 是边缘生成的请求关联标识，进入遥测 request_id 并参与 canary 分桶。
type RequestID = string

// NewRequestID 生成 128 位随机十六进制请求标识。
// 必须由边缘生成：客户端可控的 x-request-id 会被攻击者用来稳定选择 canary 桶外。
func NewRequestID() (RequestID, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate request id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// CanarySelected 保留演示分桶（按 request_id）。生产请用 CanarySelectedUnit。
func CanarySelected(requestID RequestID, percent int32) bool {
	return canaryBucket([]byte(requestID), percent)
}

// CanarySelectedUnit 是生产分桶：sha256(unit_id || release_id)，禁止 request_id。
func CanarySelectedUnit(unitID, releaseID string, percent int32) bool {
	return canaryBucket([]byte(unitID+"\x00"+releaseID), percent)
}

func canaryBucket(key []byte, percent int32) bool {
	if percent <= 0 {
		return false
	}
	if percent >= 100 {
		return true
	}
	sum := sha256.Sum256(key)
	bucket := binary.BigEndian.Uint64(sum[:8]) % 10000
	return bucket < uint64(percent)*100
}
