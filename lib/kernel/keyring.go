package kernel

import (
	"crypto/ed25519"
	"errors"

	artifactv1 "yufeng/proto/gen/artifactv1"
)

// VerifyRing 是密钥轮换后的验签环：旧公钥继续验旧制品，新制品用当前签发钥。
type VerifyRing struct {
	pubs []ed25519.PublicKey
}

// NewVerifyRing 构造验签环；至少一把公钥。
func NewVerifyRing(pubs ...ed25519.PublicKey) (*VerifyRing, error) {
	var kept []ed25519.PublicKey
	for _, p := range pubs {
		if len(p) == ed25519.PublicKeySize {
			kept = append(kept, p)
		}
	}
	if len(kept) == 0 {
		return nil, errors.New("verify ring is empty")
	}
	return &VerifyRing{pubs: kept}, nil
}

// Verify 用环内任一公钥验签；全部失败才拒绝。
func (r *VerifyRing) Verify(a *artifactv1.Artifact) error {
	if r == nil {
		return errors.New("verify ring is nil")
	}
	var last error
	for _, p := range r.pubs {
		if err := VerifyArtifact(a, p); err == nil {
			return nil
		} else {
			last = err
		}
	}
	if last == nil {
		return errors.New("artifact signature is invalid")
	}
	return last
}
