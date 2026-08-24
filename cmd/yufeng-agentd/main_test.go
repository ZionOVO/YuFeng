package main

import "testing"

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
