//go:build darwin

package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const investigationSandboxProfileTemplate = `(version 1)
(deny default)
(import "system.sb")
(deny file-read*
  (subpath "/Applications")
  (subpath "/Library")
  (subpath "/Users")
  (subpath "/Volumes")
  (subpath "/opt")
  (subpath "/private/etc")
  (subpath "/private/tmp")
  (subpath "/private/var")
  (subpath "/usr/local"))
(deny network*)
(deny file-write*)
(allow process-exec (literal "%s"))
(allow file-read-data (literal "%s"))
(allow network-outbound (literal "%s"))
`

func verifiedSandboxCapabilities() []string {
	if _, err := os.Stat("/usr/bin/sandbox-exec"); err != nil {
		return nil
	}
	return []string{"sandbox_profile", "resource_limits"}
}

func applyInvestigationSandbox() error {
	// 进程已经由 sandbox-exec 按默认拒绝配置启动；进入配置后再次 Stat
	// sandbox-exec 自身会被策略拒绝，不能把该拒绝误判为未启用沙箱。
	if os.Getenv("YUFENG_INVESTIGATION_SANDBOX") != "required" {
		return errors.New("verified sandbox profile is unavailable")
	}
	return nil
}

func platformSandboxCommand(ctx context.Context, bin string, env []string, args ...string) *exec.Cmd {
	resolved, err := exec.LookPath(bin)
	if err == nil {
		bin = resolved
	}
	if canonical, err := filepath.EvalSymlinks(bin); err == nil {
		bin = canonical
	}
	profilePath := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(bin)
	brokerPath := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(environmentValue(env, envBrokerPipe))
	profile := fmt.Sprintf(investigationSandboxProfileTemplate, profilePath, profilePath, brokerPath)
	wrapped := append([]string{"-p", profile, bin}, args...)
	return exec.CommandContext(ctx, "/usr/bin/sandbox-exec", wrapped...)
}

func environmentValue(env []string, name string) string {
	for _, value := range env {
		key, content, ok := strings.Cut(value, "=")
		if ok && key == name {
			return content
		}
	}
	return ""
}
