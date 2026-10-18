//go:build !darwin && !linux && !windows

package control

import (
	"errors"
	"net"
)

func peerOf(*net.UnixConn) (Peer, error) {
	return Peer{}, errors.ErrUnsupported
}
