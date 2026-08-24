package brain

import (
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"

	"yufeng/lib/kernel"
)

func TestParseDualTokensTable(t *testing.T) {
	cases := []struct {
		name    string
		auth    string
		cap     string
		wantErr connect.Code
	}{
		{name: "both missing", wantErr: connect.CodeUnauthenticated},
		{name: "cap missing", auth: "Bearer access", wantErr: connect.CodeUnauthenticated},
		{name: "access missing", cap: "Bearer cap", wantErr: connect.CodeUnauthenticated},
		{name: "bad scheme", auth: "Token a", cap: "Bearer c", wantErr: connect.CodeUnauthenticated},
		{name: "ok", auth: "Bearer access", cap: "Bearer cap", wantErr: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			if tc.auth != "" {
				h.Set("Authorization", tc.auth)
			}
			if tc.cap != "" {
				h.Set(CapabilityHeader, tc.cap)
			}
			got, err := ParseDualTokens(h)
			if tc.wantErr != 0 {
				if connect.CodeOf(err) != tc.wantErr {
					t.Fatalf("code=%v err=%v", connect.CodeOf(err), err)
				}
				return
			}
			if err != nil || got.Access != "access" || got.Capability != "cap" {
				t.Fatalf("got=%+v err=%v", got, err)
			}
		})
	}
}

func TestBindDualTokensSubAzp(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	raw, err := kernel.SignCapabilityToken(kernel.Claims{
		Subject: "run-1", AuthorizedParty: "worker-1", Audience: "tools",
		TokenID: "jti-azp", ExpiresAt: now.Add(time.Minute).Unix(), IssuedAt: now.Unix(),
	}, priv)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := kernel.VerifyCapabilityToken(raw, pub, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := BindDualTokens("worker-1", claims); err != nil {
		t.Fatal(err)
	}
	if err := BindDualTokens("other", claims); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("mismatch want permission_denied, got %v", err)
	}
	if err := BindDualTokens("", claims); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("empty access want unauthenticated, got %v", err)
	}
}

func TestBindDualTokensExpiredCapability(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	raw, err := kernel.SignCapabilityToken(kernel.Claims{
		Subject: "a", AuthorizedParty: "a", Audience: "tools",
		TokenID: "jti-exp", ExpiresAt: now.Add(-time.Minute).Unix(), IssuedAt: now.Add(-2 * time.Minute).Unix(),
	}, priv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.VerifyCapabilityToken(raw, pub, now); err == nil {
		t.Fatal("expired capability must fail")
	}
}
