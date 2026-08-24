package runtime

import (
	"context"
	"os/exec"
)

// VerifiedSandboxCapabilities 返回当前主机实际可启用的调查沙箱能力。
// 返回空集合时监督进程仍可注册，但服务端不得向它租赁调查工作。
func VerifiedSandboxCapabilities() []string {
	return verifiedSandboxCapabilities()
}

// ApplyInvestigationSandbox 在短命进程读取冻结输入后收紧当前进程。
// 调查进程必须由 Hatch 以 Sandbox 启动；缺少平台能力时失败关闭。
func ApplyInvestigationSandbox() error {
	return applyInvestigationSandbox()
}

func sandboxCommand(ctx context.Context, bin string, env []string, args ...string) *exec.Cmd {
	return platformSandboxCommand(ctx, bin, env, args...)
}
