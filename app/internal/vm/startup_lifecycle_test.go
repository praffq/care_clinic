package vm

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"
)

type lifecycleMachine struct {
	mu          sync.Mutex
	doneCh      chan struct{}
	killed      int
	dialErr     error
	dialSeen    chan struct{}
	closeOnKill bool
}

func newLifecycleMachine() *lifecycleMachine {
	return &lifecycleMachine{doneCh: make(chan struct{}), dialErr: errors.New("guest not listening"), dialSeen: make(chan struct{}, 16), closeOnKill: true}
}

func (m *lifecycleMachine) dial(ctx context.Context, _ uint32) (net.Conn, error) {
	select {
	case m.dialSeen <- struct{}{}:
	default:
	}
	return nil, m.dialErr
}
func (m *lifecycleMachine) done() <-chan struct{} { return m.doneCh }
func (m *lifecycleMachine) kill() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.killed++
	if m.killed == 1 && m.closeOnKill {
		close(m.doneCh)
	}
	return nil
}
func (m *lifecycleMachine) kills() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.killed
}

func lifecycleHost(t *testing.T) *host {
	h := advertisementFixture(t)
	h.status.Docker = false
	t.Cleanup(h.background.Wait)
	return h
}

func TestStartupRoutesAcceptStopAndDeferAdvertisement(t *testing.T) {
	h := lifecycleHost(t)
	for _, path := range []string{"/advertisement", "/advertisement/withdraw"} {
		w := httptest.NewRecorder()
		h.startupRoutes().ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, nil))
		if w.Code != http.StatusNoContent {
			t.Fatalf("%s during startup: %d", path, w.Code)
		}
	}
	if h.stopRequested() {
		t.Fatal("advertisement calls must not stop startup")
	}
	w := httptest.NewRecorder()
	h.startupRoutes().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/stop", nil))
	if w.Code != http.StatusNoContent || !h.stopRequested() {
		t.Fatalf("stop during startup: %d requested=%t", w.Code, h.stopRequested())
	}
	w = httptest.NewRecorder()
	h.startupRoutes().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/sync", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("other control during startup: %d", w.Code)
	}
}

