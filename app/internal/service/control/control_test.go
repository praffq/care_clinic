//go:build unix

package control_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/agent"
	"github.com/ohcnetwork/care_desktop/app/internal/engine/client"
	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
	"github.com/ohcnetwork/care_desktop/app/internal/engine/vm"
	"github.com/ohcnetwork/care_desktop/app/internal/service/api"
	"github.com/ohcnetwork/care_desktop/app/internal/service/control"
	"github.com/ohcnetwork/care_desktop/app/internal/service/settings"
	"github.com/ohcnetwork/care_desktop/app/internal/service/slots"
	"github.com/ohcnetwork/care_desktop/app/internal/service/slots/slotstest"
	"github.com/ohcnetwork/care_desktop/app/internal/service/supervisor"
	"github.com/ohcnetwork/care_desktop/app/internal/service/supervisor/supervisortest"
)

type env struct {
	cl      *control.Client
	sock    string
	staging string
	store   *slots.Store
	factory *supervisortest.Factory
	cfgPath string

	mu      sync.Mutex
	mdns    api.MDNSStatus
	applied []api.ServiceConfig
}

func (e *env) setMDNS(st api.MDNSStatus) {
	e.mu.Lock()
	e.mdns = st
	e.mu.Unlock()
}

// shortDir returns a temp dir whose paths fit in a sockaddr_un.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ccs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func newEnv(t *testing.T, setup func(*agent.FakeExecutor)) *env {
	t.Helper()
	dir := shortDir(t)
	store := slots.New(filepath.Join(dir, "slots"), "arm64")
	f := &supervisortest.Factory{Dir: dir, Setup: setup}
	sup := supervisor.New(supervisor.Config{
		Slots:          store,
		VM:             vm.Config{DataDisk: filepath.Join(dir, "data.img"), ConsoleLog: filepath.Join(dir, "console.log")},
		NewMachine:     f.New,
		PollInterval:   10 * time.Millisecond,
		HealthInterval: 50 * time.Millisecond,
		StopTimeout:    time.Second,
		BackoffMin:     20 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() { sup.Run(ctx); close(runDone) }()

	sock := filepath.Join(dir, "run", "control.sock")
	ln, err := control.Listen(control.ListenConfig{Path: sock})
	if err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(dir, "staging")
	e := &env{cl: control.NewClient(sock), sock: sock, staging: staging, store: store, factory: f, cfgPath: filepath.Join(dir, "config.json")}
	e.mdns = api.MDNSStatus{Enabled: true, Name: "care"}
	cfg, err := settings.Open(e.cfgPath, func(c api.ServiceConfig) {
		e.mu.Lock()
		e.applied = append(e.applied, c)
		e.mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: control.NewServer(control.Config{
		Engine:     sup,
		Slots:      store,
		StagingDir: staging,
		MaxUpload:  maxUpload,
		Log:        slog.New(slog.DiscardHandler),
		Forward: func() api.ForwardStatus {
			return api.ForwardStatus{Listeners: []api.ForwardListener{{Name: "http", Listen: "0.0.0.0:80", VsockPort: 8080, Listening: true}}}
		},
		MDNS: func() api.MDNSStatus {
			e.mu.Lock()
			defer e.mu.Unlock()
			return e.mdns
		},
		Settings: cfg,
	}), ReadHeaderTimeout: time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		_ = srv.Close()
		cancel()
		<-runDone
		if f.Overlapped() {
			t.Error("two machines ran at once")
		}
	})
	return e
}

// maxUpload caps uploads in tests; test bundles are a few KiB.
const maxUpload = 1 << 20

func upload(t *testing.T, cl *control.Client, path, sum string) (slots.Slot, error) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return cl.InstallEngine(t.Context(), bytes.NewReader(b), int64(len(b)), sum)
}

