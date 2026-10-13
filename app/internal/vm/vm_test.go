package vm

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestWindowsToGuest(t *testing.T) {
	cases := map[string]string{
		`C:\Users\Asha\care clinic`: "/c/Users/Asha/care clinic",
		`d:/backups/`:               "/d/backups",
		`E:\`:                       "/e",
	}
	for in, want := range cases {
		if got, ok := windowsToGuest(in); !ok || got != want {
			t.Errorf("%q -> %q %v, want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"/usr/x", "relative", `\\server\share`, "1:/x"} {
		if _, ok := windowsToGuest(in); ok {
			t.Errorf("%q should not map", in)
		}
	}
}

func TestRewriteDockerArgs(t *testing.T) {
	args := []string{"run", "--rm", "-v", `C:\care\backups:/backups:ro`,
		"--mount", `type=bind,source=C:\care\key.age,target=/key,readonly`,
		"--volume=care_pg:/var/lib/postgresql", "img", "sh", "-c", "-v x"}
	got, paths := RewriteDockerArgs(args)
	want := []string{"run", "--rm", "-v", "/c/care/backups:/backups:ro",
		"--mount", "type=bind,source=/c/care/key.age,target=/key,readonly",
		"--volume=care_pg:/var/lib/postgresql", "img", "sh", "-c", "-v x"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q", got)
	}

	if !reflect.DeepEqual(paths, []string{`C:\care\backups`, `C:\care\key.age`}) {
		t.Fatalf("paths %q", paths)
	}
	compose := []string{"compose", "down", "-v"}
	if got, p := RewriteDockerArgs(compose); !reflect.DeepEqual(got, compose) || p != nil {
		t.Fatalf("compose changed: %q", got)
	}
	cr := []string{"--log-level", "warn", "container", "create", "-v", `C:\x:/x`, "img"}
	if got, _ := RewriteDockerArgs(cr); got[5] != "/c/x:/x" {
		t.Fatalf("container create: %q", got)
	}
}

func TestRewriteMountPreservesCSVQuotedWindowsSource(t *testing.T) {
	value := `type=bind,"source=C:\backups\a,b",target=/restore`
	args, paths := RewriteDockerArgs([]string{"run", "--mount", value, "image"})
	if !reflect.DeepEqual(paths, []string{`C:\backups\a,b`}) {
		t.Fatalf("sync paths = %#v", paths)
	}
	want := `type=bind,"source=/c/backups/a,b",target=/restore`
	if len(args) != 4 || args[2] != want {
		t.Fatalf("rewritten mount = %#v, want field %q", args, want)
	}
}

func TestCommandLine(t *testing.T) {
	m := Manifest{System: "/nix/store/abc-system", Cmdline: "console=hvc0 console=ttyS0 reboot=t panic=1 loglevel=4"}
	if got := m.commandLine("ttyS0"); got != "console=ttyS0 reboot=t panic=1 loglevel=4 init=/nix/store/abc-system/init" {
		t.Fatal(got)
	}
}

func TestConfigDefaults(t *testing.T) {
	c := Config{}.withDefaults(8)
	if c.CPUs != 4 || c.MemoryMB != 4096 || c.DiskGB != 64 {
		t.Fatalf("%+v", c)
	}
	if c := (Config{CPUs: 16, MemoryMB: 512}).withDefaults(2); c.CPUs != 2 || c.MemoryMB != minMemoryMB {
		t.Fatalf("%+v", c)
	}
}

func TestDarwinBundleMinimumSupportsVirtualizationDataDiskID(t *testing.T) {
	data, err := os.ReadFile("../../build/darwin/Info.plist")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "<string>12.3.0</string>") {
		t.Fatal("macOS bundle minimum is below the VM data disk identifier requirement")
	}
}

func writeTarGz(t *testing.T, p string, files map[string]string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		_ = tw.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: int64(len(files[n])), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(files[n]))
	}
	_ = tw.Close()
	_ = gz.Close()
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func sum(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

func TestStagedEngineChecksAndExtracts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("LOCALAPPDATA", home)
	files := engineFiles("0.1.0", "k")
	files["../evil"] = "x"
	arc := filepath.Join(t.TempDir(), "engine.tar.gz")
	writeTarGz(t, arc, files)
	installEngineForTest(t, arc)
	m, err := Installed()
	if err != nil || m.Version != "0.1.0" {
		t.Fatalf("%+v %v", m, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(engineDir()), "evil")); !os.IsNotExist(err) {
		t.Fatal("path traversal entry was extracted")
	}
	if err := prepareDataDisk(context.Background(), m, 1); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(dataDisk()); fi.Size() != 1<<30 {
		t.Fatalf("data disk size %d", fi.Size())
	}

	files["kernel"] = "tampered"
	writeTarGz(t, arc, files)
	if _, err := StageEngineUpdate(toolFiles(t, arc, "v2")); err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("tampered bundle accepted: %v", err)
	}
}

// fakeGuest is an in-memory guest file system.
type fakeGuest struct{ files map[string]fakeFile }

type fakeFile struct {
	data  string
	mtime int64
}

func (f *fakeGuest) list(_ context.Context, root string) ([]guestEntry, error) {
	var out []guestEntry
	for p, ff := range f.files {
		if strings.HasPrefix(p, root+"/") {
			out = append(out, guestEntry{Path: strings.TrimPrefix(p, root+"/"), Size: int64(len(ff.data)), MtimeNs: ff.mtime})
		}
	}
	return out, nil
}

func (f *fakeGuest) get(_ context.Context, p string) (io.ReadCloser, int64, error) {
	ff, ok := f.files[p]
	if !ok {
		return nil, 0, os.ErrNotExist
	}
	return io.NopCloser(strings.NewReader(ff.data)), ff.mtime, nil
}

func (f *fakeGuest) put(_ context.Context, p string, r io.Reader, _ uint32, mt time.Time) error {
	b, _ := io.ReadAll(r)
	f.files[p] = fakeFile{string(b), mt.UnixNano()}
	return nil
}

func (f *fakeGuest) remove(_ context.Context, p string) error { delete(f.files, p); return nil }

func TestSyncThreeWay(t *testing.T) {
	root := t.TempDir()
	g := &fakeGuest{files: map[string]fakeFile{}}
	groot := "/guest/install"
	s := &syncer{g: g, logf: t.Logf, state: t.TempDir(), roots: func() []string { return []string{root} },
		toGst: func(string) string { return groot }, skipFn: skipSync}
	ctx := context.Background()
	write := func(rel, data string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte(data), 0o644)
	}
	write("Caddyfile", "one")
	write("scripts/backup.sh", "#!/bin/sh")
	write(".git/HEAD", "ref")
	if err := s.run(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if g.files[groot+"/Caddyfile"].data != "one" || g.files[groot+"/scripts/backup.sh"].data == "" {
		t.Fatalf("push: %+v", g.files)
	}
	if _, ok := g.files[groot+"/.git/HEAD"]; ok {
		t.Fatal(".git should be skipped")
	}

	// The guest writes a new file and changes nothing else: it comes back.
	g.files[groot+"/backup-state/last.json"] = fakeFile{"{}", time.Now().UnixNano()}
	// The host deletes a file: the guest copy goes too.
	_ = os.Remove(filepath.Join(root, "scripts", "backup.sh"))
	if err := s.run(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(root, "backup-state", "last.json")); err != nil || string(b) != "{}" {
		t.Fatalf("pull: %q %v", b, err)
	}
	if _, ok := g.files[groot+"/scripts/backup.sh"]; ok {
		t.Fatal("host delete not propagated")
	}

	// The guest deletes a file the host did not touch: the host copy goes.
	delete(g.files, groot+"/backup-state/last.json")
	// Both change the same file: the host wins.
	write("Caddyfile", "host-two")
	g.files[groot+"/Caddyfile"] = fakeFile{"guest-two", time.Now().Add(time.Hour).UnixNano()}
	if err := s.run(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "backup-state", "last.json")); !os.IsNotExist(err) {
		t.Fatal("guest delete not propagated")
	}
	if g.files[groot+"/Caddyfile"].data != "host-two" {
		t.Fatalf("conflict: %q", g.files[groot+"/Caddyfile"].data)
	}

	// A steady state does no work.
	before := len(g.files)
	if err := s.run(ctx, nil); err != nil || len(g.files) != before {
		t.Fatalf("steady state changed: %v", err)
	}
}

func TestSyncRestoresAnEmptyGuestWithoutDeletingHostFiles(t *testing.T) {
	root := t.TempDir()
	g := &fakeGuest{files: map[string]fakeFile{}}
	groot := "/guest/install"
	s := &syncer{g: g, logf: t.Logf, state: t.TempDir(), roots: func() []string { return []string{root} },
		toGst: func(string) string { return groot }, skipFn: skipSync}
	hostFile := filepath.Join(root, "backup-state", "last.json")
	if err := os.MkdirAll(filepath.Dir(hostFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hostFile, []byte("host backup state"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}

	// A fresh VM has an empty tmpfs-backed root even though the host has a
	// saved three-way sync state from the previous VM.
	g.files = map[string]fakeFile{}
	if err := s.run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(hostFile); err != nil || string(got) != "host backup state" {
		t.Fatalf("host mirror was deleted: %q %v", got, err)
	}
	if got := g.files[groot+"/backup-state/last.json"].data; got != "host backup state" {
		t.Fatalf("guest mirror was not restored: %q", got)
	}
}

func TestHelperPublishesStartupFailureBeforeVMIsReady(t *testing.T) {
	h := &host{status: HostStatus{Running: true, Error: "data disk: unavailable"}}
	response := httptest.NewRecorder()
	h.startupRoutes().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/status", nil))
	var status HostStatus
	if err := json.NewDecoder(response.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || status.Error != "data disk: unavailable" {
		t.Fatalf("startup status = %d %+v", response.Code, status)
	}
	if ready, err := statusOutcome(status); ready || err == nil || !strings.Contains(err.Error(), status.Error) {
		t.Fatalf("startup error outcome = %t %v", ready, err)
	}
}

func TestEnsureStatusDoesNotAcceptDockerBeforeAttachError(t *testing.T) {
	status := HostStatus{Running: true, Docker: true, Error: "share install: mount failed"}
	if ready, err := statusOutcome(status); ready || err == nil || !strings.Contains(err.Error(), "mount failed") {
		t.Fatalf("attach failure status = %t %v", ready, err)
	}
}

func TestMountSensitive(t *testing.T) {
	yes := [][]string{{"run", "img"}, {"compose", "up", "-d"}, {"compose", "-f", "x.yml", "run", "backup"}, {"container", "start", "x"}, {"compose", "exec", "backup", "sh"}}
	no := [][]string{{"version"}, {"compose", "ps"}, {"image", "inspect", "x"}, {"compose", "-p", "up", "ps"}, {}}
	for _, a := range yes {
		if !MountSensitive(a) {
			t.Errorf("%q should be mount-sensitive", a)
		}
	}
	for _, a := range no {
		if MountSensitive(a) {
			t.Errorf("%q should not be mount-sensitive", a)
		}
	}
}
