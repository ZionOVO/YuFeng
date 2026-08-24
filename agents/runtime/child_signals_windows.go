//go:build windows

package runtime

import (
	"os"
	"strconv"
	"sync"

	"golang.org/x/sys/windows"
)

func newChildSignals(nonce string) (childSignals, error) {
	name := `Local\yufeng-run-cancel-` + nonce
	namePointer, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return childSignals{}, err
	}
	event, err := windows.CreateEvent(nil, 1, 0, namePointer)
	if err != nil {
		return childSignals{}, err
	}
	var cancelOnce sync.Once
	return childSignals{
		environment: []string{envSupervisorPID + "=" + strconv.Itoa(os.Getpid()), envCancelEvent + "=" + name},
		requestCancel: func() {
			cancelOnce.Do(func() { _ = windows.SetEvent(event) })
		},
		close: func() { _ = windows.CloseHandle(event) },
	}, nil
}
