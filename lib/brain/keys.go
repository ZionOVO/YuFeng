package brain

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LoadOrCreateSigningKey 加载制品签名私钥；路径为空时生成一次性内存密钥。
// 生产部署必须提供持久化密钥路径；丢失密钥会使已下发制品全部失效。
func LoadOrCreateSigningKey(path string) (ed25519.PrivateKey, error) {
	if path == "" {
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, fmt.Errorf("generate ephemeral signing key: %w", err)
		}
		return priv, nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, []byte(hex.EncodeToString(priv)+"\n"), 0o600); err != nil {
			return nil, err
		}
		return priv, nil
	}
	if err != nil {
		return nil, err
	}
	priv, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("decode signing key: %w", err)
	}
	if len(priv) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("signing key length %d invalid", len(priv))
	}
	return ed25519.PrivateKey(priv), nil
}
