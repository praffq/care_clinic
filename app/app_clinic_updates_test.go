package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/feed"
	"github.com/ohcnetwork/care_desktop/app/internal/updates"
)

// events records what the app emits.
type events struct {
	mu   sync.Mutex
	list []event
}

type event struct {
	name string
	data []any
}

func (e *events) add(name string, data ...any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.list = append(e.list, event{name, data})
}

func (e *events) named(name string) [][]any {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out [][]any
	for _, ev := range e.list {
		if ev.name == name {
			out = append(out, ev.data)
		}
	}
	return out
}

// done returns the care-done code of label.
func (e *events) done(t *testing.T, label string) int {
	t.Helper()
	for _, d := range e.named("care-done") {
		if d[1] == label {
			return d[0].(int)
		}
	}
	t.Fatalf("no care-done for %s", label)
	return -1
}

func (e *events) errors() []string {
	var out []string
	for _, d := range e.named("care-error") {
		out = append(out, d[0].(string)+": "+d[1].(string))
	}
	return out
}

// panelApp is a set-up clinic server whose events are recorded.
func panelApp(t *testing.T) (*App, *events) {
	t.Helper()
	a := settingsApp(t)
	a.cfg.Role = roleServer
	ev := &events{}
	a.hooks.event = ev.add
	return a, ev
}

type fakeUpdater struct {
	mu      sync.Mutex
	status  updates.Status
	check   *updates.CheckResult
	err     error
	calls   []string
	desktop bool
	notes   []updates.ReleaseNote
	// steps, when set, are written to the journal one by one by Apply.
	steps []string
}

func (f *fakeUpdater) Status() (updates.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.status
	if j := st.Journal; j != nil {
		cp := *j
		cp.Steps = slices.Clone(j.Steps)
		st.Journal = &cp
	}
	return st, nil
}

func (f *fakeUpdater) call(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name)
	return f.err
}

func (f *fakeUpdater) Check(context.Context) (*updates.CheckResult, error) {
	if err := f.call("check"); err != nil {
		return nil, err
	}
	return f.check, nil
}

func (f *fakeUpdater) Apply(context.Context) (*updates.ApplyResult, error) {
	if err := f.call("apply"); err != nil {
		return nil, err
	}
	if f.desktop {
		return &updates.ApplyResult{Desktop: &updates.DesktopDownload{Version: "2.0.0"}}, updates.ErrDesktopUpdate
	}
	if len(f.steps) > 0 {
		j := &updates.Journal{ID: "op1", Component: feed.ComponentCare, To: "2.0.0", State: updates.StateRunning}
		for _, s := range f.steps {
			j.Steps = append(j.Steps, updates.StepRecord{Name: s, Status: updates.StepPending})
		}
		for i := range j.Steps {
			f.mu.Lock()
			j.Steps[i].Status = updates.StepRunning
			cp := *j
			cp.Steps = slices.Clone(j.Steps)
			f.status.Journal = &cp
			f.mu.Unlock()
			time.Sleep(3 * journalPoll)
			j.Steps[i].Status = updates.StepDone
		}
		f.mu.Lock()
		f.status.Journal = nil
		f.status.History = append(f.status.History, updates.HistoryEntry{ID: "op1", Component: feed.ComponentCare, To: "2.0.0", Result: updates.ResultApplied})
		f.mu.Unlock()
	}
	return &updates.ApplyResult{}, nil
}

func (f *fakeUpdater) Resume(context.Context) error   { return f.call("resume") }
func (f *fakeUpdater) Rollback(context.Context) error { return f.call("rollback") }

func (f *fakeUpdater) ReleaseNotes(steps []updates.PlannedStep) ([]updates.ReleaseNote, error) {
	if len(steps) == 0 {
		return nil, nil
	}
	return f.notes, nil
}

func updatesApp(t *testing.T, u *fakeUpdater) (*App, *events) {
	t.Helper()
	a, ev := panelApp(t)
	a.hooks.updater = func(context.Context) (clinicUpdater, error) { return u, nil }
	return a, ev
}

func TestClinicUpdateStatusListsHistoryNewestFirst(t *testing.T) {
	u := &fakeUpdater{}
	for i := range 25 {
		u.status.History = append(u.status.History, updates.HistoryEntry{ID: string(rune('a' + i)), Result: updates.ResultApplied})
	}
	u.status.Installed.Plugins = &updates.InstalledPlugins{}
	u.status.Unsigned = true
	a, _ := updatesApp(t, u)
	st, err := a.ClinicUpdateStatus()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.History) != historyShown || st.History[0].ID != "y" || st.History[historyShown-1].ID != "f" {
		t.Fatalf("history = %+v", st.History)
	}
	if st.Installed.Plugins != nil || !st.Unsigned || st.Check != nil || st.Notes != nil {
		t.Fatalf("status = %+v", st)
	}
}

