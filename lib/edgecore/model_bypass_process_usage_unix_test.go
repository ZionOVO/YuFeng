//go:build !windows

package edgecore

import (
	"runtime"
	"testing"

	"golang.org/x/sys/unix"
)

func modelBypassReadProcessUsage(t *testing.T) modelBypassProcessUsage {
	t.Helper()
	var usage unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_SELF, &usage); err != nil {
		t.Fatal(err)
	}
	resident := uint64(usage.Maxrss)
	if runtime.GOOS != "darwin" {
		resident *= 1024
	}
	return modelBypassProcessUsage{
		cpuSeconds:    float64(usage.Utime.Sec) + float64(usage.Utime.Usec)/1e6 + float64(usage.Stime.Sec) + float64(usage.Stime.Usec)/1e6,
		residentBytes: resident,
	}
}
