package updates_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
	"github.com/ohcnetwork/care_desktop/app/internal/feed"
	"github.com/ohcnetwork/care_desktop/app/internal/updates"
)

var (
	revB      = spec{rev: 2, engines: []eng{{"0.1.0", 1}}, cares: []string{"A", "B"}}
	revEngine = spec{rev: 2, engines: []eng{{"0.1.0", 1}, {"0.1.1", 1}}, cares: []string{"A"}}
)

func TestFreshInstallThenUpdates(t *testing.T) {
	h := newHarness(t)
	h.publish(spec{rev: 1, engines: []eng{{"0.1.0", 1}}, cares: []string{"A"}})
	r := h.check()
	if !r.Available || !r.Prefetched || steps(r) != "engine ->0.1.0, care ->"+careVersion("A") {
		t.Fatalf("check = %+v", r)
	}
	// Check stages the slot but activates nothing.
	if h.w.Booted || len(h.w.Slots) != 1 || h.w.Count("Activate") != 0 {
		t.Fatalf("check changed the engine: %v", h.w.CallLog())
	}
	if _, err := os.Stat(filepath.Join(h.dir, updates.VerifiedFile)); err != nil {
		t.Fatal(err)
	}
	res := h.mustApply()
	if len(res.Applied) != 2 || res.Applied[0].Component != feed.ComponentEngine || res.Applied[1].Result != updates.ResultApplied {
		t.Fatalf("applied = %+v", res.Applied)
	}
	h.wantOn("0.1.0", "A")
	if h.w.Count("op db.dump") != 0 || h.w.Count("op stack.stop") != 0 {
		t.Fatalf("fresh install backed up or stopped: %v", h.w.CallLog())
	}
	if _, err := os.Stat(filepath.Join(h.dir, updates.JournalFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal.json present when idle: %v", err)
	}

	// CARE update: backup first, app services stopped, migrate, up, health.
	h.publish(revB)
	if r := h.check(); steps(r) != "care "+careVersion("A")+"->"+careVersion("B") {
		t.Fatalf("check = %+v", r)
	}
	h.mustApply()
	h.wantOn("0.1.0", "B")
	if e := h.lastHistory(); e.Result != updates.ResultApplied || e.BackupID == "" {
		t.Fatalf("history = %+v", e)
	}
	wantOrder(t, h.w.CallLog(), "op db.dump", "op stack.stop", "WriteEnv", "op migrations.run", "op stack.up", "Health")

	// Engine update with CARE running.
	h.publish(spec{rev: 3, engines: []eng{{"0.1.0", 1}, {"0.1.1", 1}}, cares: []string{"A", "B"}})
	h.check()
	h.mustApply()
	h.wantOn("0.1.1", "B")
	if _, err := h.apply(); !errors.Is(err, updates.ErrUpToDate) {
		t.Fatalf("apply when up to date: %v", err)
	}
	if hs, _ := h.c.History(); len(hs) != 4 {
		t.Fatalf("history has %d entries, want 4", len(hs))
	}
}

// wantOrder checks that each of names occurs in calls after the previous.
func wantOrder(t *testing.T, calls []string, names ...string) {
	t.Helper()
	i := 0
	for _, c := range calls {
		if i < len(names) && strings.HasPrefix(c, names[i]) {
			i++
		}
	}
	if i != len(names) {
		t.Fatalf("calls %q\nmissing %q in order", calls, names[i:])
	}
}

func TestCareFailureRollsBack(t *testing.T) {
	for _, tc := range []struct {
		name    string
		step    string
		setup   func(h *harness)
		restore bool // the database is restored
		stopped bool // the app was stopped
	}{
		{name: "preflight db size", step: updates.StepPreflight, setup: func(h *harness) { h.w.FailOn("DBSize", 3) }},
		{name: "preflight disk", step: updates.StepPreflight, setup: func(h *harness) { h.w.DiskFree = 1 << 20 }},
		{name: "preflight unhealthy", step: updates.StepPreflight, setup: func(h *harness) { h.w.FailOn("Health", 3) }},
		{name: "preflight clock", step: updates.StepPreflight, setup: func(h *harness) { h.now = published(2).Add(-2 * time.Hour) }},
		{name: "backup", step: updates.StepBackup, setup: func(h *harness) { h.w.FailOn("op db.dump", 3) }},
		{name: "images", step: updates.StepImages, setup: func(h *harness) { h.w.FailOn("EnsureImages", 3) }},
		{name: "stop", step: updates.StepStopApp, setup: func(h *harness) { h.w.FailOn("op stack.stop", 3) }, stopped: true},
		{name: "write env", step: updates.StepWriteEnv, setup: func(h *harness) { h.w.FailOn("WriteEnv", 3) }, stopped: true},
		{name: "migrate", step: updates.StepMigrate, setup: func(h *harness) { h.w.FailOn("op migrations.run", 3) }, stopped: true, restore: true},
		{name: "stack up", step: updates.StepStartApp, setup: func(h *harness) { h.w.FailOn("op stack.up", 3) }, stopped: true, restore: true},
		{name: "health", step: updates.StepHealth, setup: func(h *harness) { h.w.BadBackends[Digest("backend-B")] = true }, stopped: true, restore: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			h.installCare("A")
			h.publish(revB)
			h.check()
			before := len(h.w.CallLog())
			tc.setup(h)
			_, err := h.apply()
			oe, ok := errors.AsType[*updates.OpError](err)
			if !ok || oe.Result != updates.ResultRolledBack || oe.Step != tc.step {
				t.Fatalf("apply err = %v, want rolled back at %s", err, tc.step)
			}
			h.wantOn("0.1.0", "A")
			e := h.lastHistory()
			if e.Result != updates.ResultRolledBack || e.FailedStep != tc.step || e.Error == "" {
				t.Fatalf("history = %+v", e)
			}
			calls := h.w.CallLog()[before:]
			if got := slices.ContainsFunc(calls, func(c string) bool { return strings.HasPrefix(c, "op db.restore") }); got != tc.restore {
				t.Fatalf("db.restore called = %v, want %v: %q", got, tc.restore, calls)
			}
			if got := slices.ContainsFunc(calls, func(c string) bool { return strings.HasPrefix(c, "op stack.stop") }); got != tc.stopped {
				t.Fatalf("stack.stop called = %v, want %v: %q", got, tc.stopped, calls)
			}
			if tc.restore {
				// The previous env goes back before the restore, which
				// migrates and starts with whatever env is written.
				wantOrder(t, calls, "op migrations.run", "op stack.stop", "WriteEnv", "op db.restore", "op stack.up", "Health")
			}
		})
	}
}

