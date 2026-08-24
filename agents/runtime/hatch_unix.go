//go:build !windows

package runtime

import (
	"os"
	"os/exec"
	"syscall"
)

func configureChildProcess(cmd *exec.Cmd) (func(), error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return func() {}, nil
}

func attachChildProcess(*os.Process, ResourceLimit) (func(), error) {
	return func() {}, nil
}

func terminateChildProcess(process *os.Process) error {
	if process == nil {
		return nil
	}
	return syscall.Kill(-process.Pid, syscall.SIGTERM)
}

func killProcessTree(pid int) error {
	if pid <= 0 {
		return nil
	}
	return syscall.Kill(-pid, syscall.SIGKILL)
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}
