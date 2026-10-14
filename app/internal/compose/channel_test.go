package compose

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLockSurvivesRoundTripAndCorruption(t *testing.T) {
	dir := t.TempDir()
	next := versionFixture('b').Backend
	want := Lock{Version: "3.1.0",
		Backend:  Channel{Current: versionFixture('a').Backend, Next: &next, Declined: next.Commit},
		Frontend: Channel{Current: versionFixture('a').Frontend}}
	if err := WriteLock(dir, want); err != nil {
		t.Fatal(err)
	}
	if got := ReadLock(dir); !reflect.DeepEqual(got, want) {
		t.Errorf("lock round-trip = %+v, want %+v", got, want)
	}
	if err := os.WriteFile(filepath.Join(dir, LockFile), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ReadLock(dir); !reflect.DeepEqual(got, Lock{}) {
		t.Errorf("a corrupt lock was trusted: %+v", got)
	}
}

func installed(t *testing.T) *Builder {
	t.Helper()
	dockerFixture(t)
	t.Setenv("CARE_IMAGE_EXISTS", "yes")
	b := builderFixture(t)
	writeBuildFile(t, b.dir, "src/backend/x", "old checkout")
	writeBuildFile(t, b.dir, legacyLockFile, "{}")
	if err := b.Parallel(EnsureBackend, EnsureFrontend); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestFirstInstallRecordsCAREVersion(t *testing.T) {
	b := installed(t)
	lock, want := ReadLock(b.dir), versionFixture('a')
	if lock.Version != "3.1.0" || lock.Backend.Current.Image != want.Backend.Image || lock.Frontend.Current.Image != want.Frontend.Image {
		t.Fatalf("lock = %+v", lock)
	}
	for _, name := range []string{"src", legacyLockFile} {
		if _, err := os.Stat(filepath.Join(b.dir, name)); !os.IsNotExist(err) {
			t.Errorf("source-build leftover %s was kept", name)
		}
	}
}

func TestStartupKeepsTheInstalledReleaseOffline(t *testing.T) {
	b := installed(t)
	if err := os.Remove(filepath.Join(b.dir, "fixture-updates.json")); err != nil {
		t.Fatal(err)
	}
	if err := b.EnsureBackendImage(); err != nil {
		t.Fatalf("an installed clinic needed the network to start: %v", err)
	}
}

func TestUpdatesAreStagedAppliedAndDeclined(t *testing.T) {
	b := installed(t)
	if u, err := b.PrepareUpdate(); err != nil || u.Any() {
		t.Fatalf("unchanged images offered an update: %+v %v", u, err)
	}
	m := versionFixture('a')
	m.Backend = versionFixture('b').Backend
	publish(t, b, m)
	u, err := b.PrepareUpdate()
	if err != nil || u.Backend != m.Backend.Commit || u.Frontend != "" {
		t.Fatalf("PrepareUpdate = %+v %v", u, err)
	}
	if w := b.Waiting(); w.Backend != m.Backend.Commit {
		t.Fatalf("Waiting = %+v", w)
	}
	if err := b.DeclineUpdate(); err != nil {
		t.Fatal(err)
	}
	if u, _ := b.PrepareUpdate(); u.Any() {
		t.Fatal("a declined update was offered again")
	}
	applied, err := b.ApplyPending()
	if err != nil || applied.Backend != m.Backend.Commit {
		t.Fatalf("ApplyPending = %+v %v", applied, err)
	}
	lock := ReadLock(b.dir)
	if lock.Backend.Current.Image != m.Backend.Image || lock.Backend.Next != nil || lock.Backend.Declined != "" {
		t.Fatalf("lock after apply = %+v", lock.Backend)
	}
}

func TestDroppedStagedBuildIsOfferedAgain(t *testing.T) {
	dockerFixture(t)
	b := builderFixture(t)
	next := versionFixture('b').Backend
	if err := WriteLock(b.dir, Lock{Backend: Channel{Next: &next, Declined: next.Commit}}); err != nil {
		t.Fatal(err)
	}

	if _, err := b.ApplyPending(); err != nil {
		t.Fatal(err)
	}
	got := ReadLock(b.dir).Backend
	if got.Next != nil || got.Declined != "" {
		t.Fatalf("a vanished staged build left %+v", got)
	}
}

func TestInterruptedRetagKeepsPreviousReleaseDurable(t *testing.T) {
	b := installed(t)
	before := ReadLock(b.dir).Backend.Current
	next := versionFixture('b').Backend
	if err := ModifyLock(b.dir, func(lock *Lock) error {
		lock.Backend.Next = &next
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	crash := new(int)
	b.Log = func(line string) {
		if strings.Contains(line, "CARE backend updated") {
			panic(crash)
		}
	}
	func() {
		defer func() {
			if got := recover(); got != crash {
				t.Fatalf("expected crash after retag, got %v", got)
			}
		}()
		_, err := b.ApplyPending()
		t.Fatalf("activation returned without crashing: %v", err)
	}()
	stored := ReadLock(b.dir).Backend
	if stored.Previous == nil || stored.Previous.Image != before.Image || stored.Next == nil || stored.Next.Image != next.Image {
		t.Fatalf("retag lost durable rollback releases: %+v", stored)
	}
	if err := b.RollbackApplied(); err != nil {
		t.Fatal(err)
	}
	stored = ReadLock(b.dir).Backend
	if stored.Current.Image != before.Image || stored.Previous != nil || stored.Next == nil || stored.Next.Image != next.Image {
		t.Fatalf("recovery did not restore the old release and retain the prepared update: %+v", stored)
	}
}
