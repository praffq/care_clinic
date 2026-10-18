package updates

import (
	"context"
	"errors"
	"fmt"

	"github.com/ohcnetwork/care_desktop/app/internal/feed"
)

// Desktop installs desktop app updates (F8); the app implements it. Without
// one (Options.Desktop nil) Apply stops at a desktop step with
// ErrDesktopUpdate and leaves installing to the caller.
type Desktop interface {
	// Verify checks a downloaded installer before it is used: on macOS that
	// the new bundle is signed by the same team as the running app.
	Verify(ctx context.Context, installer, version string) error
	// Handoff starts the updater helper, which replaces the app with the
	// installer's once this process quits and relaunches it. The caller
	// quits when Apply or Resume returns ErrRestartRequired.
	Handoff(ctx context.Context, installer, version string) error
	// UpdateService reinstalls the host service when the running app
	// bundles a different care-clinic-service; nil when it is current.
	UpdateService(ctx context.Context) error
}

// Desktop update steps (F8), in order. The first three run in the old app,
// the rest in the relaunched one.
const (
	StepDesktopDownload = "download-installer"
	StepDesktopVerify   = "verify-installer"
	StepHandoff         = "handoff"
	StepRelaunch        = "relaunch"
	StepServiceUpdate   = "update-service"
	StepDesktopHealth   = "health"
)

var desktopSteps = []string{StepDesktopDownload, StepDesktopVerify, StepHandoff, StepRelaunch, StepServiceUpdate, StepDesktopHealth}

var (
	// ErrRestartRequired means the updater helper is waiting for the app
	// to quit; the relaunched app calls Resume to finish the update.
	ErrRestartRequired = errors.New("restart CARE Clinic to finish the update")
	// ErrIncomplete means the desktop app was replaced but a later step
	// failed; Resume retries it and Rollback abandons it.
	ErrIncomplete = errors.New("the desktop app was updated, but the update did not finish")
)

type permanentError struct{ err error }

func (e permanentError) Error() string { return e.err.Error() }
func (e permanentError) Unwrap() error { return e.err }

// MarkPermanent marks a step error that retrying cannot fix (the operator
// cancelled a prompt, the signature does not match): the step fails at once.
func MarkPermanent(err error) error {
	if err == nil {
		return nil
	}
	return permanentError{err}
}

func (c *Coordinator) newDesktopJournal(ctx context.Context, j *Journal, in Installed, s feed.Step) error {
	if c.o.Desktop == nil {
		return ErrDesktopUpdate
	}
	a, ok := s.Desktop.Assets[c.o.DesktopPlatform]
	if !ok {
		return fmt.Errorf("desktop %s has no %s installer", s.To, c.o.DesktopPlatform)
	}
	t := &DesktopTarget{Version: s.To, URL: a.URL, SHA256: a.SHA256, Size: a.Size}
	// The health step waits for what is running now to come back after the
	// service is reinstalled; a stopped clinic stays stopped.
	if st, err := c.o.Agent.Status(ctx); err == nil && st.DockerReady {
		t.Engine = st.EngineVersion
		t.WithCare = in.Care != nil
	}
	j.Desktop = t
	return nil
}

func (c *Coordinator) desktopForward() map[string]stepFunc {
	return map[string]stepFunc{
		StepDesktopDownload: func(ctx context.Context, j *Journal) error {
			if c.relaunched(j) {
				return nil
			}
			t := j.Desktop
			path, err := c.downloadWith(ctx, t.URL, t.SHA256, t.Size, c.o.Progress)
			if err != nil {
				if errors.Is(err, ErrChecksum) {
					return MarkPermanent(err)
				}
				return err
			}
			t.Path = path
			return nil
		},
		StepDesktopVerify: func(ctx context.Context, j *Journal) error {
			if c.relaunched(j) {
				return nil
			}
			if got, _, err := hashFile(j.Desktop.Path); err != nil || got != j.Desktop.SHA256 {
				return fmt.Errorf("the downloaded installer changed since it was verified")
			}
			return MarkPermanent(c.o.Desktop.Verify(ctx, j.Desktop.Path, j.Desktop.Version))
		},
		StepHandoff: func(ctx context.Context, j *Journal) error {
			if c.relaunched(j) {
				return nil
			}
			if err := c.o.Desktop.Handoff(ctx, j.Desktop.Path, j.Desktop.Version); err != nil {
				return err
			}
			c.logf("Handed off the desktop update to %s; waiting for the app to restart", j.To)
			j.State = StateRestarting
			return nil
		},
		StepRelaunch: func(ctx context.Context, j *Journal) error {
			if !c.relaunched(j) {
				return MarkPermanent(fmt.Errorf("CARE Clinic is still version %s: the new app was not installed (see the update log)", c.o.DesktopVersion))
			}
			return nil
		},
		StepServiceUpdate: func(ctx context.Context, j *Journal) error {
			return c.o.Desktop.UpdateService(ctx)
		},
		StepDesktopHealth: func(ctx context.Context, j *Journal) error {
			t := j.Desktop
			if t.Engine == "" {
				return nil
			}
			if _, err := c.waitEngine(ctx, t.Engine); err != nil {
				return err
			}
			if !t.WithCare {
				return nil
			}
			if err := c.stackUp(ctx); err != nil {
				return err
			}
			return c.o.Stack.Health(ctx)
		},
	}
}

// relaunched reports whether this process is the new app.
func (c *Coordinator) relaunched(j *Journal) bool {
	return j.Component == feed.ComponentDesktop && c.o.DesktopVersion == j.To
}

// desktopReplaced reports whether j's app swap already happened, so it can
// no longer be rolled back.
func desktopReplaced(j *Journal) bool {
	if j.Component != feed.ComponentDesktop {
		return false
	}
	for _, s := range j.Steps {
		if s.Name == StepRelaunch {
			return s.Status == StepDone
		}
	}
	return false
}

// PrepareDesktop downloads (or resumes downloading) the desktop installer the
// verified feed's plan needs, reporting progress; it installs nothing.
func (c *Coordinator) PrepareDesktop(ctx context.Context, progress Progress) (*DesktopDownload, error) {
	unlock, err := c.acquire()
	if err != nil {
		return nil, err
	}
	defer unlock()
	m, _, err := c.manifest()
	if err != nil {
		return nil, err
	}
	in, err := c.installed()
	if err != nil {
		return nil, err
	}
	if in, err = c.adopt(ctx, m, in); err != nil && !errors.Is(err, ErrUnknownCare) {
		return nil, err
	}
	p, err := c.resolve(m, in)
	if err != nil {
		return nil, err
	}
	for _, s := range p.Steps {
		if s.Component != feed.ComponentDesktop {
			continue
		}
		a, ok := s.Desktop.Assets[c.o.DesktopPlatform]
		if !ok {
			return nil, fmt.Errorf("desktop %s has no %s installer", s.To, c.o.DesktopPlatform)
		}
		path, err := c.downloadWith(ctx, a.URL, a.SHA256, a.Size, progress)
		if err != nil {
			return nil, err
		}
		return &DesktopDownload{Version: s.To, Path: path}, nil
	}
	return nil, fmt.Errorf("%w: the plan has no desktop update", ErrNotAvailable)
}
