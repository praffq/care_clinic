//go:build !windows

package vm

import (
	"context"
	"net"
	"runtime"
)

func dialAgent(ctx context.Context) (net.Conn, error) {
	socket := controlSocket()
	if runtime.GOOS == "linux" {
		socket = "/run/care-engine-agent/control.sock"
	}
	return (&net.Dialer{}).DialContext(ctx, "unix", socket)
}
