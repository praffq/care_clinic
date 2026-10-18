package clinic

import (
	"net"
	"strconv"
	"time"
)

// HomePort is the service's forwarded port for home access; the stack's
// clinic.env already trusts the Tailscale origin it is served on.
const HomePort = 8480

func (e *Clinic) HomePortOpen() bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(HomePort)), 2*time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
