package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/feed"
	"github.com/ohcnetwork/care_desktop/app/internal/service/control"
	"github.com/ohcnetwork/care_desktop/app/internal/service/install"
	"github.com/ohcnetwork/care_desktop/app/internal/stack"
	"github.com/ohcnetwork/care_desktop/app/internal/updates"
)

// AppUpdateOutcome is how the last desktop self-update ended, as seen by the
// relaunched app: State is "" (nothing to report), "finishing", "done" or
// "failed".
type AppUpdateOutcome struct {
	State   string `json:"state"`
	Version string `json:"version"`
	Error   string `json:"error,omitempty"`
	// CanRetry: the new app is in place but a later step (service, health)
	// did not finish; FinishAppUpdate tries again.
	CanRetry bool `json:"can_retry,omitempty"`
}

const (
	outcomeFinishing = "finishing"
	outcomeDone      = "done"
	outcomeFailed    = "failed"

	servicePingTimeout = 3 * time.Second
)

// GetAppUpdateOutcome returns the last self-update's outcome; the UI also
// gets it as the "app-update-outcome" event.
func (a *App) GetAppUpdateOutcome() AppUpdateOutcome {
	a.appUpdateMu.Lock()
	defer a.appUpdateMu.Unlock()
	return a.outcome
}

func (a *App) setOutcome(o AppUpdateOutcome) {
	a.appUpdateMu.Lock()
	a.outcome = o
	a.appUpdateMu.Unlock()
	a.emit("app-update-outcome", o)
}

// desktopCoordinator opens the update coordinator for desktop updates when
// this computer is the clinic server and its service answers; nil (no
// error) means use the GitHub release instead.
func (a *App) desktopCoordinator() (*updates.Coordinator, error) {
	if a.loadConfig().Role != roleServer {
		return nil, nil
	}
	c, ok := a.svc().(*control.Client)
	if !ok {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(a.clientContext(), servicePingTimeout)
	defer cancel()
	if _, err := c.Status(ctx); err != nil {
		a.logln("The CARE Clinic service isn't answering (" + err.Error() + "); checking the latest release instead.")
		return nil, nil
	}
	return a.openDesktopCoordinator(c)
}

// openDesktopCoordinator shares its state directory (and so its journal and
// lock) with the plugin coordinator and care-clinic-service update.
func (a *App) openDesktopCoordinator(c *control.Client) (*updates.Coordinator, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return nil, err
	}
	keys, err := feed.DefaultKeys()
	if err != nil {
		return nil, err
	}
	clinicState := a.stateDir
	if clinicState == "" {
		if clinicState, err = stack.DefaultStateDir(); err != nil {
			return nil, err
		}
	}
	url := os.Getenv("CARE_UPDATES_URL")
	if url == "" {
		url = defaultUpdatesURL
	}
	return updates.Open(updates.Options{
		Dir: filepath.Join(dir, "care-clinic", "updates"), Keys: keys, AllowInsecureURLs: feed.DevBuild,
		AllowUnsigned: feed.UnsignedFeedAllowed(),
		Feed:          updates.HTTPFetcher{URL: url},
		Service:       c, Agent: updates.ClientAgent{Control: c, Log: a.logln},
		Stack: updates.RuntimeStack{
			Runtime: &stack.Runtime{Service: c, StateDir: clinicState, Log: a.logln},
			Kit:     os.DirFS(a.installDir()), Host: a.engine().Host(),
		},
		DesktopVersion: a.desktopVersion(), Log: a.logln,
		Desktop:  appDesktop{a},
		Progress: func(done, total int64) { a.updateProgress(phaseDownloading, done, total) },
	})
}

// afterDesktopStep finishes RestartToUpdate or a resume: quit for the helper,
// or keep the running app.
func (a *App) afterDesktopStep(err error) error {
	if errors.Is(err, updates.ErrRestartRequired) {
		return a.commitHandoff()
	}
	a.abortHandoff()
	return err
}

// appDesktop is the app side of a journaled desktop update.
type appDesktop struct{ a *App }

func (d appDesktop) Verify(_ context.Context, installer, version string) error {
	_, err := d.prepared(installer, version)
	return updates.MarkPermanent(err)
}

func (d appDesktop) prepared(installer, version string) (*preparedUpdate, error) {
	digest, err := fileSHA256(installer)
	if err != nil {
		return nil, err
	}
	d.a.appUpdateMu.Lock()
	p := d.a.prepared
	d.a.appUpdateMu.Unlock()
	if p != nil && p.Version == version && p.Digest == digest {
		return p, nil
	}
	if p, err = prepareAppUpdate(installer, version, digest); err != nil {
		return nil, err
	}
	p.Source = sourceFeed
	d.a.setPrepared(p)
	return p, nil
}

