package kernel

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"
)

func TestCapabilityTokenRoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	raw, err := SignCapabilityToken(Claims{
		Subject:   "agent-1",
		Role:      "orchestrator",
		Audience:  "tools",
		ExpiresAt: now.Add(time.Minute).Unix(),
		NotBefore: now.Add(-time.Second).Unix(),
		IssuedAt:  now.Unix(),
		TokenID:   "jti-1",
		Scopes:    []string{"console:read"},
		Tools:     []string{"report.create"},
		MaxCalls:  10,
		Bindings:  []string{"finding-1"},
	}, priv)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := VerifyCapabilityToken(raw, pub, now)
	if err != nil {
		t.Fatalf("验签失败: %v", err)
	}
	if claims.Subject != "agent-1" || claims.Role != "orchestrator" || claims.TokenID != "jti-1" {
		t.Fatalf("声明不完整: %+v", claims)
	}
	if claims.Issuer != KeyID(pub) {
		t.Fatalf("issuer = %s, want %s", claims.Issuer, KeyID(pub))
	}
}

func TestCapabilityTokenRejectsBadInput(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	signed := func(jti string) string {
		raw, err := SignCapabilityToken(Claims{
			Subject: "agent-1", Audience: "tools", ExpiresAt: now.Add(time.Minute).Unix(), TokenID: jti,
		}, priv)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	t.Run("缺少 jti 拒绝签发", func(t *testing.T) {
		if _, err := SignCapabilityToken(Claims{ExpiresAt: now.Unix() + 60}, priv); err == nil {
			t.Fatal("缺 jti 不应签发")
		}
	})
	t.Run("错误公钥拒绝", func(t *testing.T) {
		other, _, _ := ed25519.GenerateKey(rand.Reader)
		if _, err := VerifyCapabilityToken(signed("jti-2"), other, now); err == nil {
			t.Fatal("错误公钥不应通过")
		}
	})
	t.Run("篡改载荷拒绝", func(t *testing.T) {
		raw := signed("jti-3")
		parts := strings.Split(raw, ".")
		parts[1] = parts[1][:len(parts[1])-1] + "A"
		if _, err := VerifyCapabilityToken(strings.Join(parts, "."), pub, now); err == nil {
			t.Fatal("篡改载荷不应通过")
		}
	})
	t.Run("过期拒绝", func(t *testing.T) {
		raw, err := SignCapabilityToken(Claims{
			Subject: "agent-1", Audience: "tools", TokenID: "jti-4", ExpiresAt: now.Add(-time.Minute).Unix(),
		}, priv)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyCapabilityToken(raw, pub, now); err == nil {
			t.Fatal("过期令牌不应通过")
		}
	})
}
