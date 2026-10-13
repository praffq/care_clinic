package vm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/sys/applog"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/mdns"
)

// HostStatus is what the helper reports on its control socket.
type HostStatus struct {
	Running   bool   `json:"running"`
	Docker    bool   `json:"docker"`
	Version   string `json:"version"`
	Error     string `json:"error,omitempty"`
	DiskFree  uint64 `json:"disk_free"`
	DiskTotal uint64 `json:"disk_total"`
	PID       int    `json:"pid"`
	WebPorts  []int  `json:"web_ports,omitempty"`
	// Logs is empty when the helper predates guest log collection.
	Logs                   string                   `json:"logs,omitempty"`
	AdvertisementSupported bool                     `json:"advertisement_supported"`
	Advertisement          mdns.AdvertisementStatus `json:"advertisement"`
}

// Guest log collection states reported in HostStatus.Logs.
const (
	LogsConnecting  = "connecting"
	LogsCollecting  = "collecting"
	LogsInterrupted = "interrupted"
)

type host struct {
	m                    machine
	g                    *guest
	man                  Manifest
	logf                 func(string, ...any)
	stopReq              chan struct{}
	once                 sync.Once
	background           sync.WaitGroup
	sync                 *syncer
	advertiseMu          sync.Mutex
	advertiser           advertisementManager
	advertiseCancelMu    sync.Mutex
	advertiseCancel      context.CancelFunc
	advertiseWithdrawing bool

	mu     sync.Mutex
	status HostStatus
}

// RunHost is the helper process (care-clinic --vm-host). It boots the VM,
// exposes Docker to this computer and serves the control socket until asked to
// stop, then shuts the guest down cleanly.
func RunHost() int {
	if !Supported() {
		fmt.Fprintln(os.Stderr, errUnsupported)
		return 2
	}
	_ = os.MkdirAll(StateDir(), 0o700)
	lf, err := applog.NewStream(HostLog())
	if err == nil {
		log.SetOutput(lf)
	} else {
		fmt.Fprintf(os.Stderr, "engine logging unavailable: %v\n", err)
	}
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	logf := log.Printf
	if err := runHost(logf); err != nil {
		logf("engine: %v", err)
		return 1
	}
	return 0
}

func runHost(logf func(string, ...any)) (retErr error) {
	if err := os.MkdirAll(runDir(), 0o700); err != nil {
		return err
	}
	if c := newClient(); c.alive(context.Background()) {
		return errors.New("another engine helper is already running")
	}
	_ = os.Remove(controlSocket())
	ctl, err := net.Listen("unix", controlSocket())
	if err != nil {
		return fmt.Errorf("control socket: %w", err)
	}
	defer func() { _ = os.Remove(controlSocket()) }()

	h := &host{
		logf: logf, stopReq: make(chan struct{}),
		status: HostStatus{Running: true, PID: os.Getpid(), Logs: LogsConnecting, AdvertisementSupported: true},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-h.stopReq:
			cancel()
		case <-ctx.Done():
		}
	}()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)
	go func() {
		select {
		case s := <-sig:
			logf("engine: %s", s)
			h.requestStop()
		case <-ctx.Done():
		}
	}()

	startupHandler := h.startupRoutes()
	var activeHandler atomic.Pointer[http.Handler]
	initialHandler := startupHandler
	activeHandler.Store(&initialHandler)
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			(*activeHandler.Load()).ServeHTTP(w, r)
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() { _ = srv.Serve(ctl) }()
	defer func() {
		h.mu.Lock()
		startupFailed := h.status.Error != ""
		h.mu.Unlock()
		if retErr != nil && startupFailed {
			// Keep a startup failure queryable long enough for the app to report
			// it instead of waiting for the generic five-minute timeout.
			select {
			case <-time.After(5 * time.Second):
			case <-h.stopReq:
			}
		}
		_ = srv.Close()
	}()
	defer h.stopAdvertisement()

	man, err := Installed()
	if err != nil {
		h.setError(err)
		return err
	}
	h.mu.Lock()
	h.status.Version = man.Version
	h.mu.Unlock()
	cfg := LoadConfig()
	if h.stopRequested() {
		return nil
	}
	if err := prepareDataDisk(ctx, man, cfg.DiskGB); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		err = fmt.Errorf("data disk: %w", err)
		h.setError(err)
		return err
	}
	if h.stopRequested() {
		return nil
	}
	logf("engine: booting %s (%d CPUs, %d MB, %d GB disk)", man.Version, cfg.CPUs, cfg.MemoryMB, cfg.DiskGB)
	m, err := boot(ctx, bootOptions{manifest: man, config: cfg, logf: logf})
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		err = fmt.Errorf("start VM: %w", err)
		h.setError(err)
		return err
	}
	if err := h.bringUp(ctx, m, man, 4*time.Minute, h.attach); err != nil {
		if errors.Is(err, errStopRequested) {
			logf("engine: stopped during startup")
			return nil
		}
		return err
	}
	readyHandler := http.Handler(h.routes())
	activeHandler.Store(&readyHandler)
	logf("engine: ready")
	go h.housekeeping(ctx)

	select {
	case <-h.stopReq:
		logf("engine: stop requested")
	case <-m.done():
		logf("engine: VM stopped on its own")
		return fmt.Errorf("the VM stopped unexpectedly; see %s", ConsoleLog())
	}
	cancel()
	h.stopAdvertisement()
	h.shutdown()
	return nil
}

