//go:build !windows

package runtime

import (
	"context"
	"fmt"
	"os"
	"syscall"
)

func watchSupervisorPlatform(fd int) error {
	if fd < 3 {
		return fmt.Errorf("failed_precondition: supervisor liveness descriptor is required")
	}
	file := os.NewFile(uintptr(fd), "supervisor-liveness")
	if file == nil {
		return fmt.Errorf("failed_precondition: supervisor liveness descriptor is required")
	}
	go func() {
		var one [1]byte
		for {
			if _, err := file.Read(one[:]); err != nil {
				terminateOnSupervisorLoss()
				return
			}
		}
	}()
	return nil
}

func watchCancellationPlatform(fd int, cancel context.CancelFunc) error {
	if fd < 3 || cancel == nil {
		return fmt.Errorf("failed_precondition: cancellation descriptor is required")
	}
	file := os.NewFile(uintptr(fd), "run-cancellation")
	if file == nil {
		return fmt.Errorf("failed_precondition: cancellation descriptor is required")
	}
	go func() {
		defer file.Close() //nolint:errcheck // 只读通知管道在 goroutine 退出时释放。
		var one [1]byte
		if _, err := file.Read(one[:]); err != nil {
			cancel()
		}
	}()
	return nil
}

func terminateOnSupervisorLoss() {
	_ = syscall.Kill(0, syscall.SIGKILL)
}