func TestStepRetriesThreeTimes(t *testing.T) {
	h := newHarness(t)
	h.installCare("A")
	h.publish(revB)
	h.check()
	h.w.FailOn("op stack.up", 2)
	h.mustApply()
	h.wantOn("0.1.0", "B")
	if n := h.w.Count("op stack.up"); n != 1+3 { // install, then 2 failures + 1
		t.Fatalf("stack.up called %d times", n)
	}
}

func TestFreshCareInstallFailureStopsApp(t *testing.T) {
	h := newHarness(t)
	h.publish(spec{rev: 1, engines: []eng{{"0.1.0", 1}}, cares: []string{"A"}})
	h.check()
	h.w.BadBackends[Digest("backend-A")] = true
	_, err := h.apply()
	if oe, ok := errors.AsType[*updates.OpError](err); !ok || oe.Result != updates.ResultRolledBack {
		t.Fatalf("apply = %v", err)
	}
	st := h.status()
	if st.Installed.Care != nil || st.Installed.Engine == nil {
		t.Fatalf("installed = %+v", st.Installed)
	}
	if h.w.Running["backend"] {
		t.Fatal("failed fresh install left the backend running")
	}
}

// crashPoints is every hook point of steps.
func crashPoints(steps []string) []string {
	var out []string
	for _, s := range steps {
		out = append(out, "before:"+s, "after:"+s)
	}
	return out
}

var careStepNames = []string{
	updates.StepPreflight, updates.StepBackup, updates.StepImages, updates.StepStopApp,
	updates.StepWriteEnv, updates.StepMigrate, updates.StepStartApp, updates.StepHealth,
}

