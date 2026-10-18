package logdir

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func newDir(t *testing.T) *Dir {
	t.Helper()
	d, err := OpenOrCreate(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestDefault(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	for _, tc := range []struct {
		goos string
		env  map[string]string
		want string
	}{
		{"darwin", nil, filepath.Join("/home/u", "Library", "Logs", "care-clinic")},
		{"windows", nil, filepath.Join("/home/u", "AppData", "Local", "care-clinic", "logs")},
		{"windows", map[string]string{"LOCALAPPDATA": "/l"}, filepath.Join("/l", "care-clinic", "logs")},
		{"linux", nil, filepath.Join("/home/u", ".local", "state", "care-clinic")},
		{"linux", map[string]string{"XDG_STATE_HOME": "/s"}, filepath.Join("/s", "care-clinic")},
	} {
		if got := Default(tc.goos, "/home/u", env(tc.env)); got != tc.want {
			t.Errorf("%s %v: %s, want %s", tc.goos, tc.env, got, tc.want)
		}
	}
	if Default("darwin", "", env(nil)) != "" {
		t.Error("no home must give no folder")
	}
}

func TestRotatedName(t *testing.T) {
	for in, want := range map[string]string{"service.log": "service.2.log", "containers/care-db-1.log": "containers/care-db-1.2.log", "plain": "plain.2"} {
		if got := RotatedName(in, 2); got != want {
			t.Errorf("RotatedName(%q) = %q, want %q", in, got, want)
		}
	}
	if rotationIndex("care-db-1.log") != 0 || rotationIndex("guest-system.2.log") != 2 || rotationIndex("x.0.log") != 0 {
		t.Error("rotationIndex")
	}
}

func TestWriterRotatesKeepingTwo(t *testing.T) {
	d := newDir(t)
	w := NewWriter(d, "service.log", 20, 2)
	for _, s := range []string{"aaaaaaaaa\n", "bbbbbbbbb\n", "ccccccccc\n", "ddddddddd\n", "eeeeeeeee\n", "fffffffff\n", "ggggggggg\n"} {
		if _, err := w.Write([]byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	p := func(n string) string { return filepath.Join(d.Path(), n) }
	if got := read(t, p("service.log")); got != "ggggggggg\n" {
		t.Fatalf("current = %q", got)
	}
	if got := read(t, p("service.1.log")); got != "eeeeeeeee\nfffffffff\n" {
		t.Fatalf("service.1.log = %q", got)
	}
	if got := read(t, p("service.2.log")); got != "ccccccccc\nddddddddd\n" {
		t.Fatalf("service.2.log = %q", got)
	}
	if _, err := os.Stat(p("service.3.log")); !os.IsNotExist(err) {
		t.Fatalf("kept a third rotated file: %v", err)
	}
}

func TestOpenRefusesSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(link, nil); err == nil {
		t.Fatal("opened a symlinked log folder")
	}
	d, err := Open(real, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	outside := filepath.Join(base, "sudoers")
	if err := os.WriteFile(outside, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(real, "service.log")); err != nil {
		t.Fatal(err)
	}
	if f, err := d.OpenAppend("service.log"); err == nil {
		_ = f.Close()
		t.Fatal("followed a symlink out of the log folder")
	}
	if _, err := d.OpenAppend("../escape.log"); err == nil {
		t.Fatal("wrote outside the log folder")
	}
	if got := read(t, outside); got != "keep\n" {
		t.Fatalf("outside file changed: %q", got)
	}
}

func TestPrepare(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "Library", "Logs", "care-clinic")
	if err := Prepare(home, dir, nil); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("log folder not created: %v", err)
	}
	if err := Prepare(home, dir, nil); err != nil {
		t.Fatalf("second Prepare: %v", err)
	}
	if err := Prepare(home, filepath.Join(home, "..", "elsewhere"), nil); err == nil {
		t.Fatal("accepted a folder outside the home folder")
	}
	if runtime.GOOS == "windows" {
		return
	}
	other := t.TempDir()
	home2 := t.TempDir()
	if err := os.Symlink(other, filepath.Join(home2, "Library")); err != nil {
		t.Fatal(err)
	}
	if err := Prepare(home2, filepath.Join(home2, "Library", "Logs", "care-clinic"), nil); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("followed a symlinked component: %v", err)
	}
	if _, err := os.Stat(filepath.Join(other, "Logs")); !os.IsNotExist(err) {
		t.Fatal("created folders through the symlink")
	}
}

func writeAged(t *testing.T, d *Dir, name string, size int, age time.Duration) {
	t.Helper()
	p := filepath.Join(d.Path(), filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, bytes.Repeat([]byte("x"), size), 0o644); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(p, when, when); err != nil {
		t.Fatal(err)
	}
}

func exists(d *Dir, name string) (int64, bool) {
	fi, err := os.Stat(filepath.Join(d.Path(), filepath.FromSlash(name)))
	if err != nil {
		return 0, false
	}
	return fi.Size(), true
}

func TestSweepExpiresOldFiles(t *testing.T) {
	d := newDir(t)
	day := 24 * time.Hour
	writeAged(t, d, "service.log", 10, 8*day)
	writeAged(t, d, "service.1.log", 10, 8*day)
	writeAged(t, d, "guest-system.log", 10, day)
	writeAged(t, d, "containers/care-old-1.log", 10, 9*day)
	writeAged(t, d, "containers/care-db-1.log", 10, 9*day)
	writeAged(t, d, "notes.txt", 10, 30*day)
	actions, err := d.Sweep(SweepOptions{InUse: func(rel string) bool { return rel == "containers/care-db-1.log" }})
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 4 {
		t.Fatalf("actions = %+v", actions)
	}
	if n, ok := exists(d, "service.log"); !ok || n != 0 {
		t.Errorf("service.log: %d %v, want emptied", n, ok)
	}
	if _, ok := exists(d, "service.1.log"); ok {
		t.Error("old rotated file kept")
	}
	if _, ok := exists(d, "containers/care-old-1.log"); ok {
		t.Error("log of a container gone for 9 days kept")
	}
	if n, ok := exists(d, "containers/care-db-1.log"); !ok || n != 0 {
		t.Errorf("open container log: %d %v, want emptied", n, ok)
	}
	if n, _ := exists(d, "guest-system.log"); n != 10 {
		t.Error("recent file touched")
	}
	if n, _ := exists(d, "notes.txt"); n != 10 {
		t.Error("non-log file touched")
	}
}

func TestSweepEnforcesBudgetOldestFirst(t *testing.T) {
	d := newDir(t)
	writeAged(t, d, "care-log.log", 400, time.Minute)
	writeAged(t, d, "care-log.1.log", 400, 2*time.Hour)
	writeAged(t, d, "guest-system.1.log", 400, time.Hour)
	writeAged(t, d, "service.log", 400, 3*time.Hour)
	writeAged(t, d, "containers/care-db-1.log", 400, 30*time.Minute)
	actions, err := d.Sweep(SweepOptions{Budget: 1000})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, a := range actions {
		got = append(got, a.Action+" "+a.File)
	}
	want := []string{"deleted care-log.1.log", "deleted guest-system.1.log", "deleted containers/care-db-1.log"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("actions = %q, want %q", got, want)
	}
	actions, _ = d.Sweep(SweepOptions{Budget: 500})
	if len(actions) != 1 || actions[0].File != "service.log" || actions[0].Action != "emptied" {
		t.Fatalf("second sweep = %+v", actions)
	}
	if n, _ := exists(d, "care-log.log"); n != 400 {
		t.Error("newest file touched")
	}
}

type gate struct {
	mu   sync.Mutex
	out  bytes.Buffer
	open chan struct{}
}

func (g *gate) Write(p []byte) (int, error) {
	<-g.open
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.out.Write(p)
}

func TestAsyncNeverBlocksAndRecordsDrops(t *testing.T) {
	g := &gate{open: make(chan struct{})}
	var dropped int64
	a := NewAsync(g, 10, func(n int64) { dropped += n })
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 100 {
			_, _ = io.WriteString(a, "12345")
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("writes blocked on a stalled disk")
	}
	close(g.open)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	g.mu.Lock()
	out := g.out.String()
	g.mu.Unlock()
	if !strings.Contains(out, "bytes of output were dropped") || dropped == 0 {
		t.Fatalf("drop not recorded: %q (%d)", out, dropped)
	}
	if kept := strings.Count(out, "12345"); int64(kept*5)+dropped != 500 {
		t.Fatalf("kept %d writes and dropped %d bytes of 500", kept, dropped)
	}
}
