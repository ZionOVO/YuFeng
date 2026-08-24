package runtime

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

const (
	brokerNonceHexLength        = 32
	windowsMaxSIDSubAuthorities = 15
)

type localBrokerTransport interface {
	Accept(context.Context) (net.Conn, error)
	ChildEnvironment() []string
	ChildFiles() []*os.File
	Close() error
}

func dialBrokerClient(fd int, nonce string) (*BrokerClient, error) {
	if nonce == "" {
		return nil, failedBroker()
	}
	conn, err := dialLocalBroker(fd)
	if err != nil {
		return nil, err
	}
	return &BrokerClient{conn: conn, nonce: nonce, enc: jsonEncoder(conn), dec: jsonDecoder(conn)}, nil
}

func windowsPipeSecurityDescriptor(currentUserSID string) (string, error) {
	parts := strings.Split(currentUserSID, "-")
	if len(parts) < 4 || len(parts) > 3+windowsMaxSIDSubAuthorities || parts[0] != "S" || parts[1] != "1" {
		return "", fmt.Errorf("invalid windows user sid")
	}
	if err := validateSIDDecimal(parts[2], 48); err != nil {
		return "", err
	}
	for _, subAuthority := range parts[3:] {
		if err := validateSIDDecimal(subAuthority, 32); err != nil {
			return "", err
		}
	}
	return "D:P(A;;GA;;;SY)(A;;GA;;;" + currentUserSID + ")", nil
}

func validateSIDDecimal(value string, bitSize int) error {
	if value == "" {
		return fmt.Errorf("invalid windows user sid")
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return fmt.Errorf("invalid windows user sid")
		}
	}
	if _, err := strconv.ParseUint(value, 10, bitSize); err != nil {
		return fmt.Errorf("invalid windows user sid: %w", err)
	}
	return nil
}

func resolveWindowsPipeSecurityDescriptor(resolveCurrentUserSID func() (string, error)) (string, error) {
	if resolveCurrentUserSID == nil {
		return "", fmt.Errorf("resolve current windows user sid: resolver is nil")
	}
	currentUserSID, err := resolveCurrentUserSID()
	if err != nil {
		return "", fmt.Errorf("resolve current windows user sid: %w", err)
	}
	descriptor, err := windowsPipeSecurityDescriptor(currentUserSID)
	if err != nil {
		return "", fmt.Errorf("resolve current windows user sid: %w", err)
	}
	return descriptor, nil
}

func darwinBrokerSocketPath(nonce string) (string, error) {
	if len(nonce) != brokerNonceHexLength {
		return "", fmt.Errorf("invalid broker nonce")
	}
	for _, character := range nonce {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return "", fmt.Errorf("invalid broker nonce")
		}
	}
	return "/tmp/yfr-" + nonce + ".sock", nil
}
