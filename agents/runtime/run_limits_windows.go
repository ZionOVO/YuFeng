//go:build windows

package runtime

import (
	"errors"
	"fmt"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var isProcessInJob = windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")

const windowsJobAttachTimeout = 5 * time.Second

func limitCurrentProcess() error {
	token := windows.GetCurrentProcessToken()
	restricted, err := token.IsRestricted()
	if err != nil || !restricted {
		return errors.New("failed_precondition: restricted token is required")
	}
	deadline := time.Now().Add(windowsJobAttachTimeout)
	for {
		var inJob int32
		result, _, callErr := isProcessInJob.Call(uintptr(windows.CurrentProcess()), 0, uintptr(unsafe.Pointer(&inJob)))
		if result == 0 {
			if callErr == nil {
				callErr = syscall.EINVAL
			}
			return fmt.Errorf("query job object: %w", callErr)
		}
		if inJob != 0 {
			return nil
		}
		if !time.Now().Before(deadline) {
			return errors.New("failed_precondition: job object is required")
		}
		time.Sleep(time.Millisecond)
	}
}

func applyPlatformResourceLimits(ResourceLimit) error {
	return limitCurrentProcess()
}