func TestStopRoutedDuringReadinessCancelsWaitAndKillsMachine(t *testing.T) {
	oldReq, oldGrace := guestRequestTimeout, abortGuestGrace
	guestRequestTimeout, abortGuestGrace = time.Second, time.Second
	defer func() { guestRequestTimeout, abortGuestGrace = oldReq, oldGrace }()
	h := lifecycleHost(t)
	m := newLifecycleMachine()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-h.stopReq
		cancel()
	}()
	result := make(chan error, 1)
	go func() {
		result <- h.bringUp(ctx, m, Manifest{}, time.Minute, func(context.Context) error {
			t.Error("attach must not run after stop")
			return nil
		})
	}()
	select {
	case <-m.dialSeen: // readiness polling is genuinely in progress
	case <-time.After(5 * time.Second):
		t.Fatal("readiness wait never contacted the guest")
	}
	w := httptest.NewRecorder()
	h.startupRoutes().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/stop", nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("stop: %d", w.Code)
	}
	select {
	case err := <-result:
		if !errors.Is(err, errStopRequested) {
			t.Fatalf("bringUp = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stop did not cancel the readiness wait")
	}
	if m.kills() != 1 {
		t.Fatalf("machine kills = %d", m.kills())
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.status.Error != "" || h.status.Docker {
		t.Fatalf("status after stop: %+v", h.status)
	}
}

func TestStopBeforeReadinessPollKillsMachineWithoutWaiting(t *testing.T) {
	h := lifecycleHost(t)
	m := newLifecycleMachine()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	err := h.bringUp(ctx, m, Manifest{}, time.Minute, func(context.Context) error { return nil })
	if !errors.Is(err, errStopRequested) || m.kills() != 1 || time.Since(start) > 3*time.Second {
		t.Fatalf("err=%v kills=%d after %s", err, m.kills(), time.Since(start))
	}
}

type pipeListener struct {
	conns chan net.Conn
	done  chan struct{}
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}
func (l *pipeListener) Close() error   { close(l.done); return nil }
func (l *pipeListener) Addr() net.Addr { return &net.UnixAddr{Name: "pipe", Net: "pipe"} }

func TestStopDuringAttachRequestsGuestShutdown(t *testing.T) {
	h := lifecycleHost(t)
	m := newLifecycleMachine()
	shutdown := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"docker":true}`)) })
	mux.HandleFunc("POST /v1/time", func(w http.ResponseWriter, _ *http.Request) {})
	mux.HandleFunc("POST /v1/shutdown", func(w http.ResponseWriter, _ *http.Request) {
		close(shutdown)
		m.mu.Lock()
		close(m.doneCh)
		m.mu.Unlock()
	})
	l := &pipeListener{conns: make(chan net.Conn), done: make(chan struct{})}
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(l) }()
	defer func() { _ = srv.Close() }()
	m.dialErr = nil
	dialer := &pipeDialMachine{lifecycleMachine: m, l: l}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attaching := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- h.bringUp(ctx, dialer, Manifest{}, time.Minute, func(ctx context.Context) error {
			close(attaching)
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	select {
	case <-attaching:
	case <-time.After(5 * time.Second):
		t.Fatal("never reached attach")
	}
	cancel()
	if err := <-result; !errors.Is(err, errStopRequested) {
		t.Fatalf("bringUp = %v", err)
	}
	select {
	case <-shutdown:
	default:
		t.Fatal("guest was not asked to shut down")
	}
	if m.kills() != 0 {
		t.Fatalf("cleanly stopped guest was killed %d times", m.kills())
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.status.Error != "" {
		t.Fatalf("a requested stop must not publish an error: %+v", h.status)
	}
}

type pipeDialMachine struct {
	*lifecycleMachine
	l *pipeListener
}

func (p *pipeDialMachine) dial(ctx context.Context, _ uint32) (net.Conn, error) {
	c, s := net.Pipe()
	select {
	case p.l.conns <- s:
		return c, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestCancelledDataDiskPreparationLeavesNoDisk(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("LOCALAPPDATA", t.TempDir())
	dir := t.TempDir()
	m := Manifest{dir: dir}
	_ = os.WriteFile(m.path("data.img"), []byte("seed"), 0o600)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := prepareDataDisk(ctx, m, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	for _, p := range []string{dataDisk(), dataDisk() + ".new"} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s left behind: %v", p, err)
		}
	}
}

func TestCancelledCopyStopsMidStreamAndRemovesPartialFile(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reads := 0
	r := ctxReader{ctx, readerFunc(func(p []byte) (int, error) {
		reads++
		cancel()
		return len(p), nil
	})}
	buf := make([]byte, 8)
	if _, err := r.Read(buf); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Read(buf); !errors.Is(err, context.Canceled) || reads != 1 {
		t.Fatalf("err=%v reads=%d", err, reads)
	}
}

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

func TestStartBlockingReleasesCallerOnCancelThenStopsLateMachine(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	release := make(chan struct{})
	stopped, cleaned := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- startBlocking(ctx, func() error { <-release; return nil },
			func() { close(stopped) }, func() { close(cleaned) })
	}()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not release the blocked start")
	}
	select {
	case <-stopped:
		t.Fatal("machine stopped before its start returned")
	default:
	}
	close(release)
	for _, c := range []chan struct{}{stopped, cleaned} {
		select {
		case <-c:
		case <-time.After(5 * time.Second):
			t.Fatal("late machine was not stopped and cleaned up")
		}
	}
}

func TestStartBlockingFailureAndSuccess(t *testing.T) {
	cleaned := 0
	boom := errors.New("boom")
	if err := startBlocking(context.Background(), func() error { return boom }, func() { t.Fatal("stop") }, func() { cleaned++ }); err != boom || cleaned != 1 {
		t.Fatalf("err=%v cleaned=%d", err, cleaned)
	}
	if err := startBlocking(context.Background(), func() error { return nil }, func() { t.Fatal("stop") }, func() { t.Fatal("cleanup") }); err != nil {
		t.Fatal(err)
	}
}
