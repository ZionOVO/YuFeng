//go:build linux

package runtime

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	landlockAccessExecute    = 1 << 0
	landlockAccessWriteFile  = 1 << 1
	landlockAccessReadFile   = 1 << 2
	landlockAccessReadDir    = 1 << 3
	landlockAccessRemoveDir  = 1 << 4
	landlockAccessRemoveFile = 1 << 5
	landlockAccessMakeChar   = 1 << 6
	landlockAccessMakeDir    = 1 << 7
	landlockAccessMakeReg    = 1 << 8
	landlockAccessMakeSock   = 1 << 9
	landlockAccessMakeFIFO   = 1 << 10
	landlockAccessMakeBlock  = 1 << 11
	landlockAccessMakeSym    = 1 << 12
)

const landlockHandledFilesystem = landlockAccessExecute | landlockAccessWriteFile | landlockAccessReadFile |
	landlockAccessReadDir | landlockAccessRemoveDir | landlockAccessRemoveFile | landlockAccessMakeChar |
	landlockAccessMakeDir | landlockAccessMakeReg | landlockAccessMakeSock | landlockAccessMakeFIFO |
	landlockAccessMakeBlock | landlockAccessMakeSym

type landlockRulesetAttr struct {
	HandledAccessFilesystem uint64
}

func verifiedSandboxCapabilities() []string {
	if _, ok := linuxAuditArchitecture(); !ok {
		return nil
	}
	if _, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION); errno != 0 {
		return nil
	}
	if err := unix.Prctl(unix.PR_GET_SECCOMP, 0, 0, 0, 0); err != nil && !errors.Is(err, unix.EINVAL) {
		return nil
	}
	return []string{"landlock", "seccomp", "resource_limits"}
}

func applyInvestigationSandbox() error {
	if len(verifiedSandboxCapabilities()) == 0 {
		return errors.New("verified landlock and seccomp are unavailable")
	}
	if err := installLandlock(); err != nil {
		return fmt.Errorf("landlock: %w", err)
	}
	if err := installSeccomp(); err != nil {
		return fmt.Errorf("seccomp: %w", err)
	}
	return nil
}

func installLandlock() error {
	attr := landlockRulesetAttr{HandledAccessFilesystem: landlockHandledFilesystem}
	fd, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	if errno != 0 {
		return errno
	}
	defer unix.Close(int(fd)) //nolint:errcheck // 进程已受限，关闭规则集句柄不撤销限制。
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return err
	}
	_, _, errno = unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, fd, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func installSeccomp() error {
	auditArchitecture, ok := linuxAuditArchitecture()
	if !ok {
		return errors.New("unsupported syscall architecture")
	}
	allowed := []uint32{
		unix.SYS_READ, unix.SYS_WRITE, unix.SYS_CLOSE, unix.SYS_FCNTL, unix.SYS_IOCTL, unix.SYS_LSEEK,
		unix.SYS_READV, unix.SYS_WRITEV, unix.SYS_PREAD64, unix.SYS_PWRITE64, unix.SYS_FSTAT,
		unix.SYS_EXIT, unix.SYS_EXIT_GROUP, unix.SYS_FUTEX, unix.SYS_FUTEX_WAITV, unix.SYS_SET_ROBUST_LIST,
		unix.SYS_NANOSLEEP, unix.SYS_CLOCK_GETTIME, unix.SYS_CLOCK_NANOSLEEP, unix.SYS_SCHED_YIELD,
		unix.SYS_SCHED_GETAFFINITY, unix.SYS_GETPID, unix.SYS_GETPPID, unix.SYS_GETTID, unix.SYS_TGKILL,
		unix.SYS_RT_SIGACTION, unix.SYS_RT_SIGPROCMASK, unix.SYS_RT_SIGPENDING, unix.SYS_RT_SIGTIMEDWAIT,
		unix.SYS_RT_SIGRETURN, unix.SYS_SIGALTSTACK, unix.SYS_MMAP, unix.SYS_MPROTECT, unix.SYS_MUNMAP,
		unix.SYS_MREMAP, unix.SYS_MADVISE, unix.SYS_BRK, unix.SYS_GETRANDOM, unix.SYS_PRLIMIT64,
		unix.SYS_GETRLIMIT, unix.SYS_GETRUSAGE, unix.SYS_RSEQ, unix.SYS_CLONE, unix.SYS_CLONE3,
		unix.SYS_RESTART_SYSCALL, unix.SYS_EPOLL_CREATE1, unix.SYS_EPOLL_CTL, unix.SYS_EPOLL_PWAIT,
		unix.SYS_EVENTFD2, unix.SYS_PIPE2, unix.SYS_DUP, unix.SYS_DUP3, unix.SYS_PPOLL, unix.SYS_PSELECT6,
		unix.SYS_SIGNALFD4, unix.SYS_TIMERFD_CREATE, unix.SYS_TIMERFD_SETTIME, unix.SYS_TIMERFD_GETTIME,
		unix.SYS_GETITIMER, unix.SYS_SETITIMER, unix.SYS_UNAME, unix.SYS_GETTIMEOFDAY, unix.SYS_GETCPU,
		unix.SYS_SET_TID_ADDRESS, unix.SYS_GETUID, unix.SYS_GETEUID, unix.SYS_GETGID, unix.SYS_GETEGID,
		unix.SYS_RECVFROM, unix.SYS_SENDTO, unix.SYS_RECVMSG, unix.SYS_SENDMSG, unix.SYS_SHUTDOWN,
		unix.SYS_GETSOCKNAME, unix.SYS_GETPEERNAME, unix.SYS_GETSOCKOPT, unix.SYS_SETSOCKOPT,
		unix.SYS_MEMBARRIER, unix.SYS_PRCTL,
	}
	filters := make([]unix.SockFilter, 0, 5+len(allowed)*2)
	filters = append(filters,
		unix.SockFilter{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 4},
		unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jt: 1, K: auditArchitecture},
		unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_KILL_PROCESS},
		unix.SockFilter{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 0},
	)
	for _, number := range allowed {
		filters = append(filters,
			unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jf: 1, K: number},
			unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ALLOW},
		)
	}
	filters = append(filters, unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ERRNO | uint32(syscall.EPERM)})
	program := unix.SockFprog{Len: uint16(len(filters)), Filter: &filters[0]}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return err
	}
	_, _, errno := unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER, 0, uintptr(unsafe.Pointer(&program)))
	if errno != 0 {
		return errno
	}
	return nil
}

func platformSandboxCommand(ctx context.Context, bin string, _ []string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, bin, args...)
}
