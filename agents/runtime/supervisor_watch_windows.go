//go:build windows

package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/windows"
)

func watchSupervisorPlatform(int) error {
	pid, err := strconv.ParseUint(strings.TrimSpace(os.Getenv(envSupervisorPID)), 10, 32)
	if err != nil || pid == 0 {
		return fmt.Errorf("failed_precondition: supervisor process identity is required")
	}
	process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return fmt.Errorf("open supervisor process: %w", err)
	}
	go func() {
		defer windows.CloseHandle(process) //nolint:errcheck // 监督进程退出后释放等待句柄。
		if _, err := windows.WaitForSingleObject(process, windows.INFINITE); err == nil {
			terminateOnSupervisorLoss()
		}
	}()
	return nil
}

func watchCancellationPlatform(_ int, cancel context.CancelFunc) error {
	if cancel == nil {
		return errors.New("failed_precondition: cancellation callback is required")
	}
	name := strings.TrimSpace(os.Getenv(envCancelEvent))
	if name == "" {
		return errors.New("failed_precondition: cancellation event is required")
	}
	namePointer, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	event, err := windows.OpenEvent(windows.SYNCHRONIZE, false, namePointer)
	if err != nil {
		return fmt.Errorf("open cancellation event: %w", err)
	}
	go func() {
		defer windows.CloseHandle(event) //nolint:errcheck // 取消事件触发后释放等待句柄。
		if _, err := windows.WaitForSingleObject(event, windows.INFINITE); err == nil {
			cancel()
		}
	}()
	return nil
}

func terminateOnSupervisorLoss() {
	os.Exit(1)
}
