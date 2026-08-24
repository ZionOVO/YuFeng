package kernel

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// 能力令牌使用 JSON Web Token 编码：
//   - 头部固定 {"alg":"EdDSA","typ":"JWT"}；
//   - 载荷为 Claims 的 JavaScript 对象表示法紧凑编码；
//   - 签名为 Ed25519(header.payload) 的 base64url 编码。
//
// [能力令牌]: ../../docs/glossary.md#capability-token

const capabilityTokenHeader = `{"alg":"EdDSA","typ":"JWT"}`

// SignCapabilityToken 用治理内核私钥签发能力令牌。
// Claims 的 Issuer 若为空，自动填入公钥指纹；TokenID 若为空则拒绝——jti 只标识本次签发实例。
func SignCapabilityToken(claims Claims, key ed25519.PrivateKey) (string, error) {
	if len(key) != ed25519.PrivateKeySize {
		return "", errors.New("invalid private key length")
	}
	if strings.TrimSpace(claims.TokenID) == "" {
		return "", errors.New("capability token jti is required")
	}
	if claims.ExpiresAt == 0 {
		return "", errors.New("capability token exp is required")
	}
	if claims.NotBefore == 0 {
		claims.NotBefore = time.Now().Unix()
	}
	if claims.IssuedAt == 0 {
		claims.IssuedAt = time.Now().Unix()
	}
	if claims.Issuer == "" {
		claims.Issuer = KeyID(key.Public().(ed25519.PublicKey))
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(capabilityTokenHeader))
	payloadBytes, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("encode capability claims: %w", err)
	}
	payload := base64.RawURLEncoding.EncodeToString(payloadBytes)
	signingInput := header + "." + payload
	sig := ed25519.Sign(key, []byte(signingInput))
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// VerifyCapabilityToken 验证签名、时间窗与签发者，返回解析后的声明。
// 调用方还应继续校验 Tools/Bindings/MaxCalls 与当前请求的匹配关系。
func VerifyCapabilityToken(raw string, pub ed25519.PublicKey, now time.Time) (Claims, error) {
	var zero Claims
	if len(pub) != ed25519.PublicKeySize {
		return zero, errors.New("invalid public key length")
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return zero, errors.New("capability token must have three segments")
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return zero, fmt.Errorf("decode token header: %w", err)
	}
	if string(headerBytes) != capabilityTokenHeader {
		return zero, errors.New("unsupported capability token header")
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return zero, fmt.Errorf("decode token payload: %w", err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return zero, fmt.Errorf("decode token signature: %w", err)
	}
	if !ed25519.Verify(pub, []byte(parts[0]+"."+parts[1]), sig) {
		return zero, errors.New("capability token signature verification failed")
	}
	var claims Claims
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return zero, fmt.Errorf("decode capability claims: %w", err)
	}
	if claims.Issuer != KeyID(pub) {
		return zero, errors.New("capability token issuer does not match public key")
	}
	unix := now.Unix()
	if unix < claims.NotBefore {
		return zero, errors.New("capability token is not active yet")
	}
	if unix >= claims.ExpiresAt {
		return zero, errors.New("capability token has expired")
	}
	if strings.TrimSpace(claims.TokenID) == "" {
		return zero, errors.New("capability token jti is empty")
	}
	return claims, nil
}
