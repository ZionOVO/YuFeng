package main

import (
	"strings"
	"testing"

	"yufeng/agents/modelgateway"
)

func TestProductionJarvisForbidsModelURL(t *testing.T) {
	_, err := selectProvider(providerConfig{
		BrainURL: "https://brain:9050",
		ModelURL: "https://brain:9050",
		Token:    func() string { return "tok" },
	})
	if err == nil || !strings.Contains(err.Error(), "model-url") {
		t.Fatalf("production must reject -model-url even when it points at brain: %v", err)
	}
}

func TestProductionJarvisUsesGenerate(t *testing.T) {
	p, err := selectProvider(providerConfig{
		BrainURL: "https://brain:9050",
		Token:    func() string { return "tok" },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.(*modelgateway.GenerateProvider); !ok {
		t.Fatalf("production provider must be Generate, got %T", p)
	}
}

func TestDevInsecureWithoutDirectModelStillUsesGenerate(t *testing.T) {
	p, err := selectProvider(providerConfig{DevInsecure: true, BrainURL: "http://127.0.0.1:9050"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.(*modelgateway.GenerateProvider); !ok {
		t.Fatalf("dev-insecure must not substitute fixed model output, got %T", p)
	}
}

func TestValidateJarvisTransportFailsClosed(t *testing.T) {
	if err := validateJarvisTransport("https://brain:9050", false, "ca", "cert", "key"); err != nil {
		t.Fatalf("production HTTPS transport must be accepted: %v", err)
	}
	if err := validateJarvisTransport("http://brain:9050", false, "", "", ""); err == nil {
		t.Fatal("production Jarvis must reject plaintext brain transport")
	}
	if err := validateJarvisTransport("http://127.0.0.1:9050", true, "", "", ""); err != nil {
		t.Fatalf("explicit development mode may use plaintext transport: %v", err)
	}
	if err := validateJarvisTransport("https://brain:9050", false, "ca", "", "key"); err == nil {
		t.Fatal("production HTTPS transport must require complete client TLS material")
	}
}
