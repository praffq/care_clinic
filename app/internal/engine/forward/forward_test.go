package forward

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func listen(t *testing.T) net.Listener {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

// upstream serves each connection with handle until the test ends.
func upstream(t *testing.T, handle func(net.Conn)) DialFunc {
	t.Helper()
	l := listen(t)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() { defer func() { _ = c.Close() }(); handle(c) }()
		}
	}()
	return func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", l.Addr().String())
	}
}

func echo(c net.Conn) { _, _ = io.Copy(c, c) }

// start runs Serve in the background and returns the listen address and a
// stop function returning Serve's error.
func start(t *testing.T, dial DialFunc, opts Options) (string, func() error) {
	t.Helper()
	l := listen(t)
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- Serve(ctx, l, dial, opts) }()
	var once sync.Once
	var err error
	stop := func() error {
		once.Do(func() {
			cancel()
			select {
			case err = <-errc:
			case <-time.After(5 * time.Second):
				err = errors.New("Serve did not return")
			}
		})
		return err
	}
	t.Cleanup(func() { _ = stop() })
	return l.Addr().String(), stop
}

func dial(t *testing.T, addr string) *net.TCPConn {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	return c.(*net.TCPConn)
}

// expectClosed asserts that the peer closes c without sending data.
func expectClosed(t *testing.T, c net.Conn) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := c.Read(make([]byte, 1))
	if n != 0 || err == nil {
		t.Fatalf("read = %d, %v; want connection closed", n, err)
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		t.Fatalf("connection not closed: %v", err)
	}
}

func TestEcho(t *testing.T) {
	addr, stop := start(t, upstream(t, echo), Options{})
	for i := range 3 {
		c := dial(t, addr)
		msg := fmt.Sprintf("hello %d\n", i)
		if _, err := io.WriteString(c, msg); err != nil {
			t.Fatal(err)
		}
		got, err := bufio.NewReader(c).ReadString('\n')
		if err != nil || got != msg {
			t.Fatalf("echo = %q, %v; want %q", got, err, msg)
		}
	}
	if err := stop(); err != nil {
		t.Fatalf("Serve = %v", err)
	}
}

func TestHalfClose(t *testing.T) {
	// The upstream answers only after reading EOF, so this needs the
	// forwarder to propagate the client's CloseWrite and keep the
	// upstream-to-client direction open.
	addr, _ := start(t, upstream(t, func(c net.Conn) {
		b, err := io.ReadAll(c)
		if err != nil {
			return
		}
		_, _ = io.WriteString(c, "got "+string(b))
	}), Options{})
	c := dial(t, addr)
	if _, err := io.WriteString(c, "ping"); err != nil {
		t.Fatal(err)
	}
	if err := c.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(c)
	if err != nil || string(b) != "got ping" {
		t.Fatalf("reply = %q, %v", b, err)
	}
}

func TestLargeTransfer(t *testing.T) {
	addr, _ := start(t, upstream(t, echo), Options{})
	c := dial(t, addr)
	data := strings.Repeat("0123456789abcdef", 1<<16) // 1 MiB
	go func() {
		_, _ = io.WriteString(c, data)
		_ = c.CloseWrite()
	}()
	b, err := io.ReadAll(c)
	if err != nil || string(b) != data {
		t.Fatalf("read %d bytes, err %v; want %d bytes back", len(b), err, len(data))
	}
}

func TestConnectionLimit(t *testing.T) {
	var logs atomic.Int32
	addr, _ := start(t, upstream(t, echo), Options{
		MaxConns: 1,
		Logf:     func(string, ...any) { logs.Add(1) },
	})
	first := dial(t, addr)
	if _, err := io.WriteString(first, "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(first, make([]byte, 1)); err != nil {
		t.Fatal(err) // first is now spliced and holds the only slot
	}
	for range 3 {
		expectClosed(t, dial(t, addr))
	}
	if n := logs.Load(); n != 1 {
		t.Errorf("logged %d times; want 1 (rate limited)", n)
	}
	// Releasing the slot admits new connections again.
	_ = first.Close()
	deadline := time.Now().Add(2 * time.Second)
	for {
		c := dial(t, addr)
		_, _ = io.WriteString(c, "y")
		_ = c.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		if _, err := io.ReadFull(c, make([]byte, 1)); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("slot was not released")
		}
	}
}

func TestDialFailureClosesClient(t *testing.T) {
	var logged atomic.Value
	addr, _ := start(t, func(context.Context) (net.Conn, error) {
		return nil, errors.New("connection refused")
	}, Options{Name: "http", Logf: func(f string, a ...any) { logged.Store(fmt.Sprintf(f, a...)) }})
	expectClosed(t, dial(t, addr))
	if s, _ := logged.Load().(string); !strings.HasPrefix(s, "http: dial upstream") || !strings.Contains(s, "connection refused") {
		t.Errorf("log = %q", s)
	}
}

