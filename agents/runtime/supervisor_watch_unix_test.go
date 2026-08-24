//go:build !windows

package runtime

import (
	"context"
	"os"
	"syscall"
	"testing"
	"time"
)

func TestWatchCancellationCancelsWithoutClosingSupervisorBroker(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	watchFD, err := syscall.Dup(int(read.Fd()))
	if err != nil {
		_ = read.Close()
		_ = write.Close()
		t.Fatal(err)
	}
	if err := read.Close(); err != nil {
		_ = syscall.Close(watchFD)
		_ = write.Close()
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := WatchCancellation(watchFD, cancel); err != nil {
		_ = syscall.Close(watchFD)
		_ = write.Close()
		t.Fatal(err)
	}
	if err := write.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("cancellation pipe did not cancel execution context")
	}
}
