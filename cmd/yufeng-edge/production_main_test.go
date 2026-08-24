//go:build !yufeng_dev

package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestProductionLaunchRequiresSecureBrain(t *testing.T) {
	for _, input := range []struct {
		brain            string
		localDevelopment bool
		developmentHTTP  bool
		wantError        bool
	}{
		{"", false, false, true},
		{"", true, false, true},
		{"https://brain:9050", false, false, false},
		{"https://brain:9050", true, false, true},
		{"http://brain:9050", false, false, true},
		{"http://127.0.0.1:9050", false, true, false},
		{"brain:9050", false, true, true},
	} {
		if err := validateLaunchMode(input.brain, input.localDevelopment, input.developmentHTTP); (err != nil) != input.wantError {
			t.Fatalf("validateLaunchMode(%q,%v,%v) error=%v wantError=%v", input.brain, input.localDevelopment, input.developmentHTTP, err, input.wantError)
		}
	}
}

func TestProductionSourceExcludesRuleDemo(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	root := filepath.Dir(current)
	for _, name := range []string{"main.go", "development_flags_production.go"} {
		raw, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"local-demo", "demo-rules", "loadDetectors", "KIND_RULE", "rules/v1"} {
			if strings.Contains(string(raw), forbidden) {
				t.Fatalf("production source %s contains %q", name, forbidden)
			}
		}
	}
}

func TestProductionKeyAndPseudonymFilesFailClosed(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	publicPath := filepath.Join(dir, "public.hex")
	if err := os.WriteFile(publicPath, []byte(hex.EncodeToString(private.Public().(ed25519.PublicKey))), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPubKey(publicPath); err != nil {
		t.Fatal(err)
	}
	badPublicPath := filepath.Join(dir, "bad.hex")
	if err := os.WriteFile(badPublicPath, []byte("abcd"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPubKey(badPublicPath); err == nil {
		t.Fatal("invalid public key must fail closed")
	}
	keyPath := filepath.Join(dir, "source.key")
	if err := os.WriteFile(keyPath, make([]byte, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSourcePseudonymizer(keyPath); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSourcePseudonymizer(""); err == nil {
		t.Fatal("source pseudonym key is required")
	}
}