func TestCheckClinicUpdatesKeepsTheResult(t *testing.T) {
	u := &fakeUpdater{
		check: &updates.CheckResult{Revision: 7, Available: true, Unsigned: true, Steps: []updates.PlannedStep{{Component: feed.ComponentCare, From: "1.0.0", To: "2.0.0"}}},
		notes: []updates.ReleaseNote{{Component: feed.ComponentCare, Version: "2.0.0"}},
	}
	a, ev := updatesApp(t, u)
	if err := a.CheckClinicUpdates(); err != nil {
		t.Fatal(err)
	}
	waitJob(t, a)
	if code := ev.done(t, updateCheckJob); code != 0 {
		t.Fatalf("care-done = %d: %v", code, ev.errors())
	}
	st, err := a.ClinicUpdateStatus()
	if err != nil {
		t.Fatal(err)
	}
	if st.Check == nil || st.Check.Revision != 7 || !st.Unsigned || st.Blocker != "" || len(st.Notes) != 1 || st.CheckedAt.IsZero() {
		t.Fatalf("status = %+v", st)
	}

	u.err = errors.New("update feed rejected: bad signature")
	if err := a.CheckClinicUpdates(); err != nil {
		t.Fatal(err)
	}
	waitJob(t, a)
	st, _ = a.ClinicUpdateStatus()
	if st.Check != nil || st.CheckError != u.err.Error() {
		t.Fatalf("after a failed check = %+v", st)
	}
	if errs := ev.errors(); len(errs) != 1 || !strings.HasPrefix(errs[0], "Couldn't check for clinic updates: ") {
		t.Fatalf("care-error = %v", errs)
	}
}

func TestCheckBlocker(t *testing.T) {
	for reason, want := range map[string]string{
		updates.ErrUpToDate.Error(): blockerUpToDate,
		"no compatible update path: installed care 1.0.0 is blocked and no compatible replacement exists":                     blockerBlocked,
		"no compatible update path: care 2.0.0 requires desktop >=2.0.0, which is not installed (the desktop app is not ...)": blockerNeedsDesktop,
		updates.ErrUnknownCare.Error():                                       blockerUnknownCare,
		"no compatible update path: care 2.0.0: no update order keeps every": blockerNoPath,
	} {
		if got := checkBlocker(reason); got != want {
			t.Errorf("checkBlocker(%q) = %s, want %s", reason, got, want)
		}
	}
}

func TestApplyClinicUpdatesStreamsTheJournal(t *testing.T) {
	old := journalPoll
	journalPoll = 5 * time.Millisecond
	t.Cleanup(func() { journalPoll = old })
	u := &fakeUpdater{check: &updates.CheckResult{Available: true}, steps: []string{updates.StepBackup, updates.StepMigrate}}
	a, ev := updatesApp(t, u)
	if err := a.ApplyClinicUpdates("wrong"); err == nil {
		t.Fatal("applied with a wrong admin password")
	}
	if err := a.CheckClinicUpdates(); err != nil {
		t.Fatal(err)
	}
	waitJob(t, a)
	if err := a.ApplyClinicUpdates(settingsPassword); err != nil {
		t.Fatal(err)
	}
	waitJob(t, a)
	if code := ev.done(t, updatesJob); code != 0 {
		t.Fatalf("care-done = %d: %v", code, ev.errors())
	}
	var seen []string
	for _, d := range ev.named("update-journal") {
		j, _ := d[0].(*updates.Journal)
		if j == nil {
			seen = append(seen, "none")
			continue
		}
		for _, s := range j.Steps {
			if s.Status == updates.StepRunning {
				seen = append(seen, s.Name)
			}
		}
	}
	// The first poll may come before or after Apply writes the journal.
	seen = slices.Compact(seen)
	if len(seen) > 0 && seen[0] == "none" {
		seen = seen[1:]
	}
	if !slices.Equal(seen, []string{updates.StepBackup, updates.StepMigrate, "none"}) {
		t.Fatalf("journal events = %v", seen)
	}
	if st, _ := a.ClinicUpdateStatus(); st.Check != nil || len(st.History) != 1 {
		t.Fatalf("after apply = %+v", st)
	}
}

func TestApplyClinicUpdatesNeedsTheDesktopFirst(t *testing.T) {
	u := &fakeUpdater{desktop: true}
	a, ev := updatesApp(t, u)
	if err := a.ApplyClinicUpdates(settingsPassword); err != nil {
		t.Fatal(err)
	}
	waitJob(t, a)
	errs := ev.errors()
	if ev.done(t, updatesJob) != 1 || len(errs) != 1 || !strings.Contains(errs[0], "needs CARE Clinic 2.0.0 first") || !strings.HasPrefix(errs[0], "The clinic update didn't finish") {
		t.Fatalf("care-error = %v", errs)
	}
}

func TestResumeAndRollBackClinicUpdate(t *testing.T) {
	u := &fakeUpdater{}
	a, ev := updatesApp(t, u)
	if err := a.ResumeClinicUpdate("wrong"); err == nil {
		t.Fatal("resumed with a wrong admin password")
	}
	if err := a.ResumeClinicUpdate(settingsPassword); err != nil {
		t.Fatal(err)
	}
	waitJob(t, a)
	u.err = updates.ErrRollbackFailed
	if err := a.RollBackClinicUpdate(settingsPassword); err != nil {
		t.Fatal(err)
	}
	waitJob(t, a)
	if !slices.Equal(u.calls, []string{"resume", "rollback"}) {
		t.Fatalf("calls = %v", u.calls)
	}
	if errs := ev.errors(); len(errs) != 1 || !strings.Contains(errs[0], "rollback failed") {
		t.Fatalf("care-error = %v", errs)
	}
}

func TestClinicUpdatesNeedSetup(t *testing.T) {
	a, _ := updatesApp(t, &fakeUpdater{})
	a.cfg.SetupDone = false
	if _, err := a.ClinicUpdateStatus(); err == nil {
		t.Fatal("read updates before setup")
	}
}
