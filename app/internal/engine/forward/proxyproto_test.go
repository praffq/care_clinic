package forward

import (
	"bytes"
	"context"
	"encoding/hex"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

const sigHex = "0d0a0d0a000d0a515549540a"

func TestAppendProxyHeaderV2(t *testing.T) {
	tcp := func(ip string, port int) *net.TCPAddr { return &net.TCPAddr{IP: net.ParseIP(ip), Port: port} }
	for _, tc := range []struct {
		name     string
		src, dst net.Addr
		want     string // hex after the signature
	}{
		{
			name: "tcp4",
			src:  tcp("192.168.1.20", 51234), dst: tcp("10.0.0.5", 443),
			// ver/cmd 0x21, fam 0x11, len 12, src, dst, sport 0xc822, dport 0x01bb
			want: "21" + "11" + "000c" + "c0a80114" + "0a000005" + "c822" + "01bb",
		},
		{
			name: "tcp4 from mapped ipv6",
			src:  tcp("::ffff:127.0.0.1", 1), dst: tcp("::ffff:127.0.0.1", 8480),
			want: "21" + "11" + "000c" + "7f000001" + "7f000001" + "0001" + "2120",
		},
		{
			name: "tcp6",
			src:  tcp("fe80::1", 40000), dst: tcp("2001:db8::2", 80),
			want: "21" + "21" + "0024" +
				"fe800000000000000000000000000001" +
				"20010db8000000000000000000000002" +
				"9c40" + "0050",
		},
		{
			name: "mixed families use tcp6",
			src:  tcp("192.0.2.1", 2), dst: tcp("::1", 3),
			want: "21" + "21" + "0024" +
				"00000000000000000000ffffc0000201" +
				"00000000000000000000000000000001" +
				"0002" + "0003",
		},
		{
			name: "unix is local",
			src:  &net.UnixAddr{Name: "@", Net: "unix"}, dst: &net.UnixAddr{Name: "/tmp/s", Net: "unix"},
			want: "20" + "00" + "0000",
		},
		{
			name: "nil is local",
			want: "20" + "00" + "0000",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := hex.EncodeToString(AppendProxyHeaderV2(nil, tc.src, tc.dst))
			if want := sigHex + tc.want; got != want {
				t.Fatalf("header\n got %s\nwant %s", got, want)
			}
			// The length field covers exactly the bytes after the 16-byte preamble.
			b, _ := hex.DecodeString(got)
			if n := int(b[14])<<8 | int(b[15]); n != len(b)-16 {
				t.Fatalf("length field %d, address block %d bytes", n, len(b)-16)
			}
		})
	}
}

func TestAppendProxyHeaderV2Appends(t *testing.T) {
	b := AppendProxyHeaderV2([]byte("x"), nil, nil)
	if !bytes.HasPrefix(b, []byte("x\r\n\r\n\x00\r\nQUIT\n")) {
		t.Fatalf("got %q", b)
	}
}

// The header reaches upstream before any client bytes, with the client's
// address as source and the listener's as destination.
func TestServeProxyHeaderV2(t *testing.T) {
	got := make(chan []byte, 1)
	dial := upstream(t, func(c net.Conn) {
		b, _ := io.ReadAll(c)
		got <- b
	})
	l := listen(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = Serve(ctx, l, dial, Options{Header: ProxyHeaderV2}) }()

	c, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(c, "GET / HTTP/1.1\r\n"); err != nil {
		t.Fatal(err)
	}
	_ = c.(*net.TCPConn).CloseWrite()
	var b []byte
	select {
	case b = <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream received nothing")
	}
	_ = c.Close()
	want := AppendProxyHeaderV2(nil, c.LocalAddr(), c.RemoteAddr())
	if !bytes.HasPrefix(b, want) || !strings.HasSuffix(string(b), "GET / HTTP/1.1\r\n") || len(b) != len(want)+16 {
		t.Fatalf("upstream got %x", b)
	}
	if b[13] != proxyV2TCP4 {
		t.Fatalf("family %#x, want TCP4", b[13])
	}
}
