//go:build linux

package control

import (
	"net"

	"golang.org/x/sys/unix"
)

// peerOf returns the credentials of the process on the other end of c
// (SO_PEERCRED).
func peerOf(c *net.UnixConn) (Peer, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return Peer{}, err
	}
	var (
		cred *unix.Ucred
		cerr error
	)
	if err := raw.Control(func(fd uintptr) {
		cred, cerr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return Peer{}, err
	}
	if cerr != nil {
		return Peer{}, cerr
	}
	return Peer{UID: cred.Uid, GIDs: []uint32{cred.Gid}}, nil
}
