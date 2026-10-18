package hostfwd

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/forward"
	"github.com/ohcnetwork/care_desktop/app/internal/service/api"
)

// guest is a fake vsock: each port maps to a TCP echo server that first
// reports what it read up to EOF.
type guest struct {
	t     *testing.T
	ports map[uint32]string
	down  bool
}

func newGuest(t *testing.T, ports ...uint32) *guest {
	g := &guest{t: t, ports: map[uint32]string{}}
	for _, p := range ports {
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
				go func() {
					defer func() { _ = c.Close() }()
					b, _ := io.ReadAll(c)
					_, _ = c.Write(b)
				}()
			}
		}()
		g.ports[p] = l.Addr().String()
	}
	return g
}

func (g *guest) dial(ctx context.Context, port uint32) (net.Conn, error) {
	if g.down {
		return nil, api.ErrEngineUnavailable
	}
	addr, ok := g.ports[port]
	if !ok {
		return nil, errors.New("connection refused")
	}
	var d net.Dialer
	return d.DialContext(ctx, "tcp", addr)
}

func run(t *testing.T, cfg Config) *Forwarder {
	t.Helper()
	f := New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return f
}

// roundTrip sends msg with a half-close and returns the reply.
func roundTrip(t *testing.T, addr, msg string) string {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(c, msg); err != nil {
		t.Fatal(err)
	}
	_ = c.(*net.TCPConn).CloseWrite()
	b, _ := io.ReadAll(c)
	return string(b)
}

func listening(t *testing.T, f *Forwarder, name string) api.ForwardListener {
	t.Helper()
	for _, l := range f.Status().Listeners {
		if l.Name == name {
			return l
		}
	}
	t.Fatalf("no listener %q", name)
	return api.ForwardListener{}
}

func TestForwardsToVsockPorts(t *testing.T) {
	g := newGuest(t, 8080, 8443)
	f := run(t, Config{Dial: g.dial, Rules: []Rule{
		{Name: "http", Listen: "127.0.0.1:0", VsockPort: 8080},
		{Name: "https", Listen: "127.0.0.1:0", VsockPort: 8443},
	}})
	st := f.Status()
	if st.ProxyProtocol || len(st.Listeners) != 2 {
		t.Fatalf("status = %+v", st)
	}
	for _, l := range st.Listeners {
		if !l.Listening || l.Error != "" || strings.HasSuffix(l.Listen, ":0") {
			t.Fatalf("listener = %+v", l)
		}
		if got := roundTrip(t, l.Listen, "hello "+l.Name); got != "hello "+l.Name {
			t.Fatalf("%s: got %q", l.Name, got)
		}
	}
}

func TestEngineDownClosesClient(t *testing.T) {
	g := newGuest(t)
	g.down = true
	f := run(t, Config{Dial: g.dial, Rules: []Rule{{Name: "http", Listen: "127.0.0.1:0", VsockPort: 8080}}})
	start := time.Now()
	if got := roundTrip(t, listening(t, f, "http").Listen, "x"); got != "" {
		t.Fatalf("got %q", got)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("client closed after %s", d)
	}
}

func TestProxyProtocol(t *testing.T) {
	g := newGuest(t, 8443)
	f := run(t, Config{Dial: g.dial, ProxyProtocol: true, Rules: []Rule{{Name: "https", Listen: "127.0.0.1:0", VsockPort: 8443}}})
	if !f.Status().ProxyProtocol {
		t.Fatal("status does not report PROXY protocol")
	}
	got := roundTrip(t, listening(t, f, "https").Listen, "body")
	if !strings.HasPrefix(got, "\r\n\r\n\x00\r\nQUIT\n\x21\x11") || !strings.HasSuffix(got, "body") {
		t.Fatalf("upstream got %q", got)
	}
}

func TestBindConflictIsReportedAndRetried(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := busy.Addr().String()
	g := newGuest(t, 8080)
	f := run(t, Config{Dial: g.dial, RetryInterval: 20 * time.Millisecond, Rules: []Rule{{Name: "http", Listen: addr, VsockPort: 8080}}})
	l := listening(t, f, "http")
	if l.Listening || !strings.Contains(l.Error, "already in use") || l.Listen != addr {
		t.Fatalf("listener = %+v", l)
	}
	_ = busy.Close()
	deadline := time.Now().Add(5 * time.Second)
	for !listening(t, f, "http").Listening {
		if time.Now().After(deadline) {
			t.Fatalf("never bound: %+v", listening(t, f, "http"))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := roundTrip(t, addr, "late"); got != "late" {
		t.Fatalf("got %q", got)
	}
}

func TestStandardRules(t *testing.T) {
	rules, err := StandardRules(nil)
	if err != nil || len(rules) != 3 {
		t.Fatalf("rules = %+v, %v", rules, err)
	}
	for i, p := range forward.StandardPorts() {
		if rules[i] != (Rule{Name: p.Name, Listen: p.HostAddr, VsockPort: p.VsockPort}) {
			t.Fatalf("rule %d = %+v", i, rules[i])
		}
	}
	rules, err = StandardRules(map[string]string{"http": "127.0.0.1:18080", "home": Off})
	if err != nil || len(rules) != 2 || rules[0].Listen != "127.0.0.1:18080" || rules[1].Name != "https" {
		t.Fatalf("rules = %+v, %v", rules, err)
	}
	for _, bad := range []map[string]string{{"ssh": "127.0.0.1:22"}, {"http": "18080"}} {
		if _, err := StandardRules(bad); err == nil {
			t.Errorf("StandardRules(%v) succeeded", bad)
		}
	}
}

func TestParseLsof(t *testing.T) {
	for in, want := range map[string]string{
		"p123\ncnginx\nf4\n":        "nginx (pid 123)",
		"p9\nccaddy\np10\ncother\n": "caddy (pid 9)",
		"p77\n":                     "pid 77",
		"":                          "",
	} {
		if got := parseLsof([]byte(in)); got != want {
			t.Errorf("parseLsof(%q) = %q, want %q", in, got, want)
		}
	}
}
