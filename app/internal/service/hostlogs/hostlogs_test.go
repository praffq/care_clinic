package hostlogs

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/agent"
	"github.com/ohcnetwork/care_desktop/app/internal/engine/client"
	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/logdir"
)

func validUser() string {
	if runtime.GOOS == "windows" {
		return "S-1-5-21-1-2-3-1001"
	}
	return "nurse"
}

func TestResolve(t *testing.T) {
	root := t.TempDir()
	fallback := filepath.Join(root, "logs")

	resolve := func() (*logdir.Dir, []string, error) {
		d, notes, err := Resolve(root, fallback)
		if d != nil {
			t.Cleanup(func() { d.Close() })
		}
		return d, notes, err
	}

	d, notes, err := resolve()
	if err != nil || d.Path() != fallback || len(notes) != 1 || !strings.Contains(notes[0], UserFile) {
		t.Fatalf("no log-user: %v %v %q", d, err, notes)
	}

	if err := os.WriteFile(filepath.Join(root, UserFile), []byte("../../etc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if d, notes, _ := resolve(); d.Path() != fallback || len(notes) != 1 || !strings.Contains(notes[0], "not a valid user") {
		t.Fatalf("invalid log-user: %v %q", d.Path(), notes)
	}

	if err := WriteUser(root, validUser()); err != nil {
		t.Fatal(err)
	}
	userDir := filepath.Join(t.TempDir(), "Library", "Logs", "care-clinic")
	defer func(f func(string) (string, *logdir.Owner, error)) { forUser = f }(forUser)
	forUser = func(u string) (string, *logdir.Owner, error) {
		if u != validUser() {
			t.Fatalf("resolved %q", u)
		}
		return userDir, nil, os.MkdirAll(userDir, 0o755)
	}
	if d, notes, err := resolve(); err != nil || d.Path() != userDir || len(notes) != 0 {
		t.Fatalf("user folder: %v %v %q", d.Path(), err, notes)
	}

	forUser = func(string) (string, *logdir.Owner, error) { return "", nil, errors.New("no such user") }
	if d, notes, _ := resolve(); d.Path() != fallback || len(notes) != 1 || !strings.Contains(notes[0], "no such user") {
		t.Fatalf("unknown user: %v %q", d.Path(), notes)
	}
}

type fakeEngine struct {
	mu       sync.Mutex
	calls    map[string]int
	ops      []protocol.Op
	handlers map[string]func(n int, ctx context.Context, op protocol.Op, em agent.Emitter) error
}

func (f *fakeEngine) handle(ctx context.Context, op protocol.Op, em agent.Emitter) (any, error) {
	f.mu.Lock()
	f.calls[op.OpName()]++
	n := f.calls[op.OpName()]
	f.ops = append(f.ops, op)
	h := f.handlers[op.OpName()]
	f.mu.Unlock()
	if h == nil {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return nil, h(n, ctx, op, em)
}

func (f *fakeEngine) count(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[name]
}

func (f *fakeEngine) nth(name string, n int) protocol.Op {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, op := range f.ops {
		if op.OpName() == name {
			if n--; n == 0 {
				return op
			}
		}
	}
	return nil
}

func newAgent(t *testing.T, fe *fakeEngine, wrap func(http.Handler) http.Handler) *client.Client {
	t.Helper()
	dir := t.TempDir()
	fake := &agent.FakeExecutor{Handle: fe.handle}
	var h http.Handler = agent.NewServer(agent.Config{
		EngineVersion: "0.1.7",
		DataRoot:      dir,
		SchemaFile:    filepath.Join(dir, ".data-schema"),
		FilesRoot:     filepath.Join(dir, "files"),
		BackupsRoot:   filepath.Join(dir, "backups"),
		Log:           agent.NewLogRing(10),
	}, fake)
	if wrap != nil {
		h = wrap(h)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	addr := srv.Listener.Addr().String()
	var d net.Dialer
	c := client.New(func(ctx context.Context) (net.Conn, error) { return d.DialContext(ctx, "tcp", addr) })
	t.Cleanup(c.Close)
	return c
}

func newCollector(t *testing.T, ag Agent, root string) (*Collector, *logdir.Dir) {
	t.Helper()
	d, err := logdir.OpenOrCreate(filepath.Join(root, "user-logs"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return New(Config{
		Dir:        d,
		Agent:      ag,
		StatePath:  filepath.Join(root, StateFile),
		Notes:      []string{"startup note"},
		BackoffMin: 10 * time.Millisecond,
		BackoffMax: 20 * time.Millisecond,
		ReadyPoll:  10 * time.Millisecond,
		SaveEvery:  10 * time.Millisecond,
	}), d
}

func fileText(d *logdir.Dir, name string) string {
	b, _ := os.ReadFile(filepath.Join(d.Path(), filepath.FromSlash(name)))
	return string(b)
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestCollectorResumesAfterInterruption(t *testing.T) {
	t0 := time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC)
	fe := &fakeEngine{calls: map[string]int{}}
	fe.handlers = map[string]func(int, context.Context, protocol.Op, agent.Emitter) error{
		protocol.OpLogsSystem: func(n int, ctx context.Context, op protocol.Op, em agent.Emitter) error {
			em.Progress(protocol.Progress{State: protocol.LogStateFollowing})
			if n == 1 {
				em.Log(protocol.LogLine{Source: protocol.LogSourceMessages, Line: "kernel: boot ok", Cursor: "7:16"})
				em.Log(protocol.LogLine{Source: protocol.LogSourceDocker, Stream: protocol.LogStreamGap, Line: "docker.log was emptied"})
				return errors.New("vsock connection reset")
			}
			em.Log(protocol.LogLine{Source: protocol.LogSourceMessages, Line: "dockerd: started", Cursor: "7:33"})
			<-ctx.Done()
			return ctx.Err()
		},
		protocol.OpLogsContainers: func(n int, ctx context.Context, op protocol.Op, em agent.Emitter) error {
			em.Progress(protocol.Progress{State: protocol.LogStateFollowing})
			if n == 1 {
				em.Log(protocol.LogLine{Container: "care-db-1", Stream: "stdout", Time: t0, Line: "database system is ready"})
				em.Log(protocol.LogLine{Container: "care-backend-1", Stream: "stderr", Time: t0.Add(time.Second), Line: "Traceback"})
				em.Log(protocol.LogLine{Container: "../escape", Stream: "stdout", Time: t0, Line: "nope"})
				return errors.New("engine restarted")
			}
			em.Log(protocol.LogLine{Container: "care-db-1", Stream: "stdout", Time: t0.Add(time.Minute), Line: "checkpoint complete"})
			<-ctx.Done()
			return ctx.Err()
		},
	}
	root := t.TempDir()
	c, d := newCollector(t, newAgent(t, fe, nil), root)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	waitUntil(t, "second connections", func() bool {
		return strings.Contains(fileText(d, SystemLog), "dockerd: started") && strings.Contains(fileText(d, "containers/care-db-1.log"), "checkpoint")
	})
	cancel()
	<-done

	if got := fileText(d, SystemLog); got != "messages: kernel: boot ok\nmessages: dockerd: started\n" {
		t.Fatalf("%s = %q", SystemLog, got)
	}
	if got := fileText(d, "containers/care-db-1.log"); got != "2026-10-19T08:00:00Z database system is ready\n2026-10-19T08:01:00Z checkpoint complete\n" {
		t.Fatalf("care-db-1.log = %q", got)
	}
	if got := fileText(d, "containers/care-backend-1.log"); got != "2026-10-19T08:00:01Z stderr: Traceback\n" {
		t.Fatalf("care-backend-1.log = %q", got)
	}
	if _, err := os.Stat(filepath.Join(d.Path(), "escape.log")); !os.IsNotExist(err) {
		t.Fatal("wrote a file for an invalid container name")
	}

	sys := fe.nth(protocol.OpLogsSystem, 2).(*protocol.LogsSystem)
	if sys.Cursors[protocol.LogSourceMessages] != (protocol.FileCursor{Inode: 7, Offset: 16}) {
		t.Fatalf("second system stream resumed from %+v", sys.Cursors)
	}
	ctr := fe.nth(protocol.OpLogsContainers, 2).(*protocol.LogsContainers)
	if !ctr.Since["care-db-1"].Equal(t0) || !ctr.Since["care-backend-1"].Equal(t0.Add(time.Second)) {
		t.Fatalf("second container stream resumed from %+v", ctr.Since)
	}

	notes := fileText(d, CollectorLog)
	for _, want := range []string{
		"startup note",
		"collecting CARE Clinic logs into " + d.Path(),
		"guest docker log: docker.log was emptied",
		"guest system logs: interrupted",
		"vsock connection reset",
		"guest system logs: reconnected after",
		"container logs: interrupted",
		"container logs: reconnected after",
	} {
		if !strings.Contains(notes, want) {
			t.Errorf("collector.log lacks %q:\n%s", want, notes)
		}
	}

	var saved positions
	b, err := os.ReadFile(filepath.Join(root, StateFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.System[protocol.LogSourceMessages].Offset != 33 || !saved.Containers["care-db-1"].Since.Equal(t0.Add(time.Minute)) {
		t.Fatalf("saved state = %s", b)
	}

	fe2 := &fakeEngine{calls: map[string]int{}}
	c2, _ := newCollector(t, newAgent(t, fe2, nil), root)
	ctx2, cancel2 := context.WithCancel(t.Context())
	done2 := make(chan struct{})
	go func() { c2.Run(ctx2); close(done2) }()
	waitUntil(t, "restart streams", func() bool {
		return fe2.count(protocol.OpLogsSystem) > 0 && fe2.count(protocol.OpLogsContainers) > 0
	})
	cancel2()
	<-done2
	if s := fe2.nth(protocol.OpLogsSystem, 1).(*protocol.LogsSystem); s.Cursors[protocol.LogSourceMessages].Offset != 33 {
		t.Fatalf("after a service restart the system stream resumed from %+v", s.Cursors)
	}
}

func TestCollectorPausesWhileEngineIsDown(t *testing.T) {
	fe := &fakeEngine{calls: map[string]int{}}
	var mu sync.Mutex
	ready := false
	root := t.TempDir()
	d, err := logdir.OpenOrCreate(filepath.Join(root, "logs"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	c := New(Config{
		Dir:       d,
		Agent:     newAgent(t, fe, nil),
		StatePath: filepath.Join(root, StateFile),
		Ready: func() bool {
			mu.Lock()
			defer mu.Unlock()
			return ready
		},
		ReadyPoll: 5 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	waitUntil(t, "pause note", func() bool { return strings.Contains(fileText(d, CollectorLog), "collection is paused") })
	if fe.count(protocol.OpLogsSystem) != 0 {
		t.Fatal("streamed while the engine was down")
	}
	mu.Lock()
	ready = true
	mu.Unlock()
	waitUntil(t, "streams", func() bool { return fe.count(protocol.OpLogsSystem) == 1 && fe.count(protocol.OpLogsContainers) == 1 })
	waitUntil(t, "resume note", func() bool { return strings.Contains(fileText(d, CollectorLog), "resume from their saved positions") })
	cancel()
	<-done
}

func TestCollectorSweepNotesRetention(t *testing.T) {
	root := t.TempDir()
	c, d := newCollector(t, nil, root)
	p := filepath.Join(d.Path(), "guest-system.1.log")
	if err := os.WriteFile(p, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	c.Sweep()
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("old rotated log kept")
	}
	if !strings.Contains(fileText(d, CollectorLog), "retention: deleted guest-system.1.log") {
		t.Fatalf("collector.log:\n%s", fileText(d, CollectorLog))
	}
}
