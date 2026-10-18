//go:build !windows

package control

import (
	"context"
	"net"
)

// SystemSocket is the installed service's control socket.
const SystemSocket = "/var/run/care-clinic/control.sock"

func dialSocket(ctx context.Context, path string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", path)
}