var errStopRequested = errors.New("engine stop requested")

// Shortened by tests.
var (
	guestRequestTimeout = 5 * time.Second
	abortGuestGrace     = 30 * time.Second
)

func (h *host) requestStop() {
	h.stopAdvertisement()
	h.once.Do(func() { close(h.stopReq) })
}

func (h *host) stopRequested() bool {
	select {
	case <-h.stopReq:
		return true
	default:
		return false
	}
}

// bringUp waits for the booted machine to serve Docker and attaches it. A stop
// request (ctx cancellation) at any stage powers the machine down and returns
// errStopRequested; other failures are published on the status.
func (h *host) bringUp(ctx context.Context, m machine, man Manifest, limit time.Duration, attach func(context.Context) error) error {
	h.m, h.man = m, man
	h.g = newGuest(func(ctx context.Context) (net.Conn, error) { return m.dial(ctx, vsockControl) })
	h.advertiseMu.Lock()
	h.advertiser = mdns.NewManager(func(line string) { h.logf("%s", line) })
	h.advertiseMu.Unlock()
	h.background.Add(1)
	go func() {
		defer h.background.Done()
		h.collectLogs(ctx)
	}()
	if ctx.Err() != nil {
		h.abortBoot()
		return errStopRequested
	}
	if err := h.waitReady(ctx, limit); err != nil {
		if ctx.Err() != nil {
			h.abortBoot()
			return errStopRequested
		}
		h.setError(err)
		_ = m.kill()
		return err
	}
	if err := attach(ctx); err != nil {
		if ctx.Err() != nil {
			h.abortBoot()
			return errStopRequested
		}
		h.setError(err)
		h.shutdown()
		return err
	}
	h.mu.Lock()
	h.status.Docker = true
	h.mu.Unlock()
	return nil
}

// abortBoot stops a machine whose guest may not be serving yet: ask politely,
// and power it off if the guest cannot be reached or does not stop.
func (h *host) abortBoot() {
	h.logf("engine: stopping the VM before it was ready")
	ctx, cancel := context.WithTimeout(context.Background(), guestRequestTimeout)
	err := h.g.shutdown(ctx)
	cancel()
	if err == nil {
		select {
		case <-h.m.done():
			return
		case <-time.After(abortGuestGrace):
		}
	}
	_ = h.m.kill()
	select {
	case <-h.m.done():
	case <-time.After(10 * time.Second):
	}
}

func (h *host) setLogs(state string) {
	h.mu.Lock()
	h.status.Logs = state
	h.mu.Unlock()
}

func (h *host) setError(err error) {
	h.mu.Lock()
	h.status.Error = err.Error()
	h.mu.Unlock()
}

var waitReadyPoll = time.Second

func (h *host) waitReady(ctx context.Context, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	var last error
	for time.Now().Before(deadline) {
		select {
		case <-h.m.done():
			return fmt.Errorf("the VM stopped while starting; see %s", ConsoleLog())
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		c, cancel := context.WithTimeout(ctx, 3*time.Second)
		s, err := h.g.status(c)
		cancel()
		if err == nil && s.Docker {
			_ = h.g.setTime(ctx, time.Now())
			return nil
		}
		if err == nil {
			err = errors.New("Docker is still starting")
		}
		last = err
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(waitReadyPoll):
		}
	}
	return fmt.Errorf("the engine did not become ready: %v", last)
}

