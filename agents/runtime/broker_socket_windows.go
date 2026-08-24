//go:build windows

package runtime

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

type windowsBrokerTransport struct {
	listener net.Listener
	path     string
}

func newLocalBrokerTransport(nonce string) (localBrokerTransport, error) {
	securityDescriptor, err := resolveWindowsPipeSecurityDescriptor(currentWindowsUserSID)
	if err != nil {
		return nil, err
	}
	path := `\\.\pipe\yufeng-run-` + nonce
	listener, err := winio.ListenPipe(path, &winio.PipeConfig{
		SecurityDescriptor: securityDescriptor,
		InputBufferSize:    64 << 10,
		OutputBufferSize:   64 << 10,
	})
	if err != nil {
		return nil, fmt.Errorf("create local supervisor named pipe: %w", err)
	}
	return &windowsBrokerTransport{listener: listener, path: path}, nil
}

func currentWindowsUserSID() (string, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("get current process token user: %w", err)
	}
	if user == nil || user.User.Sid == nil || !user.User.Sid.IsValid() {
		return "", fmt.Errorf("current process token user sid is invalid")
	}
	sid := user.User.Sid.String()
	if sid == "" {
		return "", fmt.Errorf("format current process token user sid")
	}
	return sid, nil
}

func (t *windowsBrokerTransport) Accept(ctx context.Context) (net.Conn, error) {
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

func (t *windowsBrokerTransport) ChildEnvironment() []string {
	return []string{envBrokerPipe + "=" + t.path}
}
func (t *windowsBrokerTransport) ChildFiles() []*os.File { return nil }
func (t *windowsBrokerTransport) Close() error {
	if t == nil || t.listener == nil {
		return nil
	}
	return t.listener.Close()
}

func dialLocalBroker(int) (net.Conn, error) {
	path := strings.TrimSpace(os.Getenv(envBrokerPipe))
	if path == "" {
		return nil, failedBroker()
	}
	timeout := 5 * time.Second
	conn, err := winio.DialPipe(path, &timeout)
	if err != nil {
		return nil, fmt.Errorf("failed_precondition: local supervisor named pipe is required: %w", err)
	}
	return conn, nil
}
