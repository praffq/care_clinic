package backups

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/agent"
	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
	"github.com/ohcnetwork/care_desktop/app/internal/stack/stacktest"
)

var t0 = time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)

// faultAgent wraps the real agent client to inject failures.
type faultAgent struct {
	Agent
	mu sync.Mutex
	// cut makes ExportBackupFile of that file fail after half the data.
	cut string
	// flip makes ExportBackupFile of that file write a corrupted byte but
	// report success, as a bad disk would.
	flip    string
	failDel bool
	// vanish is deleted in the engine when its export starts, as backup.sh
	// rotating it out would.
	vanish    string
	exports   int
	deletions []string
}

func (f *faultAgent) ExportBackupFile(ctx context.Context, id, file string, w io.Writer) (int64, error) {
	f.mu.Lock()
	f.exports++
	cut, flip, vanish := f.cut == file, f.flip == file, f.vanish == id
	f.mu.Unlock()
	if vanish {
		if err := f.Agent.DeleteBackup(ctx, id); err != nil {
			return 0, err
		}
	}
	if !cut && !flip {
		return f.Agent.ExportBackupFile(ctx, id, file, w)
	}
	var buf strings.Builder
	n, err := f.Agent.ExportBackupFile(ctx, id, file, &buf)
	if err != nil {
		return n, err
	}
	b := []byte(buf.String())
	if cut {
		m, _ := w.Write(b[:len(b)/2])
		return int64(m), errors.New("connection reset")
	}
	b[0] ^= 0xff
	m, err := w.Write(b)
	return int64(m), err
}

func (f *faultAgent) DeleteBackup(ctx context.Context, id string) error {
	f.mu.Lock()
	fail := f.failDel
	f.deletions = append(f.deletions, id)
	f.mu.Unlock()
	if fail {
		return errors.New("agent unreachable")
	}
	return f.Agent.DeleteBackup(ctx, id)
}

type env struct {
	svc     *stacktest.Service
	ag      *faultAgent
	dir     string
	copyDir string
	free    map[string]uint64 // free bytes by folder; unlimited when missing
	now     time.Time
	ready   bool
	ex      *Exporter
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{svc: stacktest.New(t), dir: filepath.Join(t.TempDir(), "host"), now: t0.Add(time.Hour), ready: true}
	e.ag = &faultAgent{Agent: e.svc.Agent()}
	e.free = map[string]uint64{}
	e.ex = New(e.config())
	return e
}

func (e *env) config() Config {
	return Config{
		Agent: e.ag, Dir: func() string { return e.dir }, CopyDir: func() string { return e.copyDir },
		Ready: func() bool { return e.ready }, Now: func() time.Time { return e.now },
		FreeSpace: func(p string) (uint64, error) {
			if n, ok := e.free[p]; ok {
				return n, nil
			}
			return 1 << 40, nil
		},
	}
}

func sum(b string) string {
	h := sha256.Sum256([]byte(b))
	return hex.EncodeToString(h[:])
}

// vmSet writes a complete set into the engine's backups root.
func (e *env) vmSet(t *testing.T, id string, created time.Time) protocol.BackupManifest {
	t.Helper()
	dir := filepath.Join(e.svc.Backups, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	m := protocol.BackupManifest{ID: id, CreatedAt: created}
	for _, name := range []string{"database.dump.enc", "files.tar.gz.enc"} {
		body := id + "/" + name + strings.Repeat("x", 4096)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		m.Files = append(m.Files, protocol.BackupFile{Name: name, Size: int64(len(body)), SHA256: sum(body)})
	}
	b, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, protocol.BackupManifestName), b, 0o600); err != nil {
		t.Fatal(err)
	}
	return m
}

