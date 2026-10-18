package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
)

func (e *recEmitter) snapshot() []protocol.LogLine {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.logs)
}

func (e *recEmitter) lines(kind string) []string {
	var out []string
	for _, l := range e.snapshot() {
		switch {
		case kind == "gap" && l.Stream == protocol.LogStreamGap:
			out = append(out, l.Line)
		case kind != "gap" && l.Stream != protocol.LogStreamGap:
			out = append(out, l.Line)
		}
	}
	return out
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func appendFile(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func startSystem(t *testing.T, files map[string]string, op protocol.LogsSystem) (*recEmitter, func() error) {
	t.Helper()
	d := &DockerExecutor{LogFiles: files, LogPoll: 5 * time.Millisecond}
	em := &recEmitter{}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- d.LogsSystem(ctx, op, em) }()
	stop := func() error {
		cancel()
		return <-done
	}
	t.Cleanup(func() { cancel() })
	return em, stop
}

func TestLogsSystemFollowsRotationAndTruncation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the guest is Linux; Windows can't rename a file another handle has open")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "messages")
	appendFile(t, path, "one\ntwo\n")
	em, stop := startSystem(t, map[string]string{protocol.LogSourceMessages: path}, protocol.LogsSystem{})
	waitFor(t, "initial lines", func() bool { return len(em.lines("")) == 2 })

	appendFile(t, path, "three\npart")
	waitFor(t, "appended line", func() bool { return len(em.lines("")) == 3 })
	appendFile(t, path, "ial\n")
	waitFor(t, "completed line", func() bool { return len(em.lines("")) == 4 })

	if err := os.Rename(path, path+".0"); err != nil {
		t.Fatal(err)
	}
	appendFile(t, path+".0", "last before rotation\n")
	appendFile(t, path, "first after rotation\n")
	waitFor(t, "rotated lines", func() bool { return len(em.lines("")) == 6 })

	if err := os.Truncate(path, 0); err != nil {
		t.Fatal(err)
	}
	appendFile(t, path, "x\n")
	waitFor(t, "line after truncation", func() bool { return len(em.lines("")) == 7 })

	if err := stop(); !errors.Is(err, context.Canceled) {
		t.Fatalf("LogsSystem returned %v", err)
	}
	want := []string{"one", "two", "three", "partial", "last before rotation", "first after rotation", "x"}
	if got := em.lines(""); !slices.Equal(got, want) {
		t.Fatalf("lines = %q, want %q", got, want)
	}
	if gaps := em.lines("gap"); len(gaps) != 1 || !strings.Contains(gaps[0], "emptied") {
		t.Fatalf("gaps = %q", gaps)
	}
	for _, l := range em.snapshot() {
		if l.Stream != protocol.LogStreamGap {
			if _, ok := protocol.ParseFileCursor(l.Cursor); !ok || l.Source != protocol.LogSourceMessages {
				t.Fatalf("line %+v has no cursor", l)
			}
		}
	}
}

func TestLogsSystemResumesFromRotatedFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the guest is Linux; Windows can't rename a file another handle has open")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "messages")
	appendFile(t, path, "copied\nnot yet copied\n")
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	cursor := protocol.FileCursor{Inode: inodeOf(fi), Offset: int64(len("copied\n"))}
	if err := os.Rename(path, path+".0"); err != nil {
		t.Fatal(err)
	}
	appendFile(t, path, "new file\n")
	em, stop := startSystem(t, map[string]string{protocol.LogSourceMessages: path},
		protocol.LogsSystem{Cursors: map[string]protocol.FileCursor{protocol.LogSourceMessages: cursor}})
	waitFor(t, "resumed lines", func() bool { return len(em.lines("")) == 2 })
	_ = stop()
	if got := em.lines(""); !slices.Equal(got, []string{"not yet copied", "new file"}) {
		t.Fatalf("lines = %q", got)
	}
	if gaps := em.lines("gap"); len(gaps) != 0 {
		t.Fatalf("unexpected gaps %q", gaps)
	}
}

func TestLogsSystemReportsLostCursor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "docker.log")
	appendFile(t, path, "after\n")
	em, stop := startSystem(t, map[string]string{protocol.LogSourceDocker: path},
		protocol.LogsSystem{Cursors: map[string]protocol.FileCursor{protocol.LogSourceDocker: {Inode: 1, Offset: 99}}})
	waitFor(t, "current file", func() bool { return len(em.lines("")) == 1 })
	_ = stop()
	if gaps := em.lines("gap"); len(gaps) != 1 || !strings.Contains(gaps[0], "rotated") {
		t.Fatalf("gaps = %q", gaps)
	}
}