// assertStagingEmpty waits for the service to remove staged uploads.
func (e *env) assertStagingEmpty(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		ents, err := os.ReadDir(e.staging)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if len(ents) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("staging not cleaned up: %v", ents)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (e *env) install(t *testing.T, version string) {
	t.Helper()
	path, sum := slotstest.WriteTestBundle(t, slotstest.BundleEntries(version, "arm64"))
	sl, err := upload(t, e.cl, path, sum)
	if err != nil {
		t.Fatal(err)
	}
	e.assertStagingEmpty(t)
	if sl.Version != version || sl.TarballSHA256 != sum {
		t.Fatalf("installed %+v", sl)
	}
}

func (e *env) waitState(t *testing.T, want api.EngineState) api.EngineStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, err := e.cl.Status(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if st.Engine.State == want {
			return st.Engine
		}
		if time.Now().After(deadline) {
			t.Fatalf("state %s, want %s (%+v)", st.Engine.State, want, st.Engine)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSocketMode(t *testing.T) {
	e := newEnv(t, nil)
	st, err := os.Stat(e.sock)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Type() != os.ModeSocket || st.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode %v", st.Mode())
	}
	if d, _ := os.Stat(filepath.Dir(e.sock)); d.Mode().Perm() != 0o700 {
		t.Fatalf("socket dir mode %v", d.Mode())
	}
	if _, err := os.Stat(e.sock + ".new"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary socket left behind: %v", err)
	}
	if _, err := control.Listen(control.ListenConfig{Path: e.sock}); err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("second Listen: %v", err)
	}
}

func TestListenReplacesStaleSocketAndRejectsFiles(t *testing.T) {
	dir := shortDir(t)
	sock := filepath.Join(dir, "c.sock")
	ln, err := control.Listen(control.ListenConfig{Path: sock})
	if err != nil {
		t.Fatal(err)
	}
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sock); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Close left the socket: %v", err)
	}

	// A crashed service leaves its socket behind.
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	_ = stale.Close()
	ln, err = control.Listen(control.ListenConfig{Path: sock})
	if err != nil {
		t.Fatalf("stale socket: %v", err)
	}
	_ = ln.Close()

	file := filepath.Join(dir, "plain")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Listen(control.ListenConfig{Path: file}); err == nil {
		t.Fatal("Listen replaced a regular file")
	}
}

func TestPolicy(t *testing.T) {
	staff := uint32(500)
	pol := control.Policy{OwnerUID: 0, Group: &staff, UserGroups: func(uid uint32) []uint32 {
		if uid == 503 {
			return []uint32{20, 500}
		}
		return []uint32{20}
	}}
	for _, tc := range []struct {
		peer control.Peer
		want bool
	}{
		{control.Peer{UID: 0}, true},
		{control.Peer{UID: 501, GIDs: []uint32{20, 500}}, true},
		{control.Peer{UID: 502, GIDs: []uint32{20}}, false},
		{control.Peer{UID: 503, GIDs: []uint32{20}}, true}, // member per the user database
	} {
		if got := pol.Allow(tc.peer); got != tc.want {
			t.Errorf("Allow(%+v) = %v", tc.peer, got)
		}
	}
	dev := control.Policy{OwnerUID: 501}
	if !dev.Allow(control.Peer{UID: 501}) || !dev.Allow(control.Peer{UID: 0}) || dev.Allow(control.Peer{UID: 502, GIDs: []uint32{500}}) {
		t.Error("dev policy must admit only the owner and root")
	}
}

func TestNoEngine(t *testing.T) {
	e := newEnv(t, nil)
	if _, err := e.cl.Start(t.Context()); !errors.Is(err, api.ErrNoEngine) {
		t.Fatalf("Start: %v", err)
	}
	if _, err := e.cl.Agent().Status(t.Context()); !errors.Is(err, client.ErrUnavailable) {
		t.Fatalf("agent status: %v", err)
	}
	if _, err := e.cl.Activate(t.Context(), "9.9.9"); !errors.Is(err, slots.ErrNotInstalled) {
		t.Fatalf("Activate: %v", err)
	}
	var ae *control.APIError
	if _, err := e.cl.InstallEngine(t.Context(), strings.NewReader("x"), 1, "00"); !errors.As(err, &ae) || ae.Status != http.StatusBadRequest {
		t.Fatalf("short sha: %v", err)
	}
}

func TestInstallActivateRelayStop(t *testing.T) {
	e := newEnv(t, nil)
	e.install(t, "1.0.0")

	if _, err := e.cl.Activate(t.Context(), "1.0.0"); err != nil {
		t.Fatal(err)
	}
	st := e.waitState(t, api.StateReady)
	if st.EngineVersion != "1.0.0" {
		t.Fatalf("engine version %q", st.EngineVersion)
	}
	l, err := e.cl.Slots(t.Context())
	if err != nil || l.Current != "1.0.0" || len(l.Slots) != 1 {
		t.Fatalf("slots %+v %v", l, err)
	}

	ag := e.cl.Agent()
	as, err := ag.Status(t.Context())
	if err != nil || !as.DockerReady {
		t.Fatalf("agent status %+v %v", as, err)
	}
	var res protocol.StackStatusResult
	if err := ag.Op(t.Context(), protocol.StackStatus{}, &res, nil); err != nil {
		t.Fatal(err)
	}

	if _, err := e.cl.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	e.waitState(t, api.StateStopped)
	if _, err := ag.Status(t.Context()); !errors.Is(err, client.ErrUnavailable) {
		t.Fatalf("agent status after stop: %v", err)
	}
}

