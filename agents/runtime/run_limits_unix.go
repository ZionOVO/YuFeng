//go:build !windows

package runtime

import (
	"errors"
	"os"
	"syscall"
)

func limitCurrentProcess() error {
	return syscall.Setpgid(os.Getpid(), os.Getpid())
}

func applyPlatformResourceLimits(lim ResourceLimit) error {
	if err := setrlimit(syscall.RLIMIT_NOFILE, lim.Files); err != nil {
		return err
	}
	return setrlimitIfSupported(syscall.RLIMIT_CPU, lim.CPUSeconds)
}

func setrlimit(resource int, n uint64) error {
	var lim syscall.Rlimit
	if err := syscall.Getrlimit(resource, &lim); err != nil {
		return err
	}
	if lim.Cur > n {
		lim.Cur = n
	}
	if lim.Max > 0 && lim.Cur > lim.Max {
		lim.Cur = lim.Max
	}
	return syscall.Setrlimit(resource, &lim)
}

func setrlimitIfSupported(resource int, n uint64) error {
	err := setrlimit(resource, n)
	if err == nil || errors.Is(err, syscall.EINVAL) {
		return nil
	}
	return err
}
