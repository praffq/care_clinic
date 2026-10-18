package forward

import (
	"context"
	"net"
	"strconv"
)

// Port is one fixed mapping between a host listener, a vsock port and the
// guest TCP address of the clinic's Caddy. The guest relay
// (care-engine-agent) listens on VsockPort and dials GuestAddr; the host
// listens on HostAddr and dials VsockPort. Nothing else is ever forwarded.
type Port struct {
	Name      string
	VsockPort uint32
	GuestAddr string
	HostAddr  string
}

// Vsock ports of the guest relay. 1024 is the agent API (vm.AgentVsockPort).
const (
	VsockHTTP  = 8080
	VsockHTTPS = 8443
	VsockHome  = 8480
)

var standardPorts = []Port{
	{Name: "http", VsockPort: VsockHTTP, GuestAddr: "127.0.0.1:80", HostAddr: "0.0.0.0:80"},
	{Name: "https", VsockPort: VsockHTTPS, GuestAddr: "127.0.0.1:443", HostAddr: "0.0.0.0:443"},
	// Tailscale home access: `tailscale serve` on the host proxies to it, so
	// it is never exposed beyond the host's loopback.
	{Name: "home", VsockPort: VsockHome, GuestAddr: "127.0.0.1:8480", HostAddr: "127.0.0.1:8480"},
}

// StandardPorts returns the fixed port table.
func StandardPorts() []Port { return append([]Port(nil), standardPorts...) }

// PortForGuest returns the standard Port whose guest TCP port is guestPort.
func PortForGuest(guestPort int) (Port, bool) {
	for _, p := range standardPorts {
		if _, ps, err := net.SplitHostPort(p.GuestAddr); err == nil && ps == strconv.Itoa(guestPort) {
			return p, true
		}
	}
	return Port{}, false
}

// VsockDialer dials a vsock port on the guest, e.g. vm.Machine.DialVsock.
type VsockDialer func(ctx context.Context, port uint32) (net.Conn, error)

// Rule is one host-side forward: listen on Listen and dial the guest via Dial.
type Rule struct {
	Name   string
	Listen string
	Dial   DialFunc
}

// HostRules returns the host rules for the standard ports.
func HostRules(dial VsockDialer) []Rule {
	rules := make([]Rule, 0, len(standardPorts))
	for _, p := range standardPorts {
		rules = append(rules, HostRule(p, p.HostAddr, dial))
	}
	return rules
}

// HostRule returns a rule listening on listen (instead of p.HostAddr) that
// dials p's vsock port.
func HostRule(p Port, listen string, dial VsockDialer) Rule {
	port := p.VsockPort
	return Rule{
		Name:   p.Name,
		Listen: listen,
		Dial:   func(ctx context.Context) (net.Conn, error) { return dial(ctx, port) },
	}
}