func (e *env) vmIDs(t *testing.T) []string {
	t.Helper()
	sets, err := e.svc.Agent().ListBackups(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, s := range sets {
		ids = append(ids, s.ID)
	}
	return ids
}

func (e *env) hostIDs(t *testing.T) []string {
	t.Helper()
	sets, err := HostSets(e.dir)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, s := range sets {
		ids = append(ids, s.ID)
	}
	return ids
}

func (e *env) state(t *testing.T) State {
	t.Helper()
	var st State
	b, err := os.ReadFile(filepath.Join(e.dir, StateFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatal(err)
	}
	return st
}

func noPartials(t *testing.T, dir string) {
	t.Helper()
	if left, _ := filepath.Glob(filepath.Join(dir, "*"+partialSuffix)); len(left) != 0 {
		t.Fatalf("partial copies left: %v", left)
	}
}

func eq(a, b []string) bool { return strings.Join(a, ",") == strings.Join(b, ",") }

func TestExport(t *testing.T) {
	e := newEnv(t)
	e.vmSet(t, "daily-20261017T000000Z", t0.Add(-24*time.Hour))
	e.vmSet(t, "daily-20261018T000000Z", t0)
	if err := e.ex.Export(t.Context()); err != nil {
		t.Fatal(err)
	}
	if ids := e.vmIDs(t); len(ids) != 0 {
		t.Fatalf("engine still has %v", ids)
	}
	if ids := e.hostIDs(t); !eq(ids, []string{"daily-20261018T000000Z"}) {
		t.Fatalf("host sets = %v", ids)
	}
	if st := e.state(t); st.LastExported != "daily-20261018T000000Z" || !st.VerifiedAt.Equal(e.now) || st.LastError != "" {
		t.Fatalf("state = %+v", st)
	}
	if st := e.ex.Status(); st.Pending != 0 || st.Critical || st.Message != "" || st.LastExported != "daily-20261018T000000Z" {
		t.Fatalf("status = %+v", st)
	}
	// The copy carries the manifest and verifies.
	m, err := ReadManifest(filepath.Join(e.dir, "daily-20261018T000000Z"), "daily-20261018T000000Z")
	if err != nil || VerifySet(filepath.Join(e.dir, m.ID), m) != nil {
		t.Fatalf("host manifest: %+v %v", m, err)
	}
	// Windows has no POSIX permission bits; files inherit the folder ACL.
	if fi, err := os.Stat(filepath.Join(e.dir, m.ID, m.Files[0].Name)); err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o644) {
		t.Fatalf("file mode = %v %v", fi, err)
	}
	noPartials(t, e.dir)
}

func TestExportPartialDownload(t *testing.T) {
	e := newEnv(t)
	const id = "daily-20261018T000000Z"
	e.vmSet(t, id, t0)
	// A crash during an earlier copy left a partial folder behind.
	if err := os.MkdirAll(filepath.Join(e.dir, "daily-20261010T000000Z"+partialSuffix), 0o755); err != nil {
		t.Fatal(err)
	}
	e.ag.cut = "files.tar.gz.enc"
	err := e.ex.Export(t.Context())
	if err == nil || !strings.Contains(err.Error(), "connection reset") {
		t.Fatalf("err = %v", err)
	}
	noPartials(t, e.dir)
	if ids := e.hostIDs(t); len(ids) != 0 {
		t.Fatalf("host sets = %v", ids)
	}
	if ids := e.vmIDs(t); !eq(ids, []string{id}) || len(e.ag.deletions) != 0 {
		t.Fatalf("engine sets = %v, deletions %v", ids, e.ag.deletions)
	}
	st := e.ex.Status()
	if st.Pending != 1 || st.LastError == "" || !strings.HasPrefix(st.Message, "Backup not copied out since ") || st.Critical {
		t.Fatalf("status = %+v", st)
	}
	if e.state(t).LastError == "" {
		t.Fatal("export-state.json has no error")
	}

	// The retry five minutes later succeeds.
	e.ag.cut = ""
	e.now = e.now.Add(DefaultInterval)
	if err := e.ex.Export(t.Context()); err != nil {
		t.Fatal(err)
	}
	if ids := e.hostIDs(t); !eq(ids, []string{id}) || len(e.vmIDs(t)) != 0 {
		t.Fatalf("host %v engine %v", ids, e.vmIDs(t))
	}
	if st := e.ex.Status(); st.Message != "" || st.LastError != "" || st.Pending != 0 {
		t.Fatalf("status = %+v", st)
	}
}

func TestExportShaMismatch(t *testing.T) {
	t.Run("in transit", func(t *testing.T) {
		e := newEnv(t)
		const id = "daily-20261018T000000Z"
		e.vmSet(t, id, t0)
		// The engine copy rotted after its manifest was written.
		p := filepath.Join(e.svc.Backups, id, "database.dump.enc")
		b, _ := os.ReadFile(p)
		b[10] ^= 1
		_ = os.WriteFile(p, b, 0o600)
		if err := e.ex.Export(t.Context()); err == nil || !strings.Contains(err.Error(), "checksum") {
			t.Fatalf("err = %v", err)
		}
		if len(e.hostIDs(t)) != 0 || !eq(e.vmIDs(t), []string{id}) {
			t.Fatalf("host %v engine %v", e.hostIDs(t), e.vmIDs(t))
		}
		noPartials(t, e.dir)
	})
	t.Run("on disk", func(t *testing.T) {
		e := newEnv(t)
		const id = "daily-20261018T000000Z"
		e.vmSet(t, id, t0)
		e.ag.flip = "files.tar.gz.enc"
		if err := e.ex.Export(t.Context()); err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
			t.Fatalf("err = %v", err)
		}
		if len(e.hostIDs(t)) != 0 || !eq(e.vmIDs(t), []string{id}) || len(e.ag.deletions) != 0 {
			t.Fatalf("host %v engine %v deletions %v", e.hostIDs(t), e.vmIDs(t), e.ag.deletions)
		}
		noPartials(t, e.dir)
	})
}

