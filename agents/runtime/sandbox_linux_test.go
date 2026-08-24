//go:build linux

package runtime

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"

	"golang.org/x/sys/unix"
)

func TestLinuxInvestigationSandboxFailsClosed(t *testing.T) {
	if os.Getenv("YUFENG_LINUX_SANDBOX_HELPER") == "1" {
		runLinuxSandboxProbe()
		return
	}
	if len(VerifiedSandboxCapabilities()) == 0 {
		t.Fatal("Linux investigation sandbox capabilities are unavailable")
	}
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "-test.run=TestLinuxInvestigationSandboxFailsClosed")
	cmd.Env = append(os.Environ(), "YUFENG_LINUX_SANDBOX_HELPER=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox probe: %v: %s", err, output)
	}
	if string(output) != "sandbox-ok\n" {
		t.Fatalf("sandbox probe output = %q", output)
	}
}

func TestLinuxSeccompAllowlistKeepsBrokerIOAndDeniesNewSockets(t *testing.T) {
	if os.Getenv("YUFENG_LINUX_SECCOMP_HELPER") == "1" {
		if err := installSeccomp(); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		assertLinuxNetworkDenied()
		_, _ = fmt.Fprintln(os.Stdout, "seccomp-ok")
		os.Exit(0)
	}
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "-test.run=TestLinuxSeccompAllowlistKeepsBrokerIOAndDeniesNewSockets")
	cmd.Env = append(os.Environ(), "YUFENG_LINUX_SECCOMP_HELPER=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("seccomp probe: %v: %s", err, output)
	}
	if string(output) != "seccomp-ok\n" {
		t.Fatalf("seccomp probe output = %q", output)
	}
}

func runLinuxSandboxProbe() {
	fail := func(reason string) {
		_, _ = fmt.Fprintln(os.Stderr, reason)
		os.Exit(1)
	}
	if err := ApplyInvestigationSandbox(); err != nil {
		fail(err.Error())
	}
	if _, err := os.ReadFile("/etc/passwd"); err == nil {
		fail("sandbox allowed out-of-scope read")
	}
	assertLinuxNetworkDenied()
	_, _ = fmt.Fprintln(os.Stdout, "sandbox-ok")
	os.Exit(0)
}

func assertLinuxNetworkDenied() {
	if fd, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM, 0); err == nil {
		_ = unix.Close(fd)
		_, _ = fmt.Fprintln(os.Stderr, "sandbox allowed network socket")
		os.Exit(1)
	} else if !errors.Is(err, unix.EPERM) {
		_, _ = fmt.Fprintln(os.Stderr, "sandbox network denial was not EPERM")
		os.Exit(1)
	}
}
