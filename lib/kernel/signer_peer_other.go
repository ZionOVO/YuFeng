//go:build !linux

package kernel

import (
	"errors"
	"net"
)

func verifyUnixPeerUID(_ net.Conn, allowedUID int) error {
	if allowedUID < 0 {
		return nil
	}
	return errors.New("signer peer credential verification is unavailable on this platform")
}
