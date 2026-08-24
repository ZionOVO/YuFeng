package deploy

import (
	"os"
	"strings"
	"testing"
)

func TestExternalAgentdInstallersUseRestartSafeEncryptedActivation(t *testing.T) {
	unixInstallers := []string{"agentd/install-linux.sh", "agentd/install-macos.sh"}
	for _, path := range unixInstallers {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(raw)
		for _, want := range []string{"-enroll", "-activate", "worker-refresh", "enrollment.json"} {
			if !strings.Contains(text, want) {
				t.Errorf("%s missing restart-safe activation contract %q", path, want)
			}
		}
		if strings.Count(text, "worker-refresh") < 2 || strings.Count(text, "enrollment.json") < 2 ||
			!strings.Contains(text, "&& [ ! -s") {
			t.Errorf("%s must not submit a second enrollment after receipt or refresh persistence", path)
		}
		for _, forbidden := range []string{"-activation-package=", "activation=$4", `install -m 0600 "$activation"`} {
			if strings.Contains(text, forbidden) {
				t.Errorf("%s still requires plaintext activation material %q", path, forbidden)
			}
		}
	}

	windowsRaw, err := os.ReadFile("agentd/Install-Windows.ps1")
	if err != nil {
		t.Fatal(err)
	}
	windows := string(windowsRaw)
	for _, want := range []string{"-enroll", "-activate", "worker-refresh", "enrollment.json"} {
		if !strings.Contains(windows, want) {
			t.Errorf("Windows installer missing restart-safe activation contract %q", want)
		}
	}
	if !strings.Contains(windows, "-not (Test-Path -LiteralPath $enrollmentReceipt") ||
		!strings.Contains(windows, "-not (Test-Path -LiteralPath $refreshState") {
		t.Error("Windows installer must not submit a second enrollment after receipt or refresh persistence")
	}
	for _, forbidden := range []string{"$ActivationPackage", "installedActivation", "-activation-package="} {
		if strings.Contains(windows, forbidden) {
			t.Errorf("Windows installer still requires plaintext activation material %q", forbidden)
		}
	}
}

func TestExternalAgentdRunbookNeverExportsDecryptedActivation(t *testing.T) {
	raw, err := os.ReadFile("agentd/README.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{"X25519", "加密激活包", "-activate", "worker-refresh"} {
		if !strings.Contains(text, want) {
			t.Errorf("external agentd runbook missing %q", want)
		}
	}
	for _, forbidden := range []string{"下载一次性激活包", "activation.json", "/path/activation.json", "ActivationPackage"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("external agentd runbook exposes obsolete plaintext flow %q", forbidden)
		}
	}
}
