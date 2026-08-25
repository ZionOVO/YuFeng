//go:build windows

package runtime

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	createRestrictedToken = windows.NewLazySystemDLL("advapi32.dll").NewProc("CreateRestrictedToken")
	windowsProcessJobs    sync.Map
)

func configureChildProcess(cmd *exec.Cmd) (func(), error) {
	var source windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_DUPLICATE|windows.TOKEN_ASSIGN_PRIMARY|windows.TOKEN_QUERY, &source); err != nil {
		return func() {}, fmt.Errorf("open process token: %w", err)
	}
	defer source.Close() //nolint:errcheck // 受限令牌创建完成后释放源令牌。
	user, err := source.GetTokenUser()
	if err != nil {
		return func() {}, fmt.Errorf("read process token user: %w", err)
	}
	restrictedSID := windows.SIDAndAttributes{Sid: user.User.Sid}
	var restricted windows.Token
	const disableMaxPrivilege = 0x1
	result, _, callErr := createRestrictedToken.Call(
		uintptr(source), disableMaxPrivilege,
		0, 0, 0, 0,
		1, uintptr(unsafe.Pointer(&restrictedSID)),
		uintptr(unsafe.Pointer(&restricted)),
	)
	if result == 0 {
		if callErr == nil {
			callErr = syscall.EINVAL
		}
		return func() {}, fmt.Errorf("create restricted token: %w", callErr)
	}
	cmd.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP, Token: syscall.Token(restricted)}
	return func() { _ = restricted.Close() }, nil
}

func attachChildProcess(process *os.Process, limits ResourceLimit) (func(), error) {
	if process == nil {
		return func() {}, errors.New("child process is required")
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return func() {}, fmt.Errorf("create job object: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS
	info.BasicLimitInformation.ActiveProcessLimit = 1
	if limits.MemoryBytes > 0 {
		info.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_PROCESS_MEMORY
		info.ProcessMemoryLimit = uintptr(limits.MemoryBytes)
	}
	if limits.CPUSeconds > 0 {
		info.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_PROCESS_TIME
		info.BasicLimitInformation.PerProcessUserTimeLimit = int64(limits.CPUSeconds) * 10_000_000
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return func() {}, fmt.Errorf("limit job object: %w", err)
	}
	handle, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(process.Pid))
	if err != nil {
		_ = windows.CloseHandle(job)
		return func() {}, fmt.Errorf("open child process: %w", err)
	}
	defer windows.CloseHandle(handle) //nolint:errcheck // 分配作业对象完成后释放临时进程句柄。
	if err := windows.AssignProcessToJobObject(job, handle); err != nil {
		_ = windows.CloseHandle(job)
		return func() {}, fmt.Errorf("assign child process to job object: %w", err)
	}
	windowsProcessJobs.Store(process.Pid, job)
	return func() {
		windowsProcessJobs.Delete(process.Pid)
		_ = windows.CloseHandle(job)
	}, nil
}

func terminateChildProcess(process *os.Process) error {
	if process == nil {
		return nil
	}
	return killProcessTree(process.Pid)
}

func killProcessTree(pid int) error {
	if pid <= 0 {
		return nil
	}
	if raw, ok := windowsProcessJobs.Load(pid); ok {
		return windows.TerminateJobObject(raw.(windows.Handle), 1)
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Kill()
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle) //nolint:errcheck // 只读存活探测结束后尽力释放句柄。
	var code uint32
	if err := windows.GetExitCodeProcess(handle, &code); err != nil {
		return false
	}
	return code == 259
}
