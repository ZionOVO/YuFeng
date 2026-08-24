//go:build !windows && !darwin

package runtime

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"syscall"
)

type unixBrokerTransport struct {
	mu    sync.Mutex
	conn  net.Conn
	child *os.File
}

func newLocalBrokerTransport(string) (localBrokerTransport, error) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		return nil, err
	}
	syscall.CloseOnExec(fds[0])
	syscall.CloseOnExec(fds[1])
	parent := os.NewFile(uintptr(fds[0]), "broker-parent")
	child := os.NewFile(uintptr(fds[1]), "broker-child")
	conn, err := net.FileConn(parent)
	_ = parent.Close()
	if err != nil {
		_ = child.Close()
		return nil, err
	}
	return &unixBrokerTransport{conn: conn, child: child}, nil
}

func (t *unixBrokerTransport) Accept(context.Context) (net.Conn, error) {
	if t == nil {
		return nil, failedBroker()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.conn == nil {
		return nil, failedBroker()
	}
	conn := t.conn
	t.conn = nil
	return conn, nil
}

func (t *unixBrokerTransport) ChildEnvironment() []string { return nil }
func (t *unixBrokerTransport) ChildFiles() []*os.File {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.child == nil {
		return nil
	}
	return []*os.File{t.child}
}
func (t *unixBrokerTransport) Close() error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	conn, child := t.conn, t.child
	t.conn, t.child = nil, nil
	t.mu.Unlock()
	var err error
	if conn != nil {
		err = conn.Close()
	}
	if child != nil {
		err = errors.Join(err, child.Close())
	}
	return err
}

func dialLocalBroker(fd int) (net.Conn, error) {
	if fd < 3 {
		return nil, failedBroker()
	}
	file := os.NewFile(uintptr(fd), "broker")
	if file == nil {
		return nil, failedBroker()
	}
	conn, err := net.FileConn(file)
	_ = file.Close()
	if err != nil {
		return nil, fmt.Errorf("failed_precondition: local supervisor broker is required: %w", err)
	}
	return conn, nil
}