// attach makes the engine usable from this computer: folder shares and
// sockets on macOS, file sync on Windows.
func (h *host) attach(ctx context.Context) error {
	if runtime.GOOS == "windows" {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", hostTCPDocker))
		if err != nil {
			return fmt.Errorf("Docker relay: %w", err)
		}
		relay := newDockerRelay()
		go forward(ctx, ln, relay.connect, h.logf)
		h.sync = newSyncer(h.g, h.logf)
		h.mu.Lock()
		h.status.WebPorts = []int{80, 443}
		h.mu.Unlock()
		return nil
	}
	for _, s := range darwinShares() {
		if err := h.g.mount(ctx, s.tag, s.guest); err != nil {
			return fmt.Errorf("share %s: %w", s.host, err)
		}
	}
	_ = os.Remove(dockerSocket())
	ln, err := net.Listen("unix", dockerSocket())
	if err != nil {
		return fmt.Errorf("docker socket: %w", err)
	}
	_ = os.Chmod(dockerSocket(), 0o600)
	go forward(ctx, ln, func(c context.Context) (net.Conn, error) { return h.m.dial(c, vsockDocker) }, h.logf)
	// The clinic's web ports. Binding below 1024 needs no privilege on
	// macOS 10.14+; if something else holds a port, the clinic reports it.
	for _, p := range []uint32{vsockHTTP, vsockHTTPS} {
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", p))
		if err != nil {
			h.logf("engine: port %d: %v", p, err)
			continue
		}
		h.mu.Lock()
		h.status.WebPorts = append(h.status.WebPorts, int(p))
		h.mu.Unlock()
		go forward(ctx, ln, func(c context.Context) (net.Conn, error) { return h.m.dial(c, p) }, h.logf)
	}
	return nil
}

// housekeeping keeps the guest clock right after the computer sleeps, keeps
// disk numbers fresh and, on Windows, keeps shared folders in step.
func (h *host) housekeeping(ctx context.Context) {
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	last := time.Now()
	for {
		h.refresh(ctx)
		h.refreshAdvertisement(ctx)
		if h.sync != nil {
			if err := h.sync.run(ctx, nil); err != nil {
				h.logf("sync: %v", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			// Wall-clock jumps mean the host slept; the guest clock froze.
			if now.Sub(last) > time.Minute || now.Round(0).Sub(last.Round(0)) > time.Minute {
				_ = h.g.setTime(ctx, now)
			}
			last = now
		}
	}
}

func (h *host) refresh(ctx context.Context) {
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	s, err := h.g.status(c)
	free, total, derr := h.g.df(c)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.status.Docker = err == nil && s.Docker
	if derr == nil {
		h.status.DiskFree, h.status.DiskTotal = free, total
	}
}

func (h *host) shutdown() {
	h.logf("engine: shutting down guest")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err := h.g.shutdown(ctx)
	cancel()
	if err != nil {
		h.logf("engine: shutdown request: %v", err)
	}
	select {
	case <-h.m.done():
		h.logf("engine: stopped")
	case <-time.After(90 * time.Second):
		h.logf("engine: guest did not stop in time; powering off")
		_ = h.m.kill()
	}
}

func (h *host) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) { h.writeStatus(w) })
	mux.HandleFunc("POST /stop", func(w http.ResponseWriter, r *http.Request) {
		h.requestStop()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /advertisement", func(w http.ResponseWriter, r *http.Request) {
		h.refreshAdvertisement(r.Context())
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /advertisement/withdraw", func(w http.ResponseWriter, r *http.Request) {
		h.withdrawAdvertisement()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /sync", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Paths []string `json:"paths"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if h.sync == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if err := h.sync.run(r.Context(), in.Paths); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /journal", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if err := h.g.journal(r.Context(), w); err != nil {
			_, _ = fmt.Fprintln(w, err)
		}
	})
	return mux
}

func (h *host) startupRoutes() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/status":
			h.writeStatus(w)
		case r.Method == http.MethodPost && r.URL.Path == "/stop":
			h.requestStop()
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && (r.URL.Path == "/advertisement" || r.URL.Path == "/advertisement/withdraw"):
			// Nothing is advertised until the engine is healthy; the ready
			// helper reconciles the persisted settings immediately.
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "engine is still starting", http.StatusServiceUnavailable)
		}
	})
}

func (h *host) writeStatus(w http.ResponseWriter) {
	h.mu.Lock()
	status := h.status
	h.mu.Unlock()
	writeJSON(w, status)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

type share struct{ tag, host, guest string }

// darwinShares are the macOS folders the VM can see, at the same paths, so
// bind mounts from the install and backup folders work unchanged.
func darwinShares() []share {
	home, _ := os.UserHomeDir()
	out := []share{{tag: "care-home", host: home, guest: filepath.ToSlash(home)}}
	if fi, err := os.Stat("/Volumes"); err == nil && fi.IsDir() {
		out = append(out, share{tag: "care-volumes", host: "/Volumes", guest: "/Volumes"})
	}
	return out
}
