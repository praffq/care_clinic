// Package forward splices TCP-like streams between a listener and a dialer.
// The host service uses it to expose the clinic's Caddy (ports 80, 443, 8480)
// by forwarding host TCP connections to fixed vsock ports on the engine VM,
// and care-engine-agent uses it inside the VM to relay those vsock ports to
// fixed guest TCP addresses (StandardPorts). It knows nothing about any
// hypervisor: the caller supplies the net.Listener and a DialFunc.
//
// Bytes are copied untouched in both directions (TLS passes through) with
// half-close: when one side reaches EOF the other side's write half is shut
// down, and the connection is closed once both directions are done.
package forward

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"time"
)

// DialFunc opens the upstream connection for one accepted connection.
type DialFunc func(ctx context.Context) (net.Conn, error)

// HeaderFunc writes a preamble to upstream before any client bytes, e.g. a
// PROXY protocol v2 header describing client.
type HeaderFunc func(upstream io.Writer, client net.Conn) error

// Options tune Serve. The zero value is usable.
type Options struct {
	// Name prefixes log lines.
	Name string
	// MaxConns caps concurrent connections; further connections are closed
	// as soon as they are accepted. Default DefaultMaxConns.
	MaxConns int
	// DialTimeout bounds each upstream dial. Default DefaultDialTimeout.
	DialTimeout time.Duration
	// GracePeriod is how long Serve waits, once ctx is done, for in-flight
	// connections to finish before closing them. Zero closes them at once.
	GracePeriod time.Duration
	// Header, if set, is called on each upstream connection before splicing.
	Header HeaderFunc
	// Logf receives low-volume diagnostics (repeated errors are rate
	// limited). Nil discards them.
	Logf func(format string, args ...any)
}

const (
	DefaultMaxConns    = 256
	DefaultDialTimeout = 10 * time.Second

	logEvery       = 30 * time.Second
	minAcceptDelay = 5 * time.Millisecond
	maxAcceptDelay = time.Second
)

// Serve accepts connections from l and splices each with a connection from
// dial until ctx is done or l fails permanently. It closes l, waits for the
// in-flight connections (see Options.GracePeriod) and returns nil when ctx
// ended it, or the accept error otherwise. Errors on individual connections,
// including dial failures (the client is closed at once), never stop Serve.
func Serve(ctx context.Context, l net.Listener, dial DialFunc, opts Options) error {
	if opts.MaxConns <= 0 {
		opts.MaxConns = DefaultMaxConns
	}
	if opts.DialTimeout <= 0 {
		opts.DialTimeout = DefaultDialTimeout
	}
	s := &server{
		dial:  dial,
		opts:  opts,
		slots: make(chan struct{}, opts.MaxConns),
	}
	return s.serve(ctx, l)
}

type server struct {
	dial  DialFunc
	opts  Options
	slots chan struct{}
	wg    sync.WaitGroup

	logMu   sync.Mutex
	lastLog map[string]time.Time
	dropped map[string]int
}

func (s *server) serve(ctx context.Context, l net.Listener) error {
	// connCtx outlives ctx by the grace period; cancelling it aborts every
	// in-flight dial and connection.
	connCtx, abort := context.WithCancel(context.WithoutCancel(ctx))
	defer abort()
	stop := context.AfterFunc(ctx, func() { _ = l.Close() })
	defer stop()

	err := s.acceptLoop(ctx, connCtx, l)
	_ = l.Close()

	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	if s.opts.GracePeriod > 0 {
		t := time.NewTimer(s.opts.GracePeriod)
		select {
		case <-done:
		case <-t.C:
		}
		t.Stop()
	}
	abort()
	<-done
	return err
}

func (s *server) acceptLoop(ctx, connCtx context.Context, l net.Listener) error {
	var delay time.Duration
	for {
		c, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if errors.Is(err, net.ErrClosed) || errors.Is(err, os.ErrClosed) {
				return err
			}
			delay = min(max(2*delay, minAcceptDelay), maxAcceptDelay)
			s.logf("accept", "accept: %v; retrying in %s", err, delay)
			t := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				t.Stop()
				return nil
			case <-t.C:
			}
			continue
		}
		delay = 0
		select {
		case s.slots <- struct{}{}:
		default:
			_ = c.Close()
			s.logf("limit", "connection limit (%d) reached; rejected %s", s.opts.MaxConns, c.RemoteAddr())
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer func() { <-s.slots }()
			s.handle(connCtx, c)
		}()
	}
}

func (s *server) handle(ctx context.Context, client net.Conn) {
	defer func() { _ = client.Close() }()
	defer context.AfterFunc(ctx, func() { _ = client.Close() })()

	dctx, cancel := context.WithTimeout(ctx, s.opts.DialTimeout)
	up, err := s.dial(dctx)
	cancel()
	if err != nil {
		if ctx.Err() == nil {
			s.logf("dial", "dial upstream for %s: %v", client.RemoteAddr(), err)
		}
		return
	}
	defer func() { _ = up.Close() }()
	defer context.AfterFunc(ctx, func() { _ = up.Close() })()

	if s.opts.Header != nil {
		if err := s.opts.Header(up, client); err != nil {
			s.logf("header", "write header for %s: %v", client.RemoteAddr(), err)
			return
		}
	}
	splice(client, up)
}

// splice copies a<->b until both directions are done.
func splice(a, b net.Conn) {
	done := make(chan struct{})
	go func() { pipe(b, a); close(done) }()
	pipe(a, b)
	<-done
}

type closeWriter interface{ CloseWrite() error }

// pipe copies src to dst, then shuts down dst's write half so the peer sees
// EOF. On a copy error, or when dst cannot half-close, both connections are
// closed so the opposite direction ends too instead of holding the
// connection (and its slot) open.
func pipe(dst, src net.Conn) {
	if _, err := io.Copy(dst, src); err == nil {
		if cw, ok := dst.(closeWriter); ok && cw.CloseWrite() == nil {
			return
		}
	}
	_ = src.Close()
	_ = dst.Close()
}

// logf logs at most once per logEvery for each key, reporting how many
// messages were suppressed in between.
func (s *server) logf(key, format string, args ...any) {
	if s.opts.Logf == nil {
		return
	}
	s.logMu.Lock()
	if s.lastLog == nil {
		s.lastLog, s.dropped = map[string]time.Time{}, map[string]int{}
	}
	now := time.Now()
	if last, ok := s.lastLog[key]; ok && now.Sub(last) < logEvery {
		s.dropped[key]++
		s.logMu.Unlock()
		return
	}
	n := s.dropped[key]
	s.lastLog[key], s.dropped[key] = now, 0
	s.logMu.Unlock()

	if s.opts.Name != "" {
		format = s.opts.Name + ": " + format
	}
	if n > 0 {
		format += " (%d similar suppressed)"
		args = append(args, n)
	}
	s.opts.Logf(format, args...)
}
