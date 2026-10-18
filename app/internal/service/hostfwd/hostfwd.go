// Package hostfwd binds the service's host listeners (80, 443 and the
// loopback home port) for the service's whole lifetime and forwards each
// connection to the engine VM's vsock relay through forward.Serve. While no
// engine runs, the dialer fails at once and clients are closed immediately.
// A listener that cannot bind (port taken) is reported in Status, with the
// owning process when lsof can tell, and retried.
package hostfwd

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/forward"
	"github.com/ohcnetwork/care_desktop/app/internal/service/api"
)

// Rule is one host listener and the guest vsock port it forwards to.
type Rule struct {
	Name      string
	Listen    string
	VsockPort uint32
}

// Off disables a standard listener in StandardRules.
const Off = "off"

// StandardRules returns the rules for forward.StandardPorts, with listen
// addresses replaced (or the rule dropped for Off) per name in overrides.
func StandardRules(overrides map[string]string) ([]Rule, error) {
	var rules []Rule
	seen := map[string]bool{}
	for _, p := range forward.StandardPorts() {
		addr := p.HostAddr
		if o, ok := overrides[p.Name]; ok {
			seen[p.Name] = true
			if o == Off {
				continue
			}
			if _, _, err := net.SplitHostPort(o); err != nil {
				return nil, fmt.Errorf("%s listen address: %w", p.Name, err)
			}
			addr = o
		}
		rules = append(rules, Rule{Name: p.Name, Listen: addr, VsockPort: p.VsockPort})
	}
	for name := range overrides {
		if !seen[name] {
			return nil, fmt.Errorf("unknown forward %q", name)
		}
	}
	return rules, nil
}

// Config configures a Forwarder.
type Config struct {
	Rules []Rule
	Dial  forward.VsockDialer
	// ProxyProtocol is the initial SetProxyProtocol value.
	ProxyProtocol bool
	// RetryInterval is the delay between bind attempts (10s).
	RetryInterval time.Duration
	Log           *slog.Logger
}

// Forwarder owns the host listeners.
type Forwarder struct {
	cfg   Config
	proxy atomic.Bool

	mu    sync.Mutex
	state []listenerState
	lns   []net.Listener // bound listeners not yet served, by rule
}

type listenerState struct {
	addr string
	err  error
}

// New binds every rule's listener now, so a taken port is reported as the
// service starts; Run serves them and retries the ones that failed.
func New(cfg Config) *Forwarder {
	if cfg.RetryInterval <= 0 {
		cfg.RetryInterval = 10 * time.Second
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	f := &Forwarder{cfg: cfg, state: make([]listenerState, len(cfg.Rules)), lns: make([]net.Listener, len(cfg.Rules))}
	f.proxy.Store(cfg.ProxyProtocol)
	for i := range cfg.Rules {
		f.lns[i] = f.bind(i)
	}
	return f
}

// bind listens for rule i and records the outcome.
func (f *Forwarder) bind(i int) net.Listener {
	r := f.cfg.Rules[i]
	ln, err := net.Listen("tcp", r.Listen)
	if err != nil {
		err = describeBindError(r.Listen, err)
	}
	f.mu.Lock()
	prev := f.state[i].err
	f.state[i] = listenerState{addr: r.Listen, err: err}
	if ln != nil {
		f.state[i].addr = ln.Addr().String()
	}
	f.mu.Unlock()
	switch {
	case err != nil && (prev == nil || prev.Error() != err.Error()):
		f.cfg.Log.Error("forward listener unavailable; retrying", "name", r.Name, "listen", r.Listen, "err", err, "retry", f.cfg.RetryInterval)
	case err == nil:
		f.cfg.Log.Info("forwarding", "name", r.Name, "listen", ln.Addr().String(), "vsock_port", r.VsockPort)
	}
	return ln
}

// Run serves every listener until ctx ends, re-binding any that is not
// listening every RetryInterval.
func (f *Forwarder) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for i := range f.cfg.Rules {
		wg.Go(func() { f.serve(ctx, i) })
	}
	wg.Wait()
}

func (f *Forwarder) serve(ctx context.Context, i int) {
	r := f.cfg.Rules[i]
	f.mu.Lock()
	ln := f.lns[i]
	f.lns[i] = nil
	f.mu.Unlock()
	opts := forward.Options{
		Name:   r.Name,
		Logf:   func(format string, args ...any) { f.cfg.Log.Warn(fmt.Sprintf(format, args...)) },
		Header: f.header,
	}
	dial := func(ctx context.Context) (net.Conn, error) { return f.cfg.Dial(ctx, r.VsockPort) }
	for {
		if ln != nil {
			err := forward.Serve(ctx, ln, dial, opts)
			if ctx.Err() != nil {
				return
			}
			f.mu.Lock()
			f.state[i].err = fmt.Errorf("listener failed: %w", err)
			f.mu.Unlock()
			f.cfg.Log.Error("forward listener failed; re-binding", "name", r.Name, "err", err)
		}
		t := time.NewTimer(f.cfg.RetryInterval)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		ln = f.bind(i)
	}
}

// SetProxyProtocol turns the PROXY v2 header on or off for connections
// accepted from now on.
func (f *Forwarder) SetProxyProtocol(on bool) {
	if f.proxy.Swap(on) != on {
		f.cfg.Log.Info("forwarding", "proxy_protocol", on)
	}
}

func (f *Forwarder) header(upstream io.Writer, client net.Conn) error {
	if f.proxy.Load() {
		return forward.ProxyHeaderV2(upstream, client)
	}
	return nil
}

// Status reports each listener.
func (f *Forwarder) Status() api.ForwardStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := api.ForwardStatus{ProxyProtocol: f.proxy.Load(), Listeners: []api.ForwardListener{}}
	for i, r := range f.cfg.Rules {
		l := api.ForwardListener{Name: r.Name, Listen: f.state[i].addr, VsockPort: r.VsockPort, Listening: f.state[i].err == nil}
		if err := f.state[i].err; err != nil {
			l.Error = err.Error()
		}
		st.Listeners = append(st.Listeners, l)
	}
	return st
}

// describeBindError names the process holding addr's port when it is in use
// and lsof can tell.
func describeBindError(addr string, err error) error {
	if !errors.Is(err, syscall.EADDRINUSE) {
		return err
	}
	_, port, _ := net.SplitHostPort(addr)
	if owner := portOwner(port); owner != "" {
		return fmt.Errorf("%s is already in use by %s: %w", addr, owner, err)
	}
	return fmt.Errorf("%s is already in use: %w", addr, err)
}

// portOwner returns "command (pid N)" for the process listening on TCP
// port, or "" when lsof is missing or finds nothing.
func portOwner(port string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, "lsof", "-nP", "-iTCP:"+port, "-sTCP:LISTEN", "-Fpc").Output()
	return parseLsof(out)
}

// parseLsof reads lsof -Fpc output: "p<pid>" then "c<command>" per process.
func parseLsof(out []byte) string {
	var pid, cmd string
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "p") && pid == "":
			pid = line[1:]
		case strings.HasPrefix(line, "c") && pid != "":
			cmd = line[1:]
		}
		if pid != "" && cmd != "" {
			return fmt.Sprintf("%s (pid %s)", cmd, pid)
		}
	}
	if pid != "" {
		return "pid " + pid
	}
	return ""
}
