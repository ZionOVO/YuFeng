//go:build darwin

package runtime

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestDarwinInvestigationSandboxFailsClosed(t *testing.T) {
	if os.Getenv("YUFENG_DARWIN_SANDBOX_HELPER") == "1" {
		runDarwinSandboxProbe()
		return
	}
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := platformSandboxCommand(ctx, bin, nil, "-test.run=TestDarwinInvestigationSandboxFailsClosed")
	cmd.Env = append(os.Environ(), "YUFENG_DARWIN_SANDBOX_HELPER=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox probe: %v: %s", err, output)
	}
	if strings.TrimSpace(string(output)) != "sandbox-ok" {
		t.Fatalf("sandbox probe output = %q", output)
	}
}

func runDarwinSandboxProbe() {
	fail := func(reason string) {
		_, _ = fmt.Fprintln(os.Stderr, reason)
		os.Exit(1)
	}
	if _, err := os.ReadFile("/etc/passwd"); err == nil {
		fail("sandbox allowed out-of-scope read")
	}
	if err := os.WriteFile(os.TempDir()+"/yufeng-sandbox-probe", []byte("forbidden"), 0o600); err == nil {
		fail("sandbox allowed out-of-scope write")
	}
	if listener, err := net.Listen("tcp", "127.0.0.1:0"); err == nil {
		_ = listener.Close()
		fail("sandbox allowed network socket")
	}
	if err := exec.Command("/usr/bin/true").Run(); err == nil {
		fail("sandbox allowed derived process execution")
	}
	_, _ = fmt.Fprintln(os.Stdout, "sandbox-ok")
	os.Exit(0)
}
