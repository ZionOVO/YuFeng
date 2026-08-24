package runtime

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ResourceLimit 是执行实例子进程的资源上限。
type ResourceLimit struct {
	MemoryBytes uint64
	CPUSeconds  uint64
	Files       uint64
}

// HatchConfig 是孵化 yufeng-run 执行实例的参数。
type HatchConfig struct {
	Bin        string
	Args       []string
	Env        []string
	TTL        time.Duration
	Budget     *CallBudget
	Limits     ResourceLimit
	Sandbox    bool
	WorkDir    string
	ExtraFiles []*os.File
}

// HatchResult 是一次孵化的结果。
type HatchResult struct {
	PID             int
	Err             error
	Record          RunRecord
	EnvKeys         []string
	TerminalKind    string
	TerminalPayload string
}

// Hatch 孵化子进程：调用前扣预算，存活时限到期时终止进程组，监督取消时不残留子进程。
// 能力令牌禁止进入子进程环境；Limits 经非密钥环境交给子进程 LimitResources。
// 取消先发 SIGTERM 以便补偿，超时再 SIGKILL。
func Hatch(ctx context.Context, cfg HatchConfig) HatchResult {
	var out HatchResult
	if cfg.Budget != nil {
		if err := cfg.Budget.Consume(); err != nil {
			out.Err = err
			out.Record.Events = append(out.Record.Events, "budget")
			return out
		}
	}
	if cfg.Bin == "" {
		out.Err = fmt.Errorf("run binary is empty")
		return out
	}
	if envHasSecret(cfg.Env) {
		out.Err = fmt.Errorf("run environment must not contain capability or access tokens")
		return out
	}
	ttl := cfg.TTL
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, ttl)
	defer cancel()
	cmd := exec.CommandContext(runCtx, cfg.Bin, cfg.Args...)
	if cfg.Sandbox {
		cmd = sandboxCommand(runCtx, cfg.Bin, cfg.Env, cfg.Args...)
	}
	if cfg.Env != nil {
		cmd.Env = StripSecrets(cfg.Env)
	} else {
		cmd.Env = StripSecrets(os.Environ())
	}
	if extra := limitEnv(cfg.Limits); len(extra) > 0 {
		cmd.Env = append(cmd.Env, extra...)
	}
	if cfg.Sandbox {
		cmd.Env = append(cmd.Env, "YUFENG_INVESTIGATION_SANDBOX=required")
	}
	if cfg.WorkDir != "" {
		cmd.Dir = cfg.WorkDir
	}
	if len(cfg.ExtraFiles) > 0 {
		cmd.ExtraFiles = cfg.ExtraFiles
	}
	processSetupCleanup, err := configureChildProcess(cmd)
	if err != nil {
		closeExtra(cfg.ExtraFiles)
		out.Err = err
		return out
	}
	cmd.Cancel = func() error { return terminateChildProcess(cmd.Process) }
	cmd.WaitDelay = time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		processSetupCleanup()
		closeExtra(cfg.ExtraFiles)
		out.Err = err
		return out
	}
	processSetupCleanup()
	processTreeCleanup, err := attachChildProcess(cmd.Process, cfg.Limits)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		closeExtra(cfg.ExtraFiles)
		out.Err = err
		return out
	}
	defer processTreeCleanup()
	closeExtra(cfg.ExtraFiles)
	out.PID = cmd.Process.Pid
	out.Record.Events = append(out.Record.Events, "start")
	err = cmd.Wait()
	if runCtx.Err() != nil && ctx.Err() == nil {
		out.Err = context.DeadlineExceeded
		out.Record.Events = append(out.Record.Events, "ttl")
		_ = KillProcessGroup(out.PID)
		return out
	}
	if ctx.Err() != nil {
		out.Err = ctx.Err()
		out.Record.Events = append(out.Record.Events, "supervisor")
		_ = KillProcessGroup(out.PID)
		return out
	}
	if err != nil {
		out.Err = err
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			if len(msg) > 2048 {
				msg = msg[:2048]
			}
			out.Err = fmt.Errorf("%w: %s", err, msg)
		}
		out.Record.Events = append(out.Record.Events, "fail")
		return out
	}
	out.Record.Events = append(out.Record.Events, "ok")
	return out
}

func closeExtra(files []*os.File) {
	for _, f := range files {
		if f != nil {
			_ = f.Close()
		}
	}
}

func defaultRunWorkDir() string { return os.TempDir() }

// KillProcessGroup 杀掉 pid 所在进程组，避免监督退出后子进程残留。
func KillProcessGroup(pid int) error {
	return killProcessTree(pid)
}

// ProcessAlive 判断进程是否仍在（信号 0）。
func ProcessAlive(pid int) bool {
	return processAlive(pid)
}

// ExceedsLimit 在步骤声明用量超过上限时失败。
func ExceedsLimit(used, max uint64) bool {
	return max > 0 && used > max
}