func TestCareCrashThenResume(t *testing.T) {
	for _, point := range crashPoints(careStepNames) {
		t.Run(point, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			h.installCare("A")
			h.publish(revB)
			h.check()
			h.hook.set(crashAt(point))
			if _, err := h.apply(); !errors.Is(err, errCrash) {
				t.Fatalf("apply = %v", err)
			}
			h.restart()
			if j := h.status().Journal; j == nil || j.State != updates.StateUncertain {
				t.Fatalf("journal after restart = %+v", j)
			}
			if _, err := h.apply(); !errors.Is(err, updates.ErrInFlight) {
				t.Fatalf("apply with a journal = %v", err)
			}
			if err := h.c.Resume(context.Background()); err != nil {
				t.Fatalf("resume: %v", err)
			}
			h.wantOn("0.1.0", "B")
			if n := h.w.Count("op db.dump"); n != 1 {
				t.Fatalf("db.dump ran %d times", n)
			}
		})
	}
}

func TestCareCrashThenRollback(t *testing.T) {
	for _, point := range crashPoints(careStepNames) {
		t.Run(point, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			h.installCare("A")
			h.publish(revB)
			h.check()
			h.hook.set(crashAt(point))
			if _, err := h.apply(); !errors.Is(err, errCrash) {
				t.Fatalf("apply = %v", err)
			}
			h.restart()
			if err := h.c.Rollback(context.Background()); err != nil {
				t.Fatalf("rollback: %v", err)
			}
			h.wantOn("0.1.0", "A")
			if e := h.lastHistory(); e.Result != updates.ResultRolledBack {
				t.Fatalf("history = %+v", e)
			}
		})
	}
}

var careRollbackSteps = []string{
	updates.StepRBStopApp, updates.StepRBEnv, updates.StepRBRestoreDB, updates.StepRBStart, updates.StepRBHealth,
}

func TestCrashDuringRollbackThenResume(t *testing.T) {
	for _, point := range crashPoints(careRollbackSteps) {
		t.Run(point, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			h.installCare("A")
			h.publish(revB)
			h.check()
			h.w.BadBackends[Digest("backend-B")] = true
			h.hook.set(crashAt(point))
			if _, err := h.apply(); !errors.Is(err, errCrash) {
				t.Fatalf("apply = %v", err)
			}
			h.restart()
			if j := h.status().Journal; j == nil || j.State != updates.StateRollbackUncertain {
				t.Fatalf("journal after restart = %+v", j)
			}
			err := h.c.Resume(context.Background())
			if oe, ok := errors.AsType[*updates.OpError](err); !ok || oe.Result != updates.ResultRolledBack {
				t.Fatalf("resume = %v", err)
			}
			h.wantOn("0.1.0", "A")
		})
	}
}

func TestRollbackFailureKeepsJournal(t *testing.T) {
	h := newHarness(t)
	h.installCare("A")
	h.publish(revB)
	h.check()
	h.w.BadBackends[Digest("backend-B")] = true
	h.w.FailOn("op db.restore", 3)
	_, err := h.apply()
	if !errors.Is(err, updates.ErrRollbackFailed) {
		t.Fatalf("apply = %v", err)
	}
	if oe, _ := errors.AsType[*updates.OpError](err); oe == nil || oe.Result != updates.ResultRollbackFailed {
		t.Fatalf("apply = %#v", err)
	}
	if j := h.status().Journal; j == nil || j.State != updates.StateFailed {
		t.Fatalf("journal = %+v", j)
	}
	if e := h.lastHistory(); e.Result != updates.ResultRollbackFailed {
		t.Fatalf("history = %+v", e)
	}
	if _, err := h.apply(); !errors.Is(err, updates.ErrInFlight) {
		t.Fatalf("apply = %v", err)
	}
	if err := h.c.Resume(context.Background()); !errors.Is(err, updates.ErrRollbackFailed) {
		t.Fatalf("resume = %v", err)
	}
	h.restart()
	if err := h.c.Rollback(context.Background()); err != nil {
		t.Fatalf("rollback retry: %v", err)
	}
	h.wantOn("0.1.0", "A")
	if err := h.c.Rollback(context.Background()); !errors.Is(err, updates.ErrNoJournal) {
		t.Fatalf("rollback with nothing = %v", err)
	}
}

