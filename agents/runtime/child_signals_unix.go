//go:build !windows

package runtime

import (
	"errors"
	"os"
	"sync"
)

func newChildSignals(string) (childSignals, error) {
	supervisorRead, supervisorWrite, err := os.Pipe()
	if err != nil {
		return childSignals{}, err
	}
	cancelRead, cancelWrite, err := os.Pipe()
	if err != nil {
		_ = supervisorRead.Close()
		_ = supervisorWrite.Close()
		return childSignals{}, err
	}
	var cancelOnce sync.Once
	return childSignals{
		files: []*os.File{supervisorRead, cancelRead},
		requestCancel: func() {
			cancelOnce.Do(func() { _ = cancelWrite.Close() })
		},
		close: func() {
			_ = errors.Join(supervisorWrite.Close(), cancelWrite.Close())
		},
	}, nil
}
