//go:build windows

package runtime

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var isProcessInJob = windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")

func limitCurrentProcess() error {
	token := windows.GetCurrentProcessToken()
	restricted, err := token.IsRestricted()
	if err != nil || !restricted {
		return errors.New("failed_precondition: restricted token is required")
	}
	var inJob int32
	result, _, callErr := isProcessInJob.Call(uintptr(windows.CurrentProcess()), 0, uintptr(unsafe.Pointer(&inJob)))
	if result == 0 {
		if callErr == nil {
			callErr = syscall.EINVAL
		}
		return fmt.Errorf("query job object: %w", callErr)
	}
	if inJob == 0 {
		return errors.New("failed_precondition: job object is required")
	}
	return nil
}

func applyPlatformResourceLimits(ResourceLimit) error {
	return limitCurrentProcess()
}