func TestShutdownAbortsConnections(t *testing.T) {
	addr, stop := start(t, upstream(t, echo), Options{})
	c := dial(t, addr)
	_, _ = io.WriteString(c, "x")
	if _, err := io.ReadFull(c, make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	if err := stop(); err != nil {
		t.Fatalf("Serve = %v", err)
	}
	expectClosed(t, c)
	if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		_ = c.Close()
		t.Fatal("listener still accepting after shutdown")
	}
}

func TestShutdownGracePeriod(t *testing.T) {
	release := make(chan struct{})
	addr, stop := start(t, upstream(t, func(c net.Conn) {
		_, _ = io.ReadFull(c, make([]byte, 1))
		<-release
		_, _ = io.WriteString(c, "bye")
	}), Options{GracePeriod: 5 * time.Second})
	c := dial(t, addr)
	_, _ = io.WriteString(c, "x")
	time.Sleep(50 * time.Millisecond) // let the connection reach upstream

	stopped := make(chan error, 1)
	go func() { stopped <- stop() }()
	select {
	case err := <-stopped:
		t.Fatalf("Serve returned (%v) before the in-flight connection finished", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	b, _ := io.ReadAll(c)
	_ = c.Close()
	if string(b) != "bye" {
		t.Fatalf("in-flight reply = %q; want bye", b)
	}
	if err := <-stopped; err != nil {
		t.Fatalf("Serve = %v", err)
	}
}

func TestHeader(t *testing.T) {
	addr, _ := start(t, upstream(t, echo), Options{
		Header: func(w io.Writer, client net.Conn) error {
			_, err := fmt.Fprintf(w, "FROM %s\n", client.RemoteAddr())
			return err
		},
	})
	c := dial(t, addr)
	_, _ = io.WriteString(c, "data\n")
	r := bufio.NewReader(c)
	hdr, _ := r.ReadString('\n')
	if want := "FROM " + c.LocalAddr().String() + "\n"; hdr != want {
		t.Fatalf("header = %q; want %q", hdr, want)
	}
	if body, _ := r.ReadString('\n'); body != "data\n" {
		t.Fatalf("body = %q", body)
	}
}

// flakyListener fails Accept a few times before delegating.
type flakyListener struct {
	net.Listener
	fails atomic.Int32
}

func (l *flakyListener) Accept() (net.Conn, error) {
	if l.fails.Add(-1) >= 0 {
		return nil, errors.New("accept4: too many open files")
	}
	return l.Listener.Accept()
}

func TestAcceptErrorBackoff(t *testing.T) {
	l := &flakyListener{Listener: listen(t)}
	l.fails.Store(4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- Serve(ctx, l, upstream(t, echo), Options{}) }()
	c := dial(t, l.Addr().String())
	_, _ = io.WriteString(c, "ok")
	b := make([]byte, 2)
	if _, err := io.ReadFull(c, b); err != nil || string(b) != "ok" {
		t.Fatalf("echo after accept errors = %q, %v", b, err)
	}
	cancel()
	if err := <-errc; err != nil {
		t.Fatalf("Serve = %v", err)
	}
}

func TestListenerClosedReturnsError(t *testing.T) {
	l := listen(t)
	errc := make(chan error, 1)
	go func() { errc <- Serve(context.Background(), l, upstream(t, echo), Options{}) }()
	time.Sleep(20 * time.Millisecond)
	_ = l.Close()
	select {
	case err := <-errc:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("Serve = %v; want net.ErrClosed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after its listener was closed")
	}
}

func TestPorts(t *testing.T) {
	ports := StandardPorts()
	if len(ports) != 3 {
		t.Fatalf("StandardPorts = %v", ports)
	}
	seen := map[uint32]bool{}
	for _, p := range ports {
		if seen[p.VsockPort] || p.VsockPort == 1024 {
			t.Errorf("vsock port %d reused", p.VsockPort)
		}
		seen[p.VsockPort] = true
		host, _, err := net.SplitHostPort(p.GuestAddr)
		if err != nil || host != "127.0.0.1" {
			t.Errorf("%s: guest address %q is not loopback", p.Name, p.GuestAddr)
		}
	}
	if p, ok := PortForGuest(8480); !ok || p.VsockPort != VsockHome || p.HostAddr != "127.0.0.1:8480" {
		t.Errorf("PortForGuest(8480) = %+v, %v", p, ok)
	}
	if _, ok := PortForGuest(22); ok {
		t.Error("PortForGuest(22) found a port")
	}

	var dialed []uint32
	rules := HostRules(func(_ context.Context, port uint32) (net.Conn, error) {
		dialed = append(dialed, port)
		return nil, errors.New("no vm")
	})
	for _, r := range rules {
		_, _ = r.Dial(context.Background())
	}
	want := []uint32{VsockHTTP, VsockHTTPS, VsockHome}
	if fmt.Sprint(dialed) != fmt.Sprint(want) || rules[0].Listen != "0.0.0.0:80" || rules[2].Listen != "127.0.0.1:8480" {
		t.Errorf("HostRules = %+v dialed %v", rules, dialed)
	}
}
