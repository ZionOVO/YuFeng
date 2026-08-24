package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yufeng/lib/edgecore"
)

// runDemo 是行为入口：生成的公钥与签名制品必须能被数据面直接装载验签。
func TestRunDemo(t *testing.T) {
	dir := t.TempDir()
	if err := runDemo([]string{"-out", dir}); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "pubkey.hex"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	pub := ed25519.PublicKey(b)

	a, err := edgecore.LoadArtifact(filepath.Join(dir, "artifacts", "demo-rules.json"), pub)
	if err != nil {
		t.Fatalf("装载并验签演示制品: %v", err)
	}
	rules, err := edgecore.ParseRules(a.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 3 {
		t.Fatalf("演示规则数 = %d, want 3", len(rules))
	}
}

func TestPolicyEnforceRequiresArgs(t *testing.T) {
	if err := runPolicyEnforce(nil); err == nil {
		t.Fatal("policy-enforce without args must fail")
	}
	if err := runRetire(nil); err == nil {
		t.Fatal("retire without args must fail")
	}
}

func TestTLSWritesBundle(t *testing.T) {
	dir := t.TempDir()
	if err := runTLS([]string{"-out", dir}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ca.crt", "server.crt", "server.key", "client.crt", "client.key", "client-public.pem"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	first, err := os.ReadFile(filepath.Join(dir, "ca.crt"))
	if err != nil {
		t.Fatal(err)
	}
	if err := runTLS([]string{"-out", dir}); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(filepath.Join(dir, "ca.crt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("second yfctl tls must keep existing ca")
	}
}

func TestRunDemoKeepsExistingKeys(t *testing.T) {
	dir := t.TempDir()
	if err := runDemo([]string{"-out", dir}); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(filepath.Join(dir, "pubkey.hex"))
	if err != nil {
		t.Fatal(err)
	}
	if err := runDemo([]string{"-out", dir}); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(filepath.Join(dir, "pubkey.hex"))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("second yfctl demo must keep existing pubkey")
	}
}

func TestRunKeysCreatesOnlyStableSigningMaterial(t *testing.T) {
	dir := t.TempDir()
	if err := runKeys([]string{"-out", dir}); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(filepath.Join(dir, "signing.key.hex"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "pubkey.hex")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "artifacts")); !os.IsNotExist(err) {
		t.Fatal("production key initialization must not create demo artifacts")
	}
	if err := runKeys([]string{"-out", dir}); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(filepath.Join(dir, "signing.key.hex"))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("production key initialization must be idempotent")
	}
}

func TestRunKeysMigratesLegacySigningKey(t *testing.T) {
	dir := t.TempDir()
	_, legacy, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	legacyRaw := hex.EncodeToString(legacy) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "dev.key.hex"), []byte(legacyRaw), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runKeys([]string{"-out", dir}); err != nil {
		t.Fatal(err)
	}
	migrated, err := os.ReadFile(filepath.Join(dir, "signing.key.hex"))
	if err != nil {
		t.Fatal(err)
	}
	if string(migrated) != legacyRaw {
		t.Fatal("production key initialization must preserve the legacy signing key")
	}
	wantPublic := hex.EncodeToString(legacy.Public().(ed25519.PublicKey)) + "\n"
	public, err := os.ReadFile(filepath.Join(dir, "pubkey.hex"))
	if err != nil {
		t.Fatal(err)
	}
	if string(public) != wantPublic {
		t.Fatal("production public key must derive from the migrated legacy signing key")
	}

	_, anotherLegacy, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dev.key.hex"), []byte(hex.EncodeToString(anotherLegacy)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runKeys([]string{"-out", dir}); err != nil {
		t.Fatal(err)
	}
	current, err := os.ReadFile(filepath.Join(dir, "signing.key.hex"))
	if err != nil {
		t.Fatal(err)
	}
	if string(current) != legacyRaw {
		t.Fatal("existing production signing key must take precedence over the legacy file")
	}
}

func TestRunKeysRejectsInvalidLegacySigningKey(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "dev.key.hex"), []byte("invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runKeys([]string{"-out", dir}); err == nil {
		t.Fatal("invalid legacy signing key must fail closed")
	}
	if _, err := os.Stat(filepath.Join(dir, "signing.key.hex")); !os.IsNotExist(err) {
		t.Fatal("invalid legacy signing key must not be replaced")
	}
}