func TestEngineFailureRollsBack(t *testing.T) {
	for _, tc := range []struct {
		name       string
		step       string
		setup      func(h *harness)
		reactivate bool
		feed       *spec
		max        int
	}{
		{name: "stage", step: updates.StepStage, setup: func(h *harness) { h.w.FailOn("Slots", 3) }},
		{name: "stop", step: updates.StepStopStack, setup: func(h *harness) { h.w.FailOn("op stack.stop", 3) }},
		{name: "activate", step: updates.StepActivate, setup: func(h *harness) { h.w.FailOn("Activate", 3) }, reactivate: true},
		{name: "boot never ready", step: updates.StepBoot, setup: func(h *harness) { h.w.BadEngines["0.1.1"] = true }, reactivate: true},
		{name: "boot wrong schema", step: updates.StepBoot, setup: func(h *harness) { h.w.WrongSchema["0.1.1"] = 7 }, reactivate: true},
		{
			name: "boot unsupported schema", step: updates.StepBoot, setup: func(h *harness) {}, reactivate: true, max: 1,
			feed: &spec{rev: 2, engines: []eng{{"0.1.0", 1}, {"0.1.1", 2}}, cares: []string{"A"}},
		},
		{name: "stack up", step: updates.StepEngineStart, setup: func(h *harness) { h.w.FailOn("op stack.up", 3) }, reactivate: true},
		{name: "health", step: updates.StepEngineCheck, setup: func(h *harness) { h.w.FailOn("Health", 3) }, reactivate: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, func(o *updates.Options) { o.MaxDataSchema = tc.max })
			h.installCare("A")
			f := revEngine
			if tc.feed != nil {
				f = *tc.feed
			}
			h.publish(f)
			if r := h.check(); !r.Prefetched {
				t.Fatalf("check = %+v", r)
			}
			tc.setup(h)
			before := len(h.w.CallLog())
			_, err := h.apply()
			oe, ok := errors.AsType[*updates.OpError](err)
			if !ok || oe.Result != updates.ResultRolledBack || oe.Step != tc.step {
				t.Fatalf("apply = %v, want rolled back at %s", err, tc.step)
			}
			h.wantOn("0.1.0", "A")
			if got := slices.Contains(h.w.CallLog()[before:], "Activate 0.1.0"); got != tc.reactivate {
				t.Fatalf("re-activated = %v, want %v", got, tc.reactivate)
			}
		})
	}
}

var engineStepNames = []string{
	updates.StepStage, updates.StepStopStack, updates.StepActivate, updates.StepBoot, updates.StepEngineStart, updates.StepEngineCheck,
}

func TestEngineCrashThenResume(t *testing.T) {
	for _, point := range crashPoints(engineStepNames) {
		t.Run(point, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			h.installCare("A")
			h.publish(revEngine)
			h.check()
			h.hook.set(crashAt(point))
			if _, err := h.apply(); !errors.Is(err, errCrash) {
				t.Fatalf("apply = %v", err)
			}
			h.restart()
			if err := h.c.Resume(context.Background()); err != nil {
				t.Fatalf("resume: %v", err)
			}
			h.wantOn("0.1.1", "A")
		})
	}
}

func TestEngineCrashThenRollback(t *testing.T) {
	for _, point := range crashPoints(engineStepNames) {
		t.Run(point, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			h.installCare("A")
			h.publish(revEngine)
			h.check()
			h.hook.set(crashAt(point))
			if _, err := h.apply(); !errors.Is(err, errCrash) {
				t.Fatalf("apply = %v", err)
			}
			h.restart()
			if err := h.c.Rollback(context.Background()); err != nil {
				t.Fatalf("rollback: %v", err)
			}
			h.wantOn("0.1.0", "A")
		})
	}
}

