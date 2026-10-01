package daemon

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

// peerUID returns the effective uid of the process at the other end of
// conn, as the kernel recorded it when the connection was made.
func peerUID(conn *net.UnixConn) (int, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, fmt.Errorf("peer credentials: %w", err)
	}
	var credentials *unix.Ucred
	var credentialErr error
	err = raw.Control(func(fd uintptr) {
		credentials, credentialErr = unix.GetsockoptUcred(
			int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	})
	if err != nil {
		return 0, fmt.Errorf("peer credentials: %w", err)
	}
	if credentialErr != nil {
		return 0, fmt.Errorf("peer credentials: %w", credentialErr)
	}
	return int(credentials.Uid), nil
}