func TestExportCrashBetweenRenameAndDelete(t *testing.T) {
	e := newEnv(t)
	const id = "daily-20261018T000000Z"
	e.vmSet(t, id, t0)
	// The first pass copies the set but cannot delete it in the engine, as
	// if the service died right after the rename.
	e.ag.failDel = true
	if err := e.ex.Export(t.Context()); err == nil {
		t.Fatal("no error")
	}
	if !eq(e.hostIDs(t), []string{id}) || !eq(e.vmIDs(t), []string{id}) {
		t.Fatalf("host %v engine %v", e.hostIDs(t), e.vmIDs(t))
	}
	if st := e.ex.Status(); st.Pending != 1 || st.Message == "" {
		t.Fatalf("status = %+v", st)
	}

	// A new service process verifies the copy and deletes without copying.
	e.ag.failDel = false
	e.ag.exports = 0
	e.ex = New(Config{Agent: e.ag, Dir: func() string { return e.dir }, Now: func() time.Time { return e.now }})
	if err := e.ex.Export(t.Context()); err != nil {
		t.Fatal(err)
	}
	if e.ag.exports != 0 {
		t.Fatalf("copied again (%d files)", e.ag.exports)
	}
	if !eq(e.hostIDs(t), []string{id}) || len(e.vmIDs(t)) != 0 {
		t.Fatalf("host %v engine %v", e.hostIDs(t), e.vmIDs(t))
	}

	// A damaged host copy of a set still in the engine is copied again.
	e.vmSet(t, "daily-20261019T000000Z", t0.Add(24*time.Hour))
	if err := os.MkdirAll(filepath.Join(e.dir, "daily-20261019T000000Z"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := e.ex.Export(t.Context()); err != nil {
		t.Fatal(err)
	}
	if e.ag.exports != 2 || !eq(e.hostIDs(t), []string{"daily-20261019T000000Z"}) {
		t.Fatalf("exports %d host %v", e.ag.exports, e.hostIDs(t))
	}
}

func TestExportRetention(t *testing.T) {
	e := newEnv(t)
	e.vmSet(t, "daily-20261017T000000Z", t0.Add(-24*time.Hour))
	if err := e.ex.Export(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Not backups: left alone.
	other := filepath.Join(e.dir, "my-notes")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}

	// The next set fails to copy: the old one stays.
	e.vmSet(t, "daily-20261018T000000Z", t0)
	e.ag.cut = "database.dump.enc"
	if err := e.ex.Export(t.Context()); err == nil {
		t.Fatal("no error")
	}
	if !eq(e.hostIDs(t), []string{"daily-20261017T000000Z"}) {
		t.Fatalf("host = %v", e.hostIDs(t))
	}

	// Once it is copied and verified, the old one goes.
	e.ag.cut = ""
	if err := e.ex.Export(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !eq(e.hostIDs(t), []string{"daily-20261018T000000Z"}) {
		t.Fatalf("host = %v", e.hostIDs(t))
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("pruned a folder that is not a backup: %v", err)
	}
}

func TestExportCritical(t *testing.T) {
	e := newEnv(t)
	for i := range 3 {
		e.vmSet(t, "daily-2026101"+string(rune('5'+i))+"T000000Z", t0.Add(time.Duration(i-3)*24*time.Hour))
	}
	e.ag.failDel = true
	if err := e.ex.Export(t.Context()); err == nil {
		t.Fatal("no error")
	}
	st := e.ex.Status()
	if st.Pending != 3 || !st.Critical || !strings.Contains(st.Message, "3 backups are waiting") || !strings.HasPrefix(st.Message, "Backup not copied out since ") || !st.OldestPendingAt.Equal(t0.Add(-72*time.Hour)) {
		t.Fatalf("status = %+v", st)
	}
	// The host keeps exactly one verified set even while deletes fail.
	if ids := e.hostIDs(t); len(ids) != 1 {
		t.Fatalf("host = %v", ids)
	}
}

func TestExportEngineDown(t *testing.T) {
	e := newEnv(t)
	e.ready = false
	if err := e.ex.Export(t.Context()); !errors.Is(err, ErrEngineUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if st := e.ex.Status(); st.Message != "Backup not copied out since the service started" {
		t.Fatalf("status = %+v", st)
	}
}

func TestNextWait(t *testing.T) {
	loc := time.FixedZone("IST", 19800)
	for _, tc := range []struct {
		now  time.Time
		want time.Duration
	}{
		{time.Date(2026, 10, 18, 12, 0, 0, 0, loc), DefaultInterval},
		{time.Date(2026, 10, 18, 23, 59, 0, 0, loc), 3 * time.Minute},
		{time.Date(2026, 10, 19, 0, 1, 0, 0, loc), time.Minute},
		{time.Date(2026, 10, 19, 0, 2, 0, 0, loc), DefaultInterval},
	} {
		e := New(Config{Now: func() time.Time { return tc.now }})
		if got := e.nextWait(); got != tc.want {
			t.Errorf("%v: %v, want %v", tc.now, got, tc.want)
		}
	}
}

func TestRestore(t *testing.T) {
	e := newEnv(t)
	const id = "daily-20261018T000000Z"
	e.vmSet(t, id, t0)
	if err := e.ex.Export(t.Context()); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(e.svc.Files, string(protocol.FileAreaRestoreInput))
	var seen error
	e.svc.Fake.Handle = func(_ context.Context, op protocol.Op, _ agent.Emitter) (any, error) {
		if r, ok := op.(*protocol.DBRestore); !ok || r.BackupID != id {
			return nil, errors.New("unexpected op")
		}
		seen = agent.VerifyRestoreInput(input, id)
		return nil, nil
	}
	key := []byte("-----BEGIN RSA PRIVATE KEY-----\n")
	if err := Restore(t.Context(), e.svc.Agent(), filepath.Join(e.dir, id), key, nil); err != nil {
		t.Fatal(err)
	}
	if seen != nil {
		t.Fatalf("restore input during the op: %v", seen)
	}
	if left, _ := os.ReadDir(input); len(left) != 0 {
		t.Fatalf("restore input left behind: %v", left)
	}

	// A damaged host set is refused before anything is uploaded.
	p := filepath.Join(e.dir, id, "files.tar.gz.enc")
	_ = os.WriteFile(p, []byte("x"), 0o644)
	calls := len(e.svc.Fake.Calls())
	if err := Restore(t.Context(), e.svc.Agent(), filepath.Join(e.dir, id), key, nil); err == nil || !strings.Contains(err.Error(), "damaged") {
		t.Fatalf("err = %v", err)
	}
	if len(e.svc.Fake.Calls()) != calls {
		t.Fatal("restore ran")
	}
}