func TestCheckRejectsFeeds(t *testing.T) {
	h := newHarness(t)
	h.publish(spec{rev: 5, engines: []eng{{"0.1.0", 1}}, cares: []string{"A"}})
	h.check()
	verified := func() []byte {
		b, err := os.ReadFile(filepath.Join(h.dir, updates.VerifiedFile))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	good := verified()

	h.publish(spec{rev: 4, engines: []eng{{"0.1.0", 1}}, cares: []string{"A"}})
	if _, err := h.c.Check(context.Background()); !errors.Is(err, feed.ErrRollback) {
		t.Fatalf("lower revision: %v", err)
	}
	data := h.build(spec{rev: 6, engines: []eng{{"0.1.0", 1}}, cares: []string{"A"}})
	sig := feed.Sign(data, h.key).Marshal()
	h.setFeed(append(slices.Clone(data[:len(data)-1]), ' ', '}'), sig)
	if _, err := h.c.Check(context.Background()); !errors.Is(err, feed.ErrBadSignature) {
		t.Fatalf("tampered: %v", err)
	}
	h.publish(spec{rev: 7, engines: []eng{{"0.1.0", 1}}, cares: []string{"A"}, expires: published(7).Add(time.Hour)})
	if _, err := h.c.Check(context.Background()); !errors.Is(err, feed.ErrExpired) {
		t.Fatalf("expired: %v", err)
	}
	if string(verified()) != string(good) {
		t.Fatal("a rejected feed changed verified.json")
	}
	// The same revision again is fine.
	h.publish(spec{rev: 5, engines: []eng{{"0.1.0", 1}}, cares: []string{"A"}})
	h.check()
	if st := h.status(); st.Revision != 5 {
		t.Fatalf("revision = %d", st.Revision)
	}
}

func TestApplyNeedsVerifiedFeed(t *testing.T) {
	h := newHarness(t)
	if _, err := h.apply(); !errors.Is(err, updates.ErrNotVerified) {
		t.Fatalf("apply = %v", err)
	}
	// The verified feed expires: re-verified on Apply.
	h.publish(spec{rev: 1, engines: []eng{{"0.1.0", 1}}, cares: []string{"A"}, expires: h.now.Add(time.Hour)})
	h.check()
	h.now = h.now.Add(2 * time.Hour)
	if _, err := h.apply(); !errors.Is(err, feed.ErrExpired) {
		t.Fatalf("apply = %v", err)
	}
}

func TestNotAvailable(t *testing.T) {
	h := newHarness(t)
	h.publish(spec{rev: 1, engines: []eng{{"0.1.0", 1}}, cares: []string{"A"}, careNeeds: ">=2.0.0"})
	r := h.check()
	if r.Available || r.Reason == "" {
		t.Fatalf("check = %+v", r)
	}
	if _, err := h.apply(); !errors.Is(err, updates.ErrNotAvailable) || !strings.Contains(err.Error(), r.Reason) {
		t.Fatalf("apply = %v", err)
	}
}

func TestDesktopUpdateDownloadsOnly(t *testing.T) {
	h := newHarness(t)
	h.installCare("A")
	h.publish(spec{rev: 2, desktops: []string{"1.0.0", "1.1.0"}, engines: []eng{{"0.1.0", 1}}, cares: []string{"A"}})
	r := h.check()
	if steps(r) != "desktop 1.0.0->1.1.0" || !r.Prefetched {
		t.Fatalf("check = %+v", r)
	}
	res, err := h.apply()
	if !errors.Is(err, updates.ErrDesktopUpdate) || res.Desktop == nil || res.Desktop.Version != "1.1.0" {
		t.Fatalf("apply = %+v, %v", res, err)
	}
	if b, err := os.ReadFile(res.Desktop.Path); err != nil || string(b) != "installer 1.1.0" {
		t.Fatalf("installer = %q, %v", b, err)
	}
	h.wantOn("0.1.0", "A")
}

func TestOneOperationAtATime(t *testing.T) {
	h := newHarness(t)
	h.installCare("A")
	h.publish(revB)
	h.check()
	in, release := make(chan struct{}), make(chan struct{})
	h.hook.set(func(p string) error {
		if p == "before:"+updates.StepMigrate {
			close(in)
			<-release
		}
		return nil
	})
	var wg sync.WaitGroup
	wg.Go(func() {
		if _, err := h.apply(); err != nil {
			t.Errorf("apply: %v", err)
		}
	})
	<-in
	if _, err := h.c.Check(context.Background()); !errors.Is(err, updates.ErrBusy) {
		t.Errorf("concurrent check = %v", err)
	}
	// Another coordinator on the same dir (another process).
	other := h.open()
	if err := other.Resume(context.Background()); !errors.Is(err, updates.ErrBusy) {
		t.Errorf("other process resume = %v", err)
	}
	if j := h.status().Journal; j == nil || j.State != updates.StateRunning {
		t.Errorf("journal = %+v", j)
	}
	close(release)
	wg.Wait()
	h.wantOn("0.1.0", "B")
}

func TestPrefetchErrorIsReported(t *testing.T) {
	h := newHarness(t)
	h.installCare("A")
	h.publish(revB)
	h.w.FailOn("EnsureImages", 1)
	r := h.check()
	if !r.Available || r.Prefetched || !strings.Contains(r.PrefetchError, "injected") {
		t.Fatalf("check = %+v", r)
	}
	h.mustApply()
	h.wantOn("0.1.0", "B")
}

func TestDownloadRejectsBadChecksum(t *testing.T) {
	h := newHarness(t)
	h.publish(spec{rev: 1, engines: []eng{{"0.1.0", 1}}, cares: []string{"A"}})
	h.serve("/engine/0.1.0", []byte("tampered"))
	r := h.check()
	if r.Prefetched || !strings.Contains(r.PrefetchError, "bytes") && !strings.Contains(r.PrefetchError, "sha256") {
		t.Fatalf("check = %+v", r)
	}
	_, err := h.apply()
	if oe, ok := errors.AsType[*updates.OpError](err); !ok || oe.Step != updates.StepStage {
		t.Fatalf("apply = %v", err)
	}
	if len(h.w.Slots) != 0 {
		t.Fatal("a bad bundle was installed")
	}
}

// A clinic installed without the coordinator (installed.json absent) is
// adopted when its stack runs a release in the feed, so the update takes a
// backup like any other.
func TestAdoptInstalledClinic(t *testing.T) {
	h := newHarness(t)
	h.installCare("A")
	if err := os.Remove(filepath.Join(h.dir, updates.InstalledFile)); err != nil {
		t.Fatal(err)
	}
	h.restart()
	h.publish(revB)
	if r := h.check(); steps(r) != "care "+careVersion("A")+"->"+careVersion("B") {
		t.Fatalf("check = %+v", r)
	}
	st := h.status()
	if st.Installed.Engine == nil || st.Installed.Engine.Version != "0.1.0" || st.Installed.Care == nil || st.Installed.Care.Version != careVersion("A") || len(st.Installed.Care.Images) == 0 {
		t.Fatalf("adopted = %+v", st.Installed)
	}
	h.mustApply()
	h.wantOn("0.1.0", "B")
	if e := h.lastHistory(); e.BackupID == "" {
		t.Fatalf("adopted clinic updated without a backup: %+v", e)
	}
}

func TestUnknownClinicIsNotUpdated(t *testing.T) {
	h := newHarness(t)
	h.installCare("A")
	if err := os.Remove(filepath.Join(h.dir, updates.InstalledFile)); err != nil {
		t.Fatal(err)
	}
	h.w.Lock()
	h.w.Env["backend"] = protocol.Digest("sha256:" + strings.Repeat("e", 64))
	h.w.Unlock()
	h.restart()
	h.publish(revB)
	r := h.check()
	if r.Available || !strings.Contains(r.Reason, "not a release in the update feed") {
		t.Fatalf("check = %+v", r)
	}
	before := len(h.w.CallLog())
	if _, err := h.apply(); !errors.Is(err, updates.ErrNotAvailable) || !errors.Is(err, updates.ErrUnknownCare) {
		t.Fatalf("apply = %v", err)
	}
	for _, c := range h.w.CallLog()[before:] {
		if c != "Slots" && !strings.HasPrefix(c, "op stack.status") {
			t.Fatalf("apply touched the clinic: %q", h.w.CallLog()[before:])
		}
	}
}
