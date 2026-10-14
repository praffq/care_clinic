//go:build windows

package vm

import (
	"context"
	"crypto/tls"
	"net"
	"strconv"
)

func dialAgent(ctx context.Context) (net.Conn, error) {
	cfg, err := clientTLS()
	if err != nil {
		return nil, err
	}
	return (&tls.Dialer{Config: cfg}).DialContext(ctx, "tcp", "127.0.0.1:"+strconv.Itoa(hostTCPControl))
}
