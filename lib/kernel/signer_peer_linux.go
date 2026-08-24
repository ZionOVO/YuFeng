//go:build linux

package kernel

import (
	"errors"
	"net"

	"golang.org/x/sys/unix"
)

func verifyUnixPeerUID(connection net.Conn, allowedUID int) error {
	if allowedUID < 0 {
		return nil
	}
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		return errors.New("signer connection is not a Unix socket")
	}
	raw, err := unixConnection.SyscallConn()
	if err != nil {
		return err
	}
	var credential *unix.Ucred
	var credentialErr error
	if err := raw.Control(func(fd uintptr) {
		credential, credentialErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return err
	}
	if credentialErr != nil || credential == nil || int(credential.Uid) != allowedUID {
		return errors.New("signer peer user is not allowed")
	}
	return nil
}
