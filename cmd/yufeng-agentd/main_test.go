package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateAgentdTransportFailsClosed(t *testing.T) {
	if err := validateAgentdTransport("https://brain:9050", false, "ca", "cert", "key"); err != nil {
		t.Fatalf("production HTTPS transport must be accepted: %v", err)
	}
	if err := validateAgentdTransport("http://brain:9050", false, "", "", ""); err == nil {
		t.Fatal("production agentd must reject plaintext brain transport")
	}
	if err := validateAgentdTransport("http://127.0.0.1:9050", true, "", "", ""); err != nil {
		t.Fatalf("explicit development mode may use plaintext transport: %v", err)
	}
	if err := validateAgentdTransport("brain:9050", true, "", "", ""); err == nil {
		t.Fatal("brain URL without an HTTP scheme must fail at startup")
	}
	if err := validateAgentdTransport("https://brain:9050", false, "ca", "cert", ""); err == nil {
		t.Fatal("production agentd must require complete client TLS material")
	}
}

func TestResolveWorkerPublicKeyReadsMountedRegistrationMaterial(t *testing.T) {
	dir := t.TempDir()
	mounted := filepath.Join(dir, "mounted-public.pem")
	if err := os.WriteFile(mounted, []byte("  mounted-public-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := resolveWorkerPublicKey("", mounted, dir)
	if err != nil {
		t.Fatalf("resolve mounted public key: %v", err)
	}
	if got != "mounted-public-key" {
		t.Fatalf("public key = %q, want mounted material", got)
	}
	if _, err := resolveWorkerPublicKey("inline", mounted, dir); err == nil {
		t.Fatal("inline public key and mounted public key file must be mutually exclusive")
	}
}

func TestResolveWorkerPublicKeyFallsBackToPrivateState(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(workerPublicKeyPath(dir), []byte("state-public-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := resolveWorkerPublicKey("", "", dir)
	if err != nil {
		t.Fatalf("resolve state public key: %v", err)
	}
	if got != "state-public-key" {
		t.Fatalf("public key = %q, want private state material", got)
	}
}
