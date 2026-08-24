package runtime

import "os"

type childSignals struct {
	files         []*os.File
	environment   []string
	requestCancel func()
	close         func()
}

func (s childSignals) RequestCancel() {
	if s.requestCancel != nil {
		s.requestCancel()
	}
}

func (s childSignals) Close() {
	if s.close != nil {
		s.close()
	}
}
