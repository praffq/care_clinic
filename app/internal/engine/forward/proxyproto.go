package forward

import (
	"encoding/binary"
	"io"
	"net"
)

// PROXY protocol v2 (https://www.haproxy.org/download/2.9/doc/proxy-protocol.txt,
// section 2.2): a 16-byte preamble followed by the address block.
var proxyV2Signature = []byte{0x0D, 0x0A, 0x0D, 0x0A, 0x00, 0x0D, 0x0A, 0x51, 0x55, 0x49, 0x54, 0x0A}

const (
	proxyV2Local = 0x20 // version 2, LOCAL: no address information
	proxyV2Proxy = 0x21 // version 2, PROXY: connection relayed on behalf of a client
	proxyV2TCP4  = 0x11 // AF_INET, SOCK_STREAM
	proxyV2TCP6  = 0x21 // AF_INET6, SOCK_STREAM
)

// ProxyHeaderV2 is a HeaderFunc that describes client with a PROXY protocol
// v2 header: TCP over IPv4 or IPv6 with the client's remote (source) and
// local (destination) addresses, or a LOCAL header when client is not TCP.
func ProxyHeaderV2(upstream io.Writer, client net.Conn) error {
	_, err := upstream.Write(AppendProxyHeaderV2(nil, client.RemoteAddr(), client.LocalAddr()))
	return err
}

// AppendProxyHeaderV2 appends the PROXY v2 header for a connection from src
// to dst. A pair of IPv4 addresses (including IPv4-mapped IPv6) is encoded as
// TCP4, any other TCP pair as TCP6, and anything else as LOCAL.
func AppendProxyHeaderV2(b []byte, src, dst net.Addr) []byte {
	b = append(b, proxyV2Signature...)
	s, sok := src.(*net.TCPAddr)
	d, dok := dst.(*net.TCPAddr)
	if !sok || !dok || s == nil || d == nil || s.IP == nil || d.IP == nil {
		return append(b, proxyV2Local, 0x00, 0x00, 0x00)
	}
	s4, d4 := s.IP.To4(), d.IP.To4()
	if s4 != nil && d4 != nil {
		b = append(b, proxyV2Proxy, proxyV2TCP4)
		b = binary.BigEndian.AppendUint16(b, 4+4+2+2)
		b = append(b, s4...)
		b = append(b, d4...)
	} else {
		b = append(b, proxyV2Proxy, proxyV2TCP6)
		b = binary.BigEndian.AppendUint16(b, 16+16+2+2)
		b = append(b, s.IP.To16()...)
		b = append(b, d.IP.To16()...)
	}
	b = binary.BigEndian.AppendUint16(b, uint16(s.Port))
	return binary.BigEndian.AppendUint16(b, uint16(d.Port))
}
