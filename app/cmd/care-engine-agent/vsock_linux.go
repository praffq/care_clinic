//go:build linux

package main

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

type vsockAddr struct{ cid, port uint32 }

func (vsockAddr) Network() string { return "vsock" }
func (a vsockAddr) String() string {
	return strconv.FormatUint(uint64(a.cid), 10) + ":" + strconv.FormatUint(uint64(a.port), 10)
}

func toVsockAddr(sa unix.Sockaddr) vsockAddr {
	if v, ok := sa.(*unix.SockaddrVM); ok {
		return vsockAddr{v.CID, v.Port}
	}
	return vsockAddr{}
}

// vsockConn is an AF_VSOCK stream. *os.File supplies Read/Write/Close and
// deadlines through the runtime poller (the fd is non-blocking).
type vsockConn struct {
	*os.File
	local, remote vsockAddr
}

func (c *vsockConn) LocalAddr() net.Addr  { return c.local }
func (c *vsockConn) RemoteAddr() net.Addr { return c.remote }

// CloseWrite shuts down the write half so the relay can pass on half-closes.
func (c *vsockConn) CloseWrite() error {
	rc, err := c.SyscallConn()
	if err != nil {
		return err
	}
	var serr error
	if err := rc.Control(func(fd uintptr) { serr = unix.Shutdown(int(fd), unix.SHUT_WR) }); err != nil {
		return err
	}
	return serr
}

type vsockListener struct {
	f    *os.File
	rc   syscall.RawConn
	addr vsockAddr
}

// listenVsock listens on the given AF_VSOCK port for any CID.
func listenVsock(port uint32) (net.Listener, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("vsock socket: %w", err)
	}
	if err := unix.Bind(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY, Port: port}); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("vsock bind port %d: %w", port, err)
	}
	if err := unix.Listen(fd, unix.SOMAXCONN); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("vsock listen: %w", err)
	}
	f := os.NewFile(uintptr(fd), "vsock-listener")
	rc, err := f.SyscallConn()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	l := &vsockListener{f: f, rc: rc, addr: vsockAddr{unix.VMADDR_CID_ANY, port}}
	if sa, err := unix.Getsockname(fd); err == nil {
		l.addr = toVsockAddr(sa)
	}
	return l, nil
}

func (l *vsockListener) Accept() (net.Conn, error) {
	var (
		nfd    int
		sa     unix.Sockaddr
		accErr error
	)
	err := l.rc.Read(func(fd uintptr) bool {
		nfd, sa, accErr = unix.Accept4(int(fd), unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC)
		return accErr != unix.EAGAIN
	})
	if err != nil {
		return nil, err
	}
	if accErr != nil {
		return nil, accErr
	}
	return &vsockConn{
		File:   os.NewFile(uintptr(nfd), "vsock-conn"),
		local:  l.addr,
		remote: toVsockAddr(sa),
	}, nil
}

func (l *vsockListener) Close() error   { return l.f.Close() }
func (l *vsockListener) Addr() net.Addr { return l.addr }
