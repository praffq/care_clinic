package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/updates"
)

// Job labels (care-done) of the clinic update screen.
const (
	updatesJob     = "updates"
	updateCheckJob = "update-check"
)

// historyShown caps the history the Updates screen lists.
const historyShown = 20

// clinicUpdater is the update coordinator as the Updates screen uses it.
type clinicUpdater interface {
	journalSource
	Check(context.Context) (*updates.CheckResult, error)
	Apply(context.Context) (*updates.ApplyResult, error)
	Resume(context.Context) error
	Rollback(context.Context) error
	ReleaseNotes([]updates.PlannedStep) ([]updates.ReleaseNote, error)
}

// clinicCheck is the result of the last check, kept for the session: a
// check pre-fetches, so the screen shows it again rather than repeating it.
type clinicCheck struct {
	mu     sync.Mutex
	result *updates.CheckResult
	err    string
	at     time.Time
}

func (c *clinicCheck) set(r *updates.CheckResult, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.result, c.err, c.at = r, "", time.Now()
	if err != nil {
		c.result, c.err = nil, err.Error()
	}
}

func (c *clinicCheck) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.result, c.err, c.at = nil, "", time.Time{}
}

func (c *clinicCheck) get() (*updates.CheckResult, string, time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.result, c.err, c.at
}

// Why a check found nothing to apply.
const (
	blockerUpToDate     = "up_to_date"
	blockerNeedsDesktop = "needs_desktop"
	blockerBlocked      = "blocked"
	blockerUnknownCare  = "unknown_care"
	blockerNoPath       = "no_path"
)

// ClinicUpdates is the Updates screen: installed versions, the last check
// and its release notes, an unfinished operation and recent history.
type ClinicUpdates struct {
	Installed updates.Installed `json:"installed"`
	// VerifiedAt is when a feed was last verified; Unsigned, whether that
	// feed was unsigned.
	VerifiedAt time.Time `json:"verified_at,omitzero"`
	Unsigned   bool      `json:"unsigned,omitempty"`
	// Check is this session's last check; CheckError why it failed.
	Check      *updates.CheckResult `json:"check,omitempty"`
	CheckError string               `json:"check_error,omitempty"`
	CheckedAt  time.Time            `json:"checked_at,omitzero"`
	// Blocker classifies Check.Reason when nothing is available.
	Blocker string                `json:"blocker,omitempty"`
	Notes   []updates.ReleaseNote `json:"notes,omitempty"`
	// Journal is an unfinished operation (also a plugin or frontend
	// change); History, finished ones, newest first.
	Journal *updates.Journal       `json:"journal,omitempty"`
	History []updates.HistoryEntry `json:"history"`
}

func (a *App) clinicUpdater(ctx context.Context) (clinicUpdater, error) {
	if a.hooks.updater != nil {
		return a.hooks.updater(ctx)
	}
	return a.pluginCoordinator(ctx)
}

// ClinicUpdateStatus reads the update state. It takes no job lock, so the
// screen can follow an operation while it runs.
func (a *App) ClinicUpdateStatus() (out ClinicUpdates, err error) {
	defer a.logError(&err)
	if err := a.requireSetup(); err != nil {
		return out, err
	}
	u, err := a.clinicUpdater(a.clientContext())
	if err != nil {
		return out, err
	}
	st, err := u.Status()
	if err != nil {
		return out, err
	}
	out = ClinicUpdates{Installed: st.Installed, VerifiedAt: st.CheckedAt, Unsigned: st.Unsigned, Journal: st.Journal, History: []updates.HistoryEntry{}}
	if st.Installed.Plugins != nil {
		// The screen shows versions; plugin configs stay in Advanced.
		out.Installed.Plugins = nil
	}
	for _, h := range slices.Backward(st.History) {
		if len(out.History) == historyShown {
			break
		}
		out.History = append(out.History, h)
	}
	out.Check, out.CheckError, out.CheckedAt = a.lastCheck.get()
	if c := out.Check; c != nil {
		out.Unsigned = out.Unsigned || c.Unsigned
		if !c.Available {
			out.Blocker = checkBlocker(c.Reason)
		}
		if out.Notes, err = u.ReleaseNotes(c.Steps); err != nil {
			a.logln("Release notes unavailable: " + err.Error())
			out.Notes, err = nil, nil
		}
	}
	return out, nil
}

func checkBlocker(reason string) string {
	switch {
	case reason == updates.ErrUpToDate.Error():
		return blockerUpToDate
	case strings.Contains(reason, "is not a release in the update feed"):
		return blockerUnknownCare
	case strings.Contains(reason, "is blocked"):
		return blockerBlocked
	case strings.Contains(reason, "requires desktop"):
		return blockerNeedsDesktop
	}
	return blockerNoPath
}

// CheckClinicUpdates fetches and verifies the update feed, plans an update
// and pre-fetches it, as a background job (care-done "update-check").
// ClinicUpdateStatus then has the result.
func (a *App) CheckClinicUpdates() (err error) {
	defer a.logError(&err)
	return a.run(func() error {
		if err := a.requireSetup(); err != nil {
			return err
		}
		ctx := a.clientContext()
		u, err := a.clinicUpdater(ctx)
		if err != nil {
			return err
		}
		a.logln("Checking for clinic updates...")
		r, err := u.Check(ctx)
		a.lastCheck.set(r, err)
		if err == nil && !r.Available {
			a.logln("No clinic update to apply: " + r.Reason)
		}
		return err
	}, false, updateCheckJob)
}

// ApplyClinicUpdates installs the checked plan as a background job
// (care-done "updates"), emitting "update-journal" as its steps run.
func (a *App) ApplyClinicUpdates(adminPassword string) error {
	return a.updateOp(adminPassword, "Applying the clinic update...", func(ctx context.Context, u clinicUpdater) error {
		res, err := u.Apply(ctx)
		if errors.Is(err, updates.ErrDesktopUpdate) {
			to := ""
			if res != nil && res.Desktop != nil {
				to = " " + res.Desktop.Version
			}
			return errors.New("the rest of this update needs CARE Clinic" + to + " first: update the app from the CARE Clinic application card, then apply again")
		}
		return err
	})
}

// ResumeClinicUpdate continues an interrupted operation.
func (a *App) ResumeClinicUpdate(adminPassword string) error {
	return a.updateOp(adminPassword, "Resuming the interrupted update...", func(ctx context.Context, u clinicUpdater) error {
		return u.Resume(ctx)
	})
}

// RollBackClinicUpdate undoes an interrupted operation or retries a failed
// rollback.
func (a *App) RollBackClinicUpdate(adminPassword string) error {
	return a.updateOp(adminPassword, "Rolling back the interrupted update...", func(ctx context.Context, u clinicUpdater) error {
		return u.Rollback(ctx)
	})
}

func (a *App) updateOp(adminPassword, msg string, fn func(context.Context, clinicUpdater) error) (err error) {
	defer a.logError(&err)
	if err := a.requireAdmin(adminPassword); err != nil {
		return err
	}
	return a.run(func() error {
		if err := a.requireSetup(); err != nil {
			return err
		}
		ctx := a.clientContext()
		u, err := a.clinicUpdater(ctx)
		if err != nil {
			return err
		}
		a.logln(msg)
		stop := a.watchJournal(u)
		err = fn(ctx, u)
		stop()
		// The plan is stale once anything ran.
		a.lastCheck.clear()
		return err
	}, false, updatesJob)
}
