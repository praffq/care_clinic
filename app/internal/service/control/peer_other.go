//go:build !darwin && !linux

package control

import (
	"errors"
	"net"
)

func peerOf(*net.UnixConn) (Peer, error) {
	return Peer{}, errors.ErrUnsupported
}
