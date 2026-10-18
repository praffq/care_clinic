package backups

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExportCriticalAtTwo(t *testing.T) {
	e := newEnv(t)
	e.vmSet(t, "daily-20261017T000000Z", t0.Add(-24*time.Hour))
	e.vmSet(t, "daily-20261018T000000Z", t0)
	e.ag.cut = "files.tar.gz.enc"
	if err := e.ex.Export(t.Context()); err == nil {
		t.Fatal("no error")
	}
	st := e.ex.Status()
	if st.Pending != 2 || !st.Critical || !strings.Contains(st.Message, "replaces the oldest") {
		t.Fatalf("status = %+v", st)
	}
}

func TestExportBackupFolderMissing(t *testing.T) {
	e := newEnv(t)
	// The folder is on a drive that is not connected: its mount point is
	// missing, and the service must not create it on the boot disk.
	e.dir = filepath.Join(t.TempDir(), "Volumes", "USB", "care-backups")
	e.vmSet(t, "daily-20261018T000000Z", t0)
	err := e.ex.Export(t.Context())
	if err == nil || !strings.Contains(err.Error(), "is not available") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Dir(e.dir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("created %s: %v", filepath.Dir(e.dir), err)
	}
	if st := e.ex.Status(); !strings.HasPrefix(st.Message, "Backup not copied out since ") || !strings.Contains(st.LastError, "is not available") {
		t.Fatalf("status = %+v", st)
	}
	if len(e.ag.deletions) != 0 {
		t.Fatalf("deleted %v", e.ag.deletions)
	}
	// Reconnected: the next pass (five minutes later) copies it out.
	if err := os.MkdirAll(filepath.Dir(e.dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := e.ex.Export(t.Context()); err != nil {
		t.Fatal(err)
	}
	if st := e.ex.Status(); st.Message != "" || st.LastExported != "daily-20261018T000000Z" {
		t.Fatalf("status = %+v", st)
	}
}

func TestExportDiskFull(t *testing.T) {
	e := newEnv(t)
	const id = "daily-20261018T000000Z"
	e.vmSet(t, id, t0)
	e.free[e.dir] = 100
	if err := os.MkdirAll(e.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	err := e.ex.Export(t.Context())
	if err == nil || !strings.Contains(err.Error(), "not enough space in "+e.dir) {
		t.Fatalf("err = %v", err)
	}
	if !eq(e.vmIDs(t), []string{id}) || len(e.hostIDs(t)) != 0 {
		t.Fatalf("engine %v host %v", e.vmIDs(t), e.hostIDs(t))
	}
	noPartials(t, e.dir)
}

func TestExportSetRotatedOut(t *testing.T) {
	e := newEnv(t)
	e.vmSet(t, "daily-20261017T000000Z", t0.Add(-24*time.Hour))
	e.vmSet(t, "daily-20261018T000000Z", t0)
	e.ag.vanish = "daily-20261017T000000Z"
	if err := e.ex.Export(t.Context()); err != nil {
		t.Fatal(err)
	}
	if ids := e.hostIDs(t); !eq(ids, []string{"daily-20261018T000000Z"}) {
		t.Fatalf("host = %v", ids)
	}
	if st := e.ex.Status(); st.Pending != 0 || st.Message != "" {
		t.Fatalf("status = %+v", st)
	}
}

func (e *env) copyIDs(t *testing.T) []string {
	t.Helper()
	sets, err := HostSets(e.copyDir)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, s := range sets {
		ids = append(ids, s.ID)
	}
	return ids
}

func TestCopy(t *testing.T) {
	e := newEnv(t)
	usb := filepath.Join(t.TempDir(), "USB")
	e.copyDir = filepath.Join(usb, "care-copy")

	// No host set yet: nothing to do, but the folder must be there.
	if err := e.ex.Copy(t.Context()); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(e.copyDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("created the copy folder")
	}
	c := e.ex.Status().Copy
	if c == nil || c.Connected || !strings.Contains(c.Message, "isn't connected; last copied never") {
		t.Fatalf("copy = %+v", c)
	}

	if err := os.MkdirAll(e.copyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	e.vmSet(t, "daily-20261017T000000Z", t0.Add(-24*time.Hour))
	if err := e.ex.Export(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := e.ex.Copy(t.Context()); err != nil {
		t.Fatal(err)
	}
	if ids := e.copyIDs(t); !eq(ids, []string{"daily-20261017T000000Z"}) {
		t.Fatalf("copy = %v", ids)
	}
	m, err := ReadManifest(filepath.Join(e.copyDir, "daily-20261017T000000Z"), "daily-20261017T000000Z")
	if err != nil || VerifySet(filepath.Join(e.copyDir, m.ID), m) != nil {
		t.Fatalf("copy manifest: %v", err)
	}
	firstCopy := e.now
	c = e.ex.Status().Copy
	if !c.Connected || c.Message != "" || c.LastCopied != "daily-20261017T000000Z" || !c.CopiedAt.Equal(firstCopy) {
		t.Fatalf("copy = %+v", c)
	}
	if st := e.state(t); st.Copy == nil || st.Copy.LastCopied != "daily-20261017T000000Z" {
		t.Fatalf("state = %+v", st)
	}

	// Unplugged: the new set waits, "last copied" stays.
	if err := os.Rename(usb, usb+"-away"); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(24 * time.Hour)
	e.vmSet(t, "daily-20261018T000000Z", t0)
	if err := e.ex.Export(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := e.ex.Copy(t.Context()); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("err = %v", err)
	}
	c = e.ex.Status().Copy
	want := "last copied " + firstCopy.Local().Format(time.DateTime)
	if c.Connected || !strings.Contains(c.Message, want) || c.LastCopied != "daily-20261017T000000Z" {
		t.Fatalf("copy = %+v", c)
	}
	// A restarted service still knows.
	e.ex = New(e.config())
	if c := e.ex.Status().Copy; !strings.Contains(c.Message, want) {
		t.Fatalf("after restart: %+v", c)
	}

	// Plugged back in: the new set is copied, then the old one goes.
	if err := os.Rename(usb+"-away", usb); err != nil {
		t.Fatal(err)
	}
	if err := e.ex.Copy(t.Context()); err != nil {
		t.Fatal(err)
	}
	if ids := e.copyIDs(t); !eq(ids, []string{"daily-20261018T000000Z"}) {
		t.Fatalf("copy = %v", ids)
	}
	if c := e.ex.Status().Copy; !c.Connected || c.Message != "" || c.LastCopied != "daily-20261018T000000Z" {
		t.Fatalf("copy = %+v", c)
	}
	noPartials(t, e.copyDir)

	// Turned off: no copy status.
	e.copyDir = ""
	if err := e.ex.Copy(t.Context()); err != nil {
		t.Fatal(err)
	}
	if c := e.ex.Status().Copy; c != nil {
		t.Fatalf("copy = %+v", c)
	}
	if st := e.state(t); st.Copy != nil {
		t.Fatalf("state = %+v", st)
	}
}

func TestCopyFailureKeepsThePreviousCopy(t *testing.T) {
	e := newEnv(t)
	e.copyDir = t.TempDir()
	e.vmSet(t, "daily-20261017T000000Z", t0.Add(-24*time.Hour))
	if err := e.ex.Export(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := e.ex.Copy(t.Context()); err != nil {
		t.Fatal(err)
	}

	// The new host set rotted after it was verified: the copy is refused.
	e.vmSet(t, "daily-20261018T000000Z", t0)
	if err := e.ex.Export(t.Context()); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(e.dir, "daily-20261018T000000Z", "database.dump.enc")
	good, _ := os.ReadFile(p)
	bad := append([]byte(nil), good...)
	bad[0] ^= 1
	_ = os.WriteFile(p, bad, 0o644)
	err := e.ex.Copy(t.Context())
	if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("err = %v", err)
	}
	if ids := e.copyIDs(t); !eq(ids, []string{"daily-20261017T000000Z"}) {
		t.Fatalf("copy = %v", ids)
	}
	noPartials(t, e.copyDir)
	if c := e.ex.Status().Copy; !c.Connected || !strings.Contains(c.Message, "failed; last copied") {
		t.Fatalf("copy = %+v", c)
	}

	// No room on the drive: refused before copying.
	_ = os.WriteFile(p, good, 0o644)
	e.free[e.copyDir] = 10
	if err := e.ex.Copy(t.Context()); err == nil || !strings.Contains(err.Error(), "not enough space") {
		t.Fatalf("err = %v", err)
	}
	if ids := e.copyIDs(t); !eq(ids, []string{"daily-20261017T000000Z"}) {
		t.Fatalf("copy = %v", ids)
	}
}

func TestRunRetriesTheCopy(t *testing.T) {
	e := newEnv(t)
	e.copyDir = filepath.Join(t.TempDir(), "missing")
	cfg := e.config()
	cfg.Now = time.Now
	cfg.CopyRetry = 10 * time.Millisecond
	e.ex = New(cfg)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { e.ex.Run(ctx); close(done) }()
	e.vmSet(t, "daily-20261018T000000Z", t0)
	e.ex.Kick()
	if err := os.MkdirAll(e.copyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if c := e.ex.Status().Copy; c != nil && c.LastCopied == "daily-20261018T000000Z" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not copied: %+v", e.ex.Status())
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
}

func TestFindSet(t *testing.T) {
	e := newEnv(t)
	e.vmSet(t, "daily-20261017T000000Z", t0.Add(-24*time.Hour))
	if err := e.ex.Export(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Keep both: export prunes older host sets, so copy one in by hand.
	older := filepath.Join(t.TempDir(), "daily-20261017T000000Z")
	if err := os.Rename(filepath.Join(e.dir, "daily-20261017T000000Z"), older); err != nil {
		t.Fatal(err)
	}
	e.vmSet(t, "daily-20261018T000000Z", t0)
	if err := e.ex.Export(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(older, filepath.Join(e.dir, "daily-20261017T000000Z")); err != nil {
		t.Fatal(err)
	}
	set := filepath.Join(e.dir, "daily-20261017T000000Z")
	for _, c := range []struct{ path, id, want, err string }{
		{e.dir, "", filepath.Join(e.dir, "daily-20261018T000000Z"), ""},
		{e.dir, "daily-20261017T000000Z", set, ""},
		{set, "", set, ""},
		{set, "daily-20261017T000000Z", set, ""},
		{set, "daily-20261018T000000Z", "", "not daily-20261018T000000Z"},
		{e.dir, "../x", "", "invalid"},
		{t.TempDir(), "", "", "no backup in"},
	} {
		got, err := FindSet(c.path, c.id)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("FindSet(%s, %q) = %q, %v; want error %q", c.path, c.id, got, err, c.err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("FindSet(%s, %q) = %q, %v; want %q", c.path, c.id, got, err, c.want)
		}
	}
}
