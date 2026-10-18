//go:build darwin

package control

import (
	"net"

	"golang.org/x/sys/unix"
)

// peerOf returns the credentials of the process on the other end of c (LOCAL_PEERCRED,
// like getpeereid but with the groups).
func peerOf(c *net.UnixConn) (Peer, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return Peer{}, err
	}
	var (
		cred *unix.Xucred
		cerr error
	)
	if err := raw.Control(func(fd uintptr) {
		cred, cerr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	}); err != nil {
		return Peer{}, err
	}
	if cerr != nil {
		return Peer{}, cerr
	}
	n := min(max(int(cred.Ngroups), 0), len(cred.Groups))
	gids := append([]uint32(nil), cred.Groups[:n]...)
	return Peer{UID: cred.Uid, GIDs: gids}, nil
}
