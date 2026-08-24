package brain

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// newID 生成带前缀的随机标识。
func newID(prefix string) (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	return prefix + "_" + hex.EncodeToString(b[:]), nil
}
