package main

import (
	"context"
	"errors"

	"github.com/wailsapp/wails/v2/pkg/options"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.fitWindowToScreen()
	if a.osUninstall {
		return
	}
	a.refreshInstallDir()
	// The service owns the engine, forwarding, mDNS and the watchdog; the
	// app only watches its status for the control panel.
	a.startServiceWatch()
}

const screenMargin = 80

func (a *App) fitWindowToScreen() {
	if a.ctx == nil || a.ctx.Value("frontend") == nil {
		return
	}
	screens, err := wruntime.ScreenGetAll(a.ctx)
	if err != nil {
		return
	}
	screen, ok := currentScreen(screens)
	if !ok {
		return
	}
	width := max(min(windowWidth, screen.Size.Width-screenMargin), windowMinWidth)
	height := max(min(windowHeight, screen.Size.Height-screenMargin), windowMinHeight)
	if width == windowWidth && height == windowHeight {
		return
	}
	wruntime.WindowSetSize(a.ctx, width, height)
	wruntime.WindowCenter(a.ctx)
}

func currentScreen(screens []wruntime.Screen) (wruntime.Screen, bool) {
	var fallback wruntime.Screen
	found := false
	for _, s := range screens {
		if s.Size.Width <= 0 || s.Size.Height <= 0 {
			continue
		}
		if s.IsCurrent {
			return s, true
		}
		if !found || s.IsPrimary {
			fallback, found = s, true
		}
	}
	return fallback, found
}

// refreshInstallDir brings the local copy of the deployment kit up to date
// with this version. It takes effect the next time the clinic is
// configured (setup or Start).
func (a *App) refreshInstallDir() {
	if err := a.withJob(func() error {
		cfg := a.loadConfig()
		if cfg.Role != roleServer || !cfg.SetupDone || cfg.Removing {
			return nil
		}
		_, err := a.ensureInstallDir()
		return err
	}); err != nil {
		a.logln("error: couldn't update the installed configuration (" + err.Error() + ")")
	}
}

func (a *App) shutdown(context.Context) {
	a.closeConfirmations()
	a.stopServiceWatch()
}

const (
	singleInstanceID = "ohc.care-clinic"

	jobService = "service"
	jobNetwork = "network"
)

var errStartedSetup = errors.New("the clinic address can only be chosen before installation")

func (a *App) onSecondInstance(options.SecondInstanceData) {
	if a.ctx == nil {
		return
	}
	wruntime.WindowUnminimise(a.ctx)
	wruntime.Show(a.ctx)
}

func (a *App) beforeClose(context.Context) (prevent bool) {
	if a.quitConfirmed.Load() {
		return false
	}
	if !a.jobMu.TryLock() {
		a.askToQuitDuringJob(a.runningJob())
		return true
	}
	defer a.jobMu.Unlock()
	// The clinic runs in the service, so quitting the app leaves it as is.
	a.closing = true
	return false
}

func (a *App) runningJob() string {
	label, _ := a.activeJob.Load().(string)
	return label
}

func (a *App) askToQuitDuringJob(label string) {
	if a.ctx == nil || !a.busyShown.CompareAndSwap(false, true) {
		return
	}
	if request, ready := a.queueQuitDialog(label); ready {
		a.emit("quit-requested", request)
		return
	}
	go func() {
		defer a.busyShown.Store(false)
		prompt := jobQuitPrompt(label)
		quit, err := a.askToProceed(prompt.title, prompt.message+"\n\nQuit anyway?", "Quit")
		if err != nil || !quit {
			return
		}
		a.logln("Quitting while " + prompt.doing + " was still running.")
		a.quitConfirmed.Store(true)
		wruntime.Quit(a.ctx)
	}()
}

type quitPrompt struct {
	doing   string
	title   string
	message string
}

const leftRunning = "The clinic itself keeps running in the CARE Clinic service."

func jobQuitPrompt(label string) quitPrompt {
	switch label {
	case "setup":
		return quitPrompt{"setup", "Quit while setup is running?",
			"Setup stops where it is. The next time you open CARE Clinic you can continue " +
				"setting up or remove the unfinished install."}
	case jobService:
		return quitPrompt{"the service install", "Quit while the CARE Clinic service is being installed?",
			"If the administrator prompt was already accepted, the install finishes on its own. " +
				"Open CARE Clinic again to check it."}
	case "start", "restart":
		return quitPrompt{"starting CARE", "Quit while CARE is starting?",
			"The service keeps starting the clinic in the background. Open CARE Clinic again to see how it went."}
	case "stop":
		return quitPrompt{"stopping CARE", "Quit while CARE is stopping?",
			"The service keeps stopping the clinic in the background. Open CARE Clinic again to check that it stopped."}
	case "uninstall":
		return quitPrompt{"removal", "Quit while CARE is being removed?",
			"CARE is being removed from this computer. Quitting now can leave it partly removed; " +
				"open CARE Clinic again and remove it once more to finish."}
	case "app-update":
		return quitPrompt{"the CARE Clinic update", "Quit during the CARE Clinic update?",
			"The update stops and the version you have keeps working; you can update again later. " +
				"If macOS is already replacing the app, that finishes on its own."}
	}
	return quitPrompt{"an operation", "Quit while CARE Clinic is working?",
		"CARE Clinic is in the middle of an operation (see the log). It stops where it is.\n\n" + leftRunning}
}
