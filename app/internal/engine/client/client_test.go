package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/agent"
	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
)

type env struct {
	srv  *httptest.Server
	fake *agent.FakeExecutor
	cfg  agent.Config
	c    *Client
}

func newEnv(t *testing.T, opts ...Option) *env {
	t.Helper()
	dir := t.TempDir()
	cfg := agent.Config{
		EngineVersion: "0.1.0-test",
		DataRoot:      dir,
		SchemaFile:    filepath.Join(dir, ".data-schema"),
		FilesRoot:     filepath.Join(dir, "files"),
		BackupsRoot:   filepath.Join(dir, "backups"),
		MaxFileBytes:  1 << 10,
		Log:           agent.NewLogRing(100),
	}
	fake := &agent.FakeExecutor{}
	srv := httptest.NewServer(agent.NewServer(cfg, fake))
	t.Cleanup(srv.Close)
	addr := srv.Listener.Addr().String()
	var d net.Dialer
	c := New(func(ctx context.Context) (net.Conn, error) { return d.DialContext(ctx, "tcp", addr) }, opts...)
	t.Cleanup(c.Close)
	return &env{srv: srv, fake: fake, cfg: cfg, c: c}
}

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestStatusAndWaitReady(t *testing.T) {
	e := newEnv(t)
	if err := os.WriteFile(e.cfg.SchemaFile, []byte("3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := e.c.Status(ctxT(t))
	if err != nil {
		t.Fatal(err)
	}
	if !st.DockerReady || st.EngineVersion != "0.1.0-test" || st.DataSchema != 3 {
		t.Fatalf("status = %+v", st)
	}

	e.fake.PingErr = errors.New("docker down")
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	st, err = e.c.WaitReady(ctx, 20*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitReady err = %v, want deadline", err)
	}
	if st.EngineVersion != "0.1.0-test" || st.DockerReady {
		t.Fatalf("last status = %+v", st)
	}
}

func TestWaitReadyBecomesReady(t *testing.T) {
	e := newEnv(t)
	// The agent is not listening yet: requests fail until it comes up.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var addr atomic.Pointer[string]
	addr.Store(new(l.Addr().String()))
	_ = l.Close()
	var d net.Dialer
	c := New(func(ctx context.Context) (net.Conn, error) { return d.DialContext(ctx, "tcp", *addr.Load()) })
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if _, err := c.WaitReady(ctx, 10*time.Millisecond); !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "connect") {
		t.Fatalf("WaitReady err = %v, want deadline wrapping the dial error", err)
	}

	var mu sync.Mutex
	pings := 0
	e.fake.PingErr = nil
	srv := httptest.NewServer(agent.NewServer(e.cfg, &pingAfter{FakeExecutor: e.fake, mu: &mu, n: &pings}))
	defer srv.Close()
	addr.Store(new(srv.Listener.Addr().String()))
	st, err := c.WaitReady(ctxT(t), 5*time.Millisecond)
	if err != nil || !st.DockerReady {
		t.Fatalf("WaitReady = %+v, %v", st, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if pings < 3 {
		t.Fatalf("pings = %d, want >= 3", pings)
	}
}

// pingAfter fails the first two pings.
type pingAfter struct {
	*agent.FakeExecutor
	mu *sync.Mutex
	n  *int
}

func (p *pingAfter) Ping(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	*p.n++
	if *p.n < 3 {
		return errors.New("not yet")
	}
	return nil
}

func TestOpStreamsFramesAndResult(t *testing.T) {
	e := newEnv(t)
	e.fake.Handle = func(_ context.Context, op protocol.Op, em agent.Emitter) (any, error) {
		em.Progress(protocol.Progress{Service: protocol.ServiceBackend, State: "building"})
		em.Log(protocol.LogLine{Line: "step 1/2"})
		return protocol.ImageBuildResult{Digest: digest("a")}, nil
	}
	var frames []protocol.Frame
	var res protocol.ImageBuildResult
	err := e.c.Op(ctxT(t), protocol.ImageBuild{Kind: protocol.BuildFrontendRebuild, Base: digest("c")}, &res, func(f protocol.Frame) { frames = append(frames, f) })
	if err != nil {
		t.Fatal(err)
	}
	if res.Digest != digest("a") {
		t.Fatalf("result = %+v", res)
	}
	if len(frames) != 2 || frames[0].Progress == nil || frames[0].Progress.State != "building" || frames[1].Log == nil || frames[1].Log.Line != "step 1/2" {
		t.Fatalf("frames = %+v", frames)
	}
	calls := e.fake.Calls()
	if len(calls) != 1 || calls[0].Method != "ImageBuild" {
		t.Fatalf("calls = %+v", calls)
	}

	// Pointer ops and a nil result work too.
	if err := e.c.Op(ctxT(t), &protocol.StackDown{}, nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestOpErrorFrame(t *testing.T) {
	e := newEnv(t)
	e.fake.Handle = func(context.Context, protocol.Op, agent.Emitter) (any, error) {
		return nil, &protocol.OpError{Code: protocol.CodeNotInstalled, Message: "no compose file"}
	}
	err := e.c.Op(ctxT(t), protocol.RunMigrations{}, nil, nil)
	var oe *protocol.OpError
	if !errors.As(err, &oe) || oe.Code != protocol.CodeNotInstalled {
		t.Fatalf("err = %v, want OpError not_installed", err)
	}
}

func TestOpRejectedRequests(t *testing.T) {
	e := newEnv(t)
	ctx := ctxT(t)

	// Local validation: nothing reaches the agent.
	var ve *protocol.ValidationError
	if err := e.c.Op(ctx, protocol.ImageBuild{Kind: "bogus"}, nil, nil); !errors.As(err, &ve) || ve.Errors[0].Field != "kind" {
		t.Fatalf("local validation err = %v", err)
	}
	if err := e.c.Op(ctx, protocol.ImageLoad{}, nil, nil); err == nil {
		t.Fatal("Op(image.load) succeeded")
	}

	// 400 from the agent: the password file is checked server-side.
	err := e.c.Op(ctx, protocol.ManageCmd{Command: protocol.ManageCreateSuperuser, Username: "admin", PasswordFile: "pw"}, nil, nil)
	if !errors.As(err, &ve) || len(ve.Errors) != 1 || ve.Errors[0].Field != "password_file" {
		t.Fatalf("400 err = %v", err)
	}

	// 404 for an op the agent does not know.
	err = e.c.Op(ctx, fakeOp{}, nil, nil)
	var ae *APIError
	if !errors.Is(err, ErrUnknownOp) || errors.Is(err, ErrNotFound) || !errors.As(err, &ae) || ae.Op != "no.such" {
		t.Fatalf("404 err = %v", err)
	}
	if len(e.fake.Calls()) != 0 {
		t.Fatalf("calls = %+v", e.fake.Calls())
	}
}

type fakeOp struct{}

func (fakeOp) OpName() string  { return "no.such" }
func (fakeOp) Validate() error { return nil }

func TestBusyAndDetachedMutatingOp(t *testing.T) {
	e := newEnv(t)
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	e.fake.Handle = func(ctx context.Context, op protocol.Op, em agent.Emitter) (any, error) {
		if _, ok := op.(*protocol.StackUp); !ok {
			return nil, nil
		}
		em.Progress(protocol.Progress{Message: "starting"})
		close(started)
		<-release
		finished <- ctx.Err()
		return nil, nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	opErr := make(chan error, 1)
	go func() { opErr <- e.c.Op(ctx, protocol.StackUp{}, nil, nil) }()
	<-started

	err := e.c.Op(ctxT(t), protocol.StackStop{}, nil, nil)
	var ae *APIError
	if !errors.Is(err, ErrBusy) || !errors.As(err, &ae) || ae.Op != protocol.OpStackUp {
		t.Fatalf("busy err = %v", err)
	}
	if err := e.c.Shutdown(ctxT(t)); !errors.Is(err, ErrBusy) {
		t.Fatalf("shutdown err = %v", err)
	}

	cancel()
	if err := <-opErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled op err = %v", err)
	}
	// The op keeps running in the VM after the client went away.
	rep, err := e.c.Diagnostics(ctxT(t))
	if err != nil || rep.Busy != protocol.OpStackUp {
		t.Fatalf("diagnostics = %+v, %v", rep, err)
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatalf("op ctx err = %v, want still live", err)
	}
}

func TestOpCancelReadOnly(t *testing.T) {
	e := newEnv(t)
	cancelled := make(chan struct{})
	e.fake.Handle = func(ctx context.Context, _ protocol.Op, em agent.Emitter) (any, error) {
		em.Log(protocol.LogLine{Line: "first"})
		<-ctx.Done()
		close(cancelled)
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithCancel(t.Context())
	err := e.c.Op(ctx, logsOp, nil, func(protocol.Frame) { cancel() })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("agent did not cancel the op")
	}
}

func TestOpTruncatedStream(t *testing.T) {
	e := newEnv(t)
	e.fake.Handle = func(_ context.Context, _ protocol.Op, em agent.Emitter) (any, error) {
		em.Progress(protocol.Progress{Message: "half way"})
		e.srv.CloseClientConnections()
		return nil, nil
	}
	var got int
	err := e.c.Op(ctxT(t), protocol.StackUp{}, nil, func(protocol.Frame) { got++ })
	if !errors.Is(err, ErrIncompleteStream) {
		t.Fatalf("err = %v, want ErrIncompleteStream", err)
	}
	if got != 1 {
		t.Fatalf("frames = %d", got)
	}
}

func TestOpLineTooLong(t *testing.T) {
	e := newEnv(t, WithMaxLineBytes(4<<10))
	e.fake.Handle = func(_ context.Context, _ protocol.Op, em agent.Emitter) (any, error) {
		em.Log(protocol.LogLine{Line: strings.Repeat("x", 8<<10)})
		return nil, nil
	}
	if err := e.c.Op(ctxT(t), logsOp, nil, nil); !errors.Is(err, ErrLineTooLong) {
		t.Fatalf("err = %v, want ErrLineTooLong", err)
	}
}

func TestLoadImage(t *testing.T) {
	e := newEnv(t)
	e.fake.Handle = func(_ context.Context, _ protocol.Op, em agent.Emitter) (any, error) {
		em.Progress(protocol.Progress{Message: "loaded"})
		return nil, nil
	}
	op := protocol.ImageLoad{Role: protocol.ImageRoleBackend, Digest: digest("b")}
	tar := bytes.Repeat([]byte("tar&data?="), 100_000)
	var frames int
	if err := e.c.LoadImage(ctxT(t), op, bytes.NewReader(tar), func(protocol.Frame) { frames++ }); err != nil {
		t.Fatal(err)
	}
	calls := e.fake.Calls()
	if len(calls) != 1 || calls[0].Method != "ImageLoad" || frames != 1 {
		t.Fatalf("calls = %d, frames = %d", len(calls), frames)
	}
	if got := calls[0].Op.(*protocol.ImageLoad); *got != op {
		t.Fatalf("op = %+v", got)
	}
	if !bytes.Equal(calls[0].Body, tar) {
		t.Fatalf("body: %d bytes, want %d", len(calls[0].Body), len(tar))
	}

	var ve *protocol.ValidationError
	if err := e.c.LoadImage(ctxT(t), protocol.ImageLoad{Role: "x"}, bytes.NewReader(nil), nil); !errors.As(err, &ve) {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadImageCancel(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(t.Context())
	tar := &endlessReader{onRead: cancel}
	if err := e.c.LoadImage(ctx, protocol.ImageLoad{Role: protocol.ImageRoleBackend, Digest: digest("b")}, tar, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

// endlessReader yields data forever, calling onRead after the first read.
type endlessReader struct {
	onRead func()
	once   sync.Once
}

func (r *endlessReader) Read(p []byte) (int, error) {
	defer r.once.Do(r.onRead)
	time.Sleep(time.Millisecond)
	return len(p), nil
}

func TestFiles(t *testing.T) {
	e := newEnv(t)
	ctx := ctxT(t)
	if err := e.c.PutFile(ctx, protocol.FileAreaConfig, "app.env", strings.NewReader("A=1\n"), 4); err != nil {
		t.Fatal(err)
	}
	rc, err := e.c.GetFile(ctx, protocol.FileAreaConfig, "app.env")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(rc)
	_ = rc.Close()
	if buf.String() != "A=1\n" {
		t.Fatalf("got %q", buf.String())
	}
	if err := e.c.DeleteFile(ctx, protocol.FileAreaConfig, "app.env"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.c.GetFile(ctx, protocol.FileAreaConfig, "app.env"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get deleted err = %v", err)
	}
	if err := e.c.DeleteFile(ctx, protocol.FileAreaConfig, "app.env"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete missing err = %v", err)
	}
	if err := e.c.PutFile(ctx, protocol.FileAreaBuild, "big", bytes.NewReader(make([]byte, 2<<10)), 2<<10); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversize err = %v", err)
	}
	if err := e.c.PutFile(ctx, protocol.FileAreaBuild, "big", bytes.NewReader(make([]byte, 2<<10)), -1); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversize unknown length err = %v", err)
	}
	var ve *protocol.ValidationError
	if err := e.c.PutFile(ctx, "etc", "../passwd", strings.NewReader(""), 0); !errors.As(err, &ve) || len(ve.Errors) != 2 {
		t.Fatalf("bad ref err = %v", err)
	}
}

func TestBackups(t *testing.T) {
	e := newEnv(t)
	ctx := ctxT(t)
	data := []byte("pg dump bytes")
	sum := sha256.Sum256(data)
	writeSet := func(id, sha string) {
		dir := filepath.Join(e.cfg.BackupsRoot, id)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "db.dump"), data, 0o600); err != nil {
			t.Fatal(err)
		}
		m := `{"created_at":"2026-01-02T03:04:05Z","files":[{"name":"db.dump","size":13,"sha256":"` + sha + `"}]}`
		if err := os.WriteFile(filepath.Join(dir, agent.ManifestName), []byte(m), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeSet("20260102T030405", hex.EncodeToString(sum[:]))
	writeSet("corrupt", strings.Repeat("0", 64))

	sets, err := e.c.ListBackups(ctx)
	if err != nil || len(sets) != 2 || !sets[0].Complete {
		t.Fatalf("sets = %+v, %v", sets, err)
	}

	var buf bytes.Buffer
	n, err := e.c.ExportBackupFile(ctx, "20260102T030405", "db.dump", &buf)
	if err != nil || n != int64(len(data)) || !bytes.Equal(buf.Bytes(), data) {
		t.Fatalf("export = %d, %v, %q", n, err, buf.Bytes())
	}
	if _, err := e.c.ExportBackupFile(ctx, "corrupt", "db.dump", &bytes.Buffer{}); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("corrupt export err = %v", err)
	}
	if _, err := e.c.ExportBackupFile(ctx, "20260102T030405", "other", &bytes.Buffer{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unlisted export err = %v", err)
	}
	var ve *protocol.ValidationError
	if _, err := e.c.ExportBackupFile(ctx, "..", "db.dump", &bytes.Buffer{}); !errors.As(err, &ve) {
		t.Fatalf("bad id err = %v", err)
	}

	if err := e.c.DeleteBackup(ctx, "corrupt"); err != nil {
		t.Fatal(err)
	}
	if err := e.c.DeleteBackup(ctx, "corrupt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete missing err = %v", err)
	}
	if sets, err := e.c.ListBackups(ctx); err != nil || len(sets) != 1 {
		t.Fatalf("sets = %+v, %v", sets, err)
	}
}

func TestDiagnosticsTimeShutdown(t *testing.T) {
	e := newEnv(t)
	ctx := ctxT(t)
	e.fake.ContainerList = []string{"care-backend-1 running"}
	rep, err := e.c.Diagnostics(ctx)
	if err != nil || rep.EngineVersion != "0.1.0-test" || len(rep.Containers) != 1 {
		t.Fatalf("diagnostics = %+v, %v", rep, err)
	}

	now := time.Date(2026, 10, 18, 1, 2, 3, 0, time.UTC)
	if err := e.c.SetTime(ctx, now); err != nil {
		t.Fatal(err)
	}
	var ve *protocol.ValidationError
	if err := e.c.SetTime(ctx, time.Time{}); !errors.As(err, &ve) {
		t.Fatalf("zero time err = %v", err)
	}
	e.fake.SetTimeErr = errors.New("clock_settime: EPERM")
	var ae *APIError
	if err := e.c.SetTime(ctx, now); !errors.As(err, &ae) || ae.Status != 500 || ae.Code != protocol.CodeOpFailed {
		t.Fatalf("set time failure err = %v", err)
	}

	e.fake.PoweredOff = make(chan struct{})
	if err := e.c.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-e.fake.PoweredOff:
	case <-time.After(5 * time.Second):
		t.Fatal("no PowerOff")
	}
	if err := e.c.Shutdown(ctx); !errors.Is(err, ErrBusy) {
		t.Fatalf("second shutdown err = %v", err)
	}

	var sawTime bool
	for _, c := range e.fake.Calls() {
		if c.Method == "SetTime" && c.Time.Equal(now) {
			sawTime = true
		}
	}
	if !sawTime {
		t.Fatal("SetTime not recorded")
	}
}

var logsOp = protocol.StackLogs{Service: protocol.ServiceBackend, TailLines: 10}

func digest(hex1 string) protocol.Digest {
	return protocol.Digest("sha256:" + strings.Repeat(hex1, 64))
}

func TestExportBackupMissingChecksum(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("data"))
	}))
	defer srv.Close()
	var d net.Dialer
	c := New(func(ctx context.Context) (net.Conn, error) {
		return d.DialContext(ctx, "tcp", srv.Listener.Addr().String())
	})
	_, err := c.ExportBackupFile(ctxT(t), "id1", "db.dump", &bytes.Buffer{})
	if !errors.Is(err, ErrMissingChecksum) || errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("err = %v, want ErrMissingChecksum", err)
	}
}

// TestOpBackToBack checks that the agent's op lock is released before the
// result reaches the client, so the next mutating op is never refused busy.
func TestOpBackToBack(t *testing.T) {
	e := newEnv(t)
	e.fake.Handle = func(context.Context, protocol.Op, agent.Emitter) (any, error) { return nil, nil }
	for i := range 200 {
		if err := e.c.Op(ctxT(t), &protocol.StackDown{}, nil, nil); err != nil {
			t.Fatalf("op %d: %v", i, err)
		}
	}
}
