package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/forward"
)

// loopbackVsock stands in for listenVsock: each "vsock port" gets a TCP
// listener on 127.0.0.1, and ports in fail cannot be bound.
type loopbackVsock struct {
	mu    sync.Mutex
	addrs map[uint32]string
	fail  map[uint32]bool
}

func (v *loopbackVsock) listen(p forward.Port) (net.Listener, error) {
	port := p.VsockPort
	if v.fail[port] {
		return nil, errors.New("address in use")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	v.mu.Lock()
	v.addrs[port] = l.Addr().String()
	v.mu.Unlock()
	return l, nil
}

func echoServer(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); _ = c.Close() }()
		}
	}()
	return l.Addr().String()
}

// refusedAddr returns a loopback address with nothing listening.
func refusedAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

func TestRelays(t *testing.T) {
	v := &loopbackVsock{addrs: map[uint32]string{}, fail: map[uint32]bool{3: true}}
	ports := []forward.Port{
		{Name: "up", VsockPort: 1, GuestAddr: echoServer(t)},
		{Name: "down", VsockPort: 2, GuestAddr: refusedAddr(t)},
		{Name: "unbindable", VsockPort: 3, GuestAddr: echoServer(t)},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wait := startRelays(ctx, v.listen, ports)
	if _, ok := v.addrs[3]; ok || len(v.addrs) != 2 {
		t.Fatalf("listeners = %v", v.addrs)
	}

	up, err := net.Dial("tcp", v.addrs[1])
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = up.Close() }()
	_ = up.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(up, "ping"); err != nil {
		t.Fatal(err)
	}
	if err := up.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if b, err := io.ReadAll(up); err != nil || string(b) != "ping" {
		t.Fatalf("relayed echo = %q, %v", b, err)
	}

	// The clinic stack is not up: the connection is closed at once.
	down, err := net.Dial("tcp", v.addrs[2])
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = down.Close() }()
	_ = down.SetReadDeadline(time.Now().Add(2 * time.Second))
	if n, err := down.Read(make([]byte, 1)); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("read on refused relay = %d, %v; want EOF", n, err)
	}

	cancel()
	done := make(chan struct{})
	go func() { wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("relays did not stop")
	}
	if c, err := net.DialTimeout("tcp", v.addrs[1], time.Second); err == nil {
		_ = c.Close()
		t.Fatal("relay listener still open after shutdown")
	}
}

func freeTCPPort(t *testing.T) uint16 {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return uint16(l.Addr().(*net.TCPAddr).Port)
}

func TestTLSRelayRequiresClientCert(t *testing.T) {
	ca := newCA(t, true)
	srvCert, srvKey := ca.issue(t, "engine", x509.ExtKeyUsageServerAuth)
	cfg, err := parseTLSBundle(bundle(ca.pem, srvCert, srvKey))
	if err != nil {
		t.Fatal(err)
	}
	port := freeTCPPort(t)
	ctx, cancel := context.WithCancel(context.Background())
	wait := startRelays(ctx, tlsRelayListener(cfg), []forward.Port{{Name: "t", TCPPort: port, GuestAddr: echoServer(t)}})
	defer func() { cancel(); wait() }()

	roots := x509.NewCertPool()
	roots.AddCert(ca.cert)
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port)))
	dial := func(certs ...tls.Certificate) (string, error) {
		c, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", addr,
			&tls.Config{RootCAs: roots, Certificates: certs, MinVersion: tls.VersionTLS13})
		if err != nil {
			return "", err
		}
		defer func() { _ = c.Close() }()
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := c.Write([]byte("ping")); err != nil {
			return "", err
		}
		b := make([]byte, 4)
		_, err = io.ReadFull(c, b)
		return string(b), err
	}
	cc, ck := ca.issue(t, "host", x509.ExtKeyUsageClientAuth)
	pair, err := tls.X509KeyPair(cc, ck)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := dial(pair); err != nil || got != "ping" {
		t.Fatalf("with client cert: %q, %v", got, err)
	}
	if _, err := dial(); err == nil {
		t.Fatal("relay accepted a connection without a client certificate")
	}
}

func TestTLSRelayNeedsTCPPort(t *testing.T) {
	if _, err := tlsRelayListener(&tls.Config{})(forward.Port{Name: "x"}); err == nil {
		t.Fatal("want an error for a port without TCPPort")
	}
}