// TestRelayStreams checks that op frames reach the app as the agent emits
// them and that request bodies pass through intact.
func TestRelayStreams(t *testing.T) {
	release := make(chan struct{})
	e := newEnv(t, func(x *agent.FakeExecutor) {
		x.Handle = func(ctx context.Context, _ protocol.Op, em agent.Emitter) (any, error) {
			em.Progress(protocol.Progress{State: "loading"})
			select {
			case <-release:
				return nil, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	})
	e.install(t, "1.0.0")
	if _, err := e.cl.Activate(t.Context(), "1.0.0"); err != nil {
		t.Fatal(err)
	}
	e.waitState(t, api.StateReady)

	body := bytes.Repeat([]byte("care"), 1<<20)
	op := protocol.ImageLoad{Role: protocol.ImageRoleBackend, Digest: protocol.Digest("sha256:" + strings.Repeat("a", 64))}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	frames := 0
	err := e.cl.Agent().LoadImage(ctx, op, io.MultiReader(bytes.NewReader(body)), func(f protocol.Frame) {
		if f.Progress != nil && f.Progress.State == "loading" {
			frames++
			close(release) // the op only finishes once its first frame arrived
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if frames != 1 {
		t.Fatalf("got %d progress frames", frames)
	}
	calls := e.factory.Last().Exec.Calls()
	if got := calls[len(calls)-1]; got.Method != "ImageLoad" || !bytes.Equal(got.Body, body) {
		t.Fatalf("agent got %s with %d bytes", got.Method, len(got.Body))
	}
}

func TestInstallUploadRejects(t *testing.T) {
	e := newEnv(t, nil)
	path, sum := slotstest.WriteTestBundle(t, slotstest.BundleEntries("1.0.0", "arm64"))

	wrong := strings.Repeat("0", 64)
	_, err := upload(t, e.cl, path, wrong)
	var ae *control.APIError
	if !errors.Is(err, slots.ErrChecksum) || !errors.As(err, &ae) || ae.Status != http.StatusUnprocessableEntity {
		t.Fatalf("wrong sha: %v", err)
	}
	if strings.Contains(err.Error(), sum) {
		t.Fatalf("response leaks the tarball hash: %v", err)
	}
	e.assertStagingEmpty(t)

	garbage := []byte("not a tarball")
	gsum := sha256.Sum256(garbage)
	_, err = e.cl.InstallEngine(t.Context(), bytes.NewReader(garbage), int64(len(garbage)), hex.EncodeToString(gsum[:]))
	if !errors.As(err, &ae) || ae.Code != api.CodeInvalidBundle {
		t.Fatalf("garbage: %v", err)
	}
	e.assertStagingEmpty(t)

	big := &countingReader{r: bytes.NewReader(make([]byte, maxUpload+1))}
	_, err = e.cl.InstallEngine(t.Context(), big, maxUpload+1, wrong)
	if !errors.As(err, &ae) || ae.Status != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized upload: %v", err)
	}
	if big.n > 0 {
		t.Fatalf("client sent %d bytes of a refused upload", big.n)
	}
	e.assertStagingEmpty(t)

	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return e.cl.Dial(ctx)
	}}}
	resp, err := hc.Post("http://x/v1/engine/install?sha256="+wrong, "application/gzip", io.MultiReader(strings.NewReader("chunked")))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusLengthRequired {
		t.Fatalf("chunked upload: %d", resp.StatusCode)
	}

	if st, err := os.Stat(e.staging); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("staging dir: %v %v", st.Mode(), err)
	}
}

func TestInstallUploadCancelled(t *testing.T) {
	e := newEnv(t, nil)
	pr, pw := io.Pipe()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := e.cl.InstallEngine(ctx, pr, maxUpload, strings.Repeat("0", 64))
		done <- err
	}()
	if _, err := pw.Write(make([]byte, 64<<10)); err != nil {
		t.Fatal(err)
	}
	// Wait until the service is staging the upload, then hang up.
	deadline := time.Now().Add(2 * time.Second)
	for ents, _ := os.ReadDir(e.staging); len(ents) == 0; ents, _ = os.ReadDir(e.staging) {
		if time.Now().After(deadline) {
			t.Fatal("upload never staged")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	_ = pw.CloseWithError(context.Canceled)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled upload: %v", err)
	}
	e.assertStagingEmpty(t)
}