func (d appDesktop) Handoff(_ context.Context, installer, version string) error {
	p, err := d.prepared(installer, version)
	if err != nil {
		return updates.MarkPermanent(err)
	}
	return d.a.startHandoff(p)
}

func (d appDesktop) UpdateService(context.Context) error {
	return updates.MarkPermanent(d.a.updateServiceIfNeeded())
}

// updateServiceIfNeeded reinstalls care-clinic-service from this app when
// the installed binary (or, on macOS, LaunchDaemon plist) differs, behind one
// administrator prompt. The install script swaps the binary and restarts
// the service, which stops the clinic VM cleanly and boots it again; the
// engine data and slots are not touched.
func (a *App) updateServiceIfNeeded() error {
	if !install.Supported || !install.Installed() {
		return nil
	}
	path, err := a.bundleLayout().ServicePath()
	if err != nil {
		a.logln("This copy of CARE Clinic bundles no service to update (" + err.Error() + ").")
		return nil
	}
	u, err := user.Current()
	if err != nil {
		return fmt.Errorf("couldn't identify the logged-in user: %w", err)
	}
	plan := servicePlan(path, u)
	if ok, err := install.Current(plan); err != nil {
		return fmt.Errorf("couldn't compare the installed CARE Clinic service: %w", err)
	} else if ok {
		a.logln("The CARE Clinic service is already up to date.")
		return nil
	}
	a.logln("Updating the CARE Clinic service from " + path + ". The clinic restarts; its data is kept.")
	inst := a.installService
	if inst == nil {
		inst = install.Install
	}
	if err := inst(plan); err != nil {
		return fmt.Errorf("couldn't update the CARE Clinic service: %w", err)
	}
	return a.waitServiceReachable(serviceStartTimeout)
}

// resumeAppUpdate runs at startup: when a desktop update handed off to the
// helper (or stopped part way), it finishes the journaled steps (service,
// health, the rest of the plan) in the background.
func (a *App) resumeAppUpdate() {
	if a.loadConfig().Role != roleServer {
		return
	}
	c, ok := a.svc().(*control.Client)
	if !ok {
		return
	}
	u, err := a.openDesktopCoordinator(c)
	if err != nil {
		a.logln("error: couldn't open the update coordinator: " + err.Error())
		return
	}
	st, err := u.Status()
	if err != nil {
		a.logln("error: couldn't read the update journal: " + err.Error())
		return
	}
	if j := st.Journal; j == nil || j.Component != feed.ComponentDesktop {
		return
	}
	if err := a.finishAppUpdate(u, st.Journal.To); err != nil {
		a.logln("error: couldn't finish the CARE Clinic update: " + err.Error())
	}
}

// FinishAppUpdate retries the unfinished steps of a desktop update.
func (a *App) FinishAppUpdate() error {
	c, ok := a.svc().(*control.Client)
	if !ok || a.loadConfig().Role != roleServer {
		return a.logged(errors.New("only the clinic server finishes app updates"))
	}
	u, err := a.openDesktopCoordinator(c)
	if err != nil {
		return a.logged(err)
	}
	st, err := u.Status()
	if err != nil {
		return a.logged(err)
	}
	if st.Journal == nil || st.Journal.Component != feed.ComponentDesktop {
		return a.logged(errors.New("no unfinished CARE Clinic update"))
	}
	return a.finishAppUpdate(u, st.Journal.To)
}

func (a *App) finishAppUpdate(u *updates.Coordinator, version string) error {
	return a.runJob(func() error {
		a.setOutcome(AppUpdateOutcome{State: outcomeFinishing, Version: version})
		a.logln("Finishing the update to CARE Clinic " + version + "...")
		err := u.Resume(a.clientContext())
		if errors.Is(err, updates.ErrRestartRequired) {
			return a.afterDesktopStep(err)
		}
		if err != nil {
			a.setOutcome(AppUpdateOutcome{State: outcomeFailed, Version: version, Error: err.Error(), CanRetry: errors.Is(err, updates.ErrIncomplete)})
			return err
		}
		a.logln("CARE Clinic " + version + " is installed and the clinic is back.")
		a.setOutcome(AppUpdateOutcome{State: outcomeDone, Version: version})
		return nil
	}, false, "app-update")
}
