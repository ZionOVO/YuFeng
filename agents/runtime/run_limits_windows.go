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
	restricted, err := windowsTokenHasRestrictedPrivileges(token)
	if err != nil {
		return fmt.Errorf("verify restricted token: %w", err)
	}
	if !restricted {
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

func windowsTokenHasRestrictedPrivileges(token windows.Token) (bool, error) {
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return false, err
	}
	isAdministrator, err := token.IsMember(administrators)
	if err != nil {
		return false, err
	}
	if isAdministrator {
		return false, nil
	}

	var size uint32
	err = windows.GetTokenInformation(token, windows.TokenPrivileges, nil, 0, &size)
	if !errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) {
		return false, err
	}
	buffer := make([]byte, size)
	if err := windows.GetTokenInformation(token, windows.TokenPrivileges, &buffer[0], size, &size); err != nil {
		return false, err
	}
	name, err := windows.UTF16PtrFromString("SeChangeNotifyPrivilege")
	if err != nil {
		return false, err
	}
	var traversal windows.LUID
	if err := windows.LookupPrivilegeValue(nil, name, &traversal); err != nil {
		return false, err
	}
	privileges := (*windows.Tokenprivileges)(unsafe.Pointer(&buffer[0]))
	for _, privilege := range privileges.AllPrivileges() {
		if privilege.Luid != traversal {
			return false, nil
		}
	}
	return true, nil
}

func applyPlatformResourceLimits(ResourceLimit) error {
	return limitCurrentProcess()
}
