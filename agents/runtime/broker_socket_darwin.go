//go:build darwin

package runtime

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

type darwinBrokerTransport struct {
	listener net.Listener
	path     string
}

func newLocalBrokerTransport(nonce string) (localBrokerTransport, error) {
	path, err := darwinBrokerSocketPath(nonce)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("create local supervisor socket: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("protect local supervisor socket: %w", err)
	}
	if canonical, err := filepath.EvalSymlinks(path); err == nil {
		path = canonical
	}
	return &darwinBrokerTransport{listener: listener, path: path}, nil
}

func (t *darwinBrokerTransport) Accept(ctx context.Context) (net.Conn, error) {
	if t == nil || t.listener == nil {
		return nil, failedBroker()
	}
	type result struct {
		conn net.Conn
		err  error
	}
	ready := make(chan result, 1)
	go func() {
		conn, err := t.listener.Accept()
		ready <- result{conn: conn, err: err}
	}()
	select {
	case <-ctx.Done():
		_ = t.listener.Close()
		return nil, ctx.Err()
	case value := <-ready:
		return value.conn, value.err
	}
}

func (t *darwinBrokerTransport) ChildEnvironment() []string {
	return []string{envBrokerPipe + "=" + t.path}
}

func (t *darwinBrokerTransport) ChildFiles() []*os.File { return nil }

func (t *darwinBrokerTransport) Close() error {
	if t == nil {
		return nil
	}
	var closeErr error
	if t.listener != nil {
		closeErr = t.listener.Close()
		t.listener = nil
	}
	if t.path != "" {
		if err := os.Remove(t.path); err != nil && !os.IsNotExist(err) && closeErr == nil {
			closeErr = err
		}
	}
	return closeErr
}

func dialLocalBroker(int) (net.Conn, error) {
	path := strings.TrimSpace(os.Getenv(envBrokerPipe))
	if path == "" {
		return nil, failedBroker()
	}
	conn, err := net.Dial("unix", path)
	if err != nil {
		return nil, fmt.Errorf("failed_precondition: local supervisor socket is required: %w", err)
	}
	return conn, nil
}