func TestLogsSystemTailStartsAtALineBoundary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "care-engine-agent.log")
	appendFile(t, path, strings.Repeat("old line\n", 50)+"newest line\n")
	em, stop := startSystem(t, map[string]string{protocol.LogSourceAgent: path}, protocol.LogsSystem{TailBytes: 25})
	waitFor(t, "tail", func() bool { return len(em.lines("")) >= 2 })
	_ = stop()
	if got := em.lines(""); !slices.Equal(got, []string{"old line", "old line", "newest line"}[3-len(got):]) {
		t.Fatalf("lines = %q", got)
	}
}

func TestLogsContainers(t *testing.T) {
	since := time.Date(2026, 10, 18, 5, 0, 0, 0, time.UTC)
	r := &fakeRunner{respond: func(c Command) fakeRun {
		switch {
		case c.Args[1] == "ps":
			return fakeRun{stdout: "id-db\tcare-db-1\trunning\nid-mig\tcare-migrate-1\texited\nid-bad\tbad name\trunning\n"}
		case slices.Contains(c.Args, "id-db"):
			return fakeRun{
				stdout: "2026-10-18T05:00:00Z already copied\n2026-10-18T05:00:01.5Z ready to accept connections\n",
				stderr: "2026-10-18T05:00:02Z WARNING: low disk\n",
			}
		case slices.Contains(c.Args, "id-mig"):
			return fakeRun{stdout: "2026-10-18T04:00:00Z migrated\n"}
		}
		return fakeRun{}
	}}
	d := &DockerExecutor{Run: r.run, Now: func() time.Time { return since }, ContainerPoll: 5 * time.Millisecond}
	em := &recEmitter{}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- d.LogsContainers(ctx, protocol.LogsContainers{Since: map[string]time.Time{"care-db-1": since}}, em)
	}()
	waitFor(t, "several polls", func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return len(r.cmds) > 12
	})
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("LogsContainers returned %v", err)
	}
	var got []string
	for _, l := range em.snapshot() {
		got = append(got, l.Container+"|"+l.Stream+"|"+l.Line)
	}
	slices.Sort(got)
	want := []string{
		"care-db-1|stderr|WARNING: low disk",
		"care-db-1|stdout|ready to accept connections",
		"care-migrate-1|stdout|migrated",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("lines = %q, want %q", got, want)
	}
	var first, follow, stopped []string
	for _, a := range r.argv() {
		if a[1] != "logs" {
			continue
		}
		switch a[len(a)-1] {
		case "id-db":
			if first == nil {
				first = a
			}
			follow = a
		case "id-mig":
			stopped = a
		case "id-bad":
			t.Fatalf("followed a container with an invalid name: %q", a)
		}
	}
	if !slices.Equal(first, []string{"docker", "logs", "--timestamps", "--follow", "--since", "2026-10-18T05:00:00Z", "id-db"}) {
		t.Fatalf("first follow = %q", first)
	}
	if !slices.Equal(follow, []string{"docker", "logs", "--timestamps", "--follow", "--since", "2026-10-18T05:00:02Z", "id-db"}) {
		t.Fatalf("later follow = %q", follow)
	}
	if !slices.Equal(stopped, []string{"docker", "logs", "--timestamps", "--tail", "200", "id-mig"}) {
		t.Fatalf("stopped container = %q", stopped)
	}
}

func TestLogsContainersDockerDown(t *testing.T) {
	r := &fakeRunner{respond: func(c Command) fakeRun {
		return fakeRun{stderr: "Cannot connect to the Docker daemon", err: errors.New("exit status 1")}
	}}
	d := &DockerExecutor{Run: r.run, ContainerPoll: 5 * time.Millisecond}
	em := &recEmitter{}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- d.LogsContainers(ctx, protocol.LogsContainers{}, em) }()
	waitFor(t, "polls", func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return len(r.cmds) > 3
	})
	cancel()
	<-done
	if gaps := em.lines("gap"); len(gaps) != 1 || !strings.Contains(gaps[0], "Cannot connect") {
		t.Fatalf("gaps = %q", gaps)
	}
}

func TestLogOpsAreReadOnly(t *testing.T) {
	for _, op := range []string{protocol.OpLogsSystem, protocol.OpLogsContainers} {
		if !readOnlyOps[op] {
			t.Errorf("%s takes the mutating-op lock", op)
		}
	}
}
