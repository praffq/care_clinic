//go:build linux

package main

import (
	"net"
	"syscall"
	"time"

	"github.com/mdlayher/vsock"
)

func listenVsock(port uint32) (net.Listener, error) { return vsock.Listen(port, nil) }

func setClock(t time.Time) error {
	tv := syscall.NsecToTimeval(t.UnixNano())
	return syscall.Settimeofday(&tv)
}

func diskUsage(p string) (free, total uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(p, &st); err != nil {
		return 0, 0, err
	}
	return st.Bavail * uint64(st.Bsize), st.Blocks * uint64(st.Bsize), nil
}
