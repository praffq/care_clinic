package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/bundle"
	"github.com/ohcnetwork/care_desktop/app/internal/hostcheck"
	"github.com/ohcnetwork/care_desktop/app/internal/release"
	"github.com/ohcnetwork/care_desktop/app/internal/service/install"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/applog"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/proc"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

type App struct {
	ctx       context.Context
	installFS fs.FS
	pins      *release.Pins
	log       *applog.Logger
	// service reaches the host service; nil means the installed one.
	service serviceAPI

	cfgMu      sync.RWMutex
	cfg        Config
	configFile string

	jobMu     sync.RWMutex
	closing   bool
	activeJob atomic.Value
	busyShown atomic.Bool

	// Owned by jobMu; retains the original setup password in memory only.
	setupAttempt *setupAttempt

	// The clinic root FindClinic validated, held only in memory until Connect
	// pins it, so looking for a clinic writes nothing.
	pendingMu   sync.Mutex
	pendingURL  string
	pendingRoot string

	quitConfirmed atomic.Bool
	quitDialogMu  sync.Mutex
	quitUIReady   bool
	quitSequence  uint64
	quitRequest   *QuitRequest

	confirmationMu       sync.Mutex
	confirmationUIReady  bool
	confirmationClosed   bool
	confirmationSequence uint64
	confirmation         *pendingConfirmation

	// host overrides the onboarding computer checks (tests).
	host *hostcheck.Probes
	// installService installs care-clinic-service; nil means install.Install.
	installService func(install.Plan) error
	// layout finds the bundled service binary and engine; zero means
	// bundle.Default().
	layout *bundle.Layout
	// readyTimeout bounds waiting for the engine; zero means engineReadyTimeout.
	readyTimeout time.Duration
	// stateDir overrides stack.DefaultStateDir (tests).
	stateDir string
	// nameCheck overrides mdns.CheckAvailable (tests).
	nameCheck  func(string) error
	stopWatch  context.CancelFunc
	statusMu   sync.Mutex
	lastStatus *ServiceState

	osUninstall  bool
	removeTarget string
}

func NewApp(installFS fs.FS, log *applog.Logger) (*App, error) {
	proc.FixPath()
	env, err := fs.ReadFile(installFS, "install/"+release.EnvFile)
	if err != nil {
		return nil, fmt.Errorf("this build is missing its embedded %s: %w", release.EnvFile, err)
	}
	pins, err := release.Load(env)
	if err != nil {
		return nil, err
	}
	path, err := configPath()
	if err != nil {
		return nil, err
	}
	cfg, err := loadConfig(path)
	if err != nil {
		return nil, err
	}
	a := &App{installFS: installFS, pins: pins, log: log, cfg: cfg, configFile: path}
	if err := a.inferInstalledRole(); err != nil {
		return nil, err
	}
	if a.loadConfig().Role == roleServer {
		if err := a.saveConfig(a.loadConfig()); err != nil {
			return nil, err
		}
	}
	return a, nil
}

func (a *App) logln(msg string) {
	a.log.Write(msg)
	if a.ctx != nil {
		wruntime.EventsEmit(a.ctx, "care-log", msg)
	}
}

func (a *App) emit(event string, data ...any) {
	if a.ctx != nil {
		wruntime.EventsEmit(a.ctx, event, data...)
	}
}

// logError writes the error a bound method is about to hand back to the
// interface into the log file, naming the method, so a friendly message on
// screen always has a technical counterpart the operator can send to support.
// It is deferred with a pointer to the named return value:
//
//	func (a *App) Something() (err error) {
//		defer a.logError(&err)
//
// The job helpers do this for everything that runs through them, so only bound
// methods that do their own locking need the line - and nothing needs it twice.
func (a *App) logError(err *error) {
	if err != nil {
		*err = a.logged(*err)
	}
}

// loggedError marks an error that is already in the log file, so a bound method
// and the helper it ran through do not each write the same line.
type loggedError struct{ error }

func (a *App) logged(err error) error {
	var already loggedError
	if err == nil || errors.As(err, &already) {
		return err
	}
	a.logln(boundMethod() + ": " + err.Error())
	return loggedError{err}
}

// boundMethod walks out to the exported App method the interface actually
// called, so the log names ConnectClient rather than the helper that happened
// to produce the error.
func boundMethod() string {
	pcs := make([]uintptr, 12)
	frames := runtime.CallersFrames(pcs[:runtime.Callers(2, pcs)])
	for {
		frame, more := frames.Next()
		if _, method, ok := strings.Cut(frame.Function, "(*App)."); ok {
			name, _, _ := strings.Cut(method, ".")
			if name != "" && name[0] >= 'A' && name[0] <= 'Z' {
				return name
			}
		}
		if !more {
			return "CARE Clinic"
		}
	}
}

// reportError puts a failure on the interface. It replaces the native error
// dialogs that used to interrupt whatever the operator was doing; the desktop
// decides how to show it. The caller has already written the line to the log.
func (a *App) reportError(label, detail string) {
	a.emit("care-error", label, detail)
}