func TestRelayAllowlist(t *testing.T) {
	e := newEnv(t, nil)
	e.install(t, "1.0.0")
	if _, err := e.cl.Activate(t.Context(), "1.0.0"); err != nil {
		t.Fatal(err)
	}
	e.waitState(t, api.StateReady)
	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return e.cl.Dial(ctx)
	}}}
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{"GET", "/v1/agent/v1/status", 200},
		{"POST", "/v1/agent/v1/shutdown", 404},
		{"POST", "/v1/agent/v1/time", 404},
		{"POST", "/v1/agent/v1/status", 404},
		{"GET", "/v1/agent/v1/files/../shutdown", 404},
		{"GET", "/v1/agent/v1/files/%2e%2e/x", 404},
		{"GET", "/v1/agent/v2/status", 404},
	} {
		req, _ := http.NewRequestWithContext(t.Context(), tc.method, "http://x"+tc.path, nil)
		req.URL.Opaque = tc.path // keep dot segments as sent
		resp, err := hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.path, resp.StatusCode, tc.want)
		}
	}
	if calls := e.factory.Last().Exec.Calls(); len(calls) > 0 && calls[len(calls)-1].Method == "PowerOff" {
		t.Fatal("relayed a shutdown")
	}
}

func TestWatch(t *testing.T) {
	e := newEnv(t, nil)
	e.install(t, "1.0.0")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	seen := make(chan api.EngineState, 64)
	go func() {
		_ = e.cl.Watch(ctx, func(st api.ServiceStatus) { seen <- st.Engine.State })
	}()
	if _, err := e.cl.Activate(t.Context(), "1.0.0"); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case s := <-seen:
			if s == api.StateReady {
				return
			}
		case <-ctx.Done():
			t.Fatal("watch never reported ready")
		}
	}
}

type countingReader struct {
	r io.Reader
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

func TestStatusIncludesForwardAndMDNS(t *testing.T) {
	e := newEnv(t, nil)
	st, err := e.cl.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if st.Forward == nil || len(st.Forward.Listeners) != 1 || !st.Forward.Listeners[0].Listening {
		t.Fatalf("forward = %+v", st.Forward)
	}
	if st.MDNS == nil || st.MDNS.Name != "care" {
		t.Fatalf("mdns = %+v", st.MDNS)
	}
}

func TestWatchReportsMDNSChanges(t *testing.T) {
	e := newEnv(t, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	seen := make(chan api.MDNSStatus, 64)
	go func() {
		_ = e.cl.Watch(ctx, func(st api.ServiceStatus) { seen <- *st.MDNS })
	}()
	if st := <-seen; st.Advertising {
		t.Fatalf("first = %+v", st)
	}
	e.setMDNS(api.MDNSStatus{Enabled: true, Name: "care", Advertising: true})
	for {
		select {
		case st := <-seen:
			if st.Advertising {
				return
			}
		case <-ctx.Done():
			t.Fatal("watch never reported the mDNS change")
		}
	}
}

func TestServiceConfig(t *testing.T) {
	e := newEnv(t, nil)
	cfg, err := e.cl.Config(t.Context())
	if err != nil || cfg.MDNSName != settings.DefaultMDNSName {
		t.Fatalf("config = %+v, %v", cfg, err)
	}
	cfg, err = e.cl.SetConfig(t.Context(), api.ServiceConfig{MDNSName: "Clinic-East.local"})
	if err != nil || cfg.MDNSName != "clinic-east" {
		t.Fatalf("set = %+v, %v", cfg, err)
	}
	if b, err := os.ReadFile(e.cfgPath); err != nil || !strings.Contains(string(b), `"clinic-east"`) {
		t.Fatalf("config.json = %s, %v", b, err)
	}
	if _, err := e.cl.SetConfig(t.Context(), api.ServiceConfig{MDNSName: "not valid"}); !errors.Is(err, api.ErrInvalidConfig) {
		t.Fatalf("invalid name = %v", err)
	}
	resp, err := rawPost(t, e.sock, "/v1/service/config", `{"mdns_name":"care","extra":1}`)
	if err != nil || resp != http.StatusBadRequest {
		t.Fatalf("unknown field = %d, %v", resp, err)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.applied) != 1 || e.applied[0].MDNSName != "clinic-east" {
		t.Fatalf("applied = %+v", e.applied)
	}
}

func rawPost(t *testing.T, sock, path, body string) (int, error) {
	t.Helper()
	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	}}}
	resp, err := hc.Post("http://x"+path, "application/json", strings.NewReader(body))
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}
