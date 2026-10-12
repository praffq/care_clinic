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

	"github.com/ohcnetwork/care_desktop/app/internal/release"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/applog"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/mdns"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/proc"
	"github.com/ohcnetwork/care_desktop/app/internal/vm"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

type App struct {
	ctx       context.Context
	installFS fs.FS
	pins      *release.Pins
	log       *applog.Logger

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

	advMu          sync.Mutex
	adv            *mdns.Manager
	advError       error
	advStop        chan struct{}
	advWorkMu      sync.Mutex
	advWake        chan struct{}
	advCancel      context.CancelFunc
	advGeneration  uint64
	advWithdrawing bool
	advHelper      bool
	advPaused      atomic.Bool
	emitEvent      func(string, ...any)

	store storageWatch

	dockerCheckMu sync.Mutex
	dockerCheck   *dockerStatusCall

	osUninstall  bool
	removeTarget string
}

func NewApp(installFS fs.FS, log *applog.Logger) (*App, error) {
	proc.FixPath()
	useEngine()
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
		if _, err := a.engine().Backups().PendingRestore(); err != nil {
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
	if a.emitEvent != nil {
		a.emitEvent(event, data...)
		return
	}
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

// Failures are logged and conflicts are shown here; background callers retry.
func (a *App) startAdvertise() error {
	return a.startAdvertiseContext(context.Background())
}

func (a *App) startAdvertiseContext(parent context.Context) (result error) {
	a.advMu.Lock()
	defer a.advMu.Unlock()
	defer func() {
		if result != nil && parent.Err() == nil {
			a.reportAdvertiseError(result)
		}
	}()
	if parent.Err() != nil {
		return parent.Err()
	}
	select {
	case <-a.advStop:
		return nil
	default:
	}
	// Keep settings writers from being overwritten by an older watcher snapshot.
	a.cfgMu.RLock()
	cfg, err := normalizeRole(a.cfg)
	if err == nil && vm.Supported() && cfg.Role == roleServer {
		err = vm.SaveAdvertisement(vm.AdvertisementConfig{
			Enabled: cfg.SetupDone && !cfg.Removing && cfg.MDNSName != "",
			Name:    cfg.MDNSName, InstallDir: a.installDir(),
		})
	}
	a.cfgMu.RUnlock()
	if err != nil {
		a.adv.Stop()
		a.adv = nil
		return err
	}
	name := cfg.MDNSName
	if a.advPaused.Load() {
		return a.reportAdvertisementStatus(mdns.AdvertisementStatus{})
	}
	if vm.Supported() && cfg.Role == roleServer {
		ctx, cancel := context.WithTimeout(parent, 15*time.Second)
		defer cancel()
		s := vm.Status(ctx)
		if !s.Running {
			a.advHelper = false
			a.adv.Stop()
			a.adv = nil
			if err := vm.SetAdvertisementOwner(true); err != nil {
				return err
			}
			return a.reportAdvertisementStatus(mdns.AdvertisementStatus{})
		}
		if s.AdvertisementSupported {
			a.advHelper = true
			a.adv.Stop()
			a.adv = nil
			if err := vm.SetAdvertisementOwner(true); err != nil {
				return err
			}
			if err := vm.RefreshAdvertisement(ctx); err != nil {
				return err
			}
			s = vm.Status(ctx)
			if !s.Running || !s.AdvertisementSupported {
				err := errors.New("persistent clinic advertising could not be confirmed with the CARE engine helper")
				return err
			}
			if parent.Err() != nil {
				return parent.Err()
			}
			return a.reportAdvertisementStatus(s.Advertisement)
		}
		a.advHelper = false
		if err := vm.SetAdvertisementOwner(false); err != nil {
			return err
		}
		// A replacement helper may have started during the older-helper handoff.
		// Recheck after revoking ownership before opening a window responder.
		s = vm.Status(ctx)
		if !s.Running || s.AdvertisementSupported {
			a.adv.Stop()
			a.adv = nil
			err := errors.New("CARE engine helper changed during network advertising handoff; retrying on the next check")
			return err
		}
	}
	if cfg.Role != roleServer || name == "" || cfg.Removing || (!cfg.SetupDone && cfg.AdminPwHash == "") {
		a.adv.Stop()
		a.adv = nil
		return a.reportAdvertisementStatus(mdns.AdvertisementStatus{})
	}
	if a.adv == nil {
		a.adv = mdns.NewManager(a.logln)
	}
	s := a.adv.RefreshContext(parent, name)
	if parent.Err() != nil {
		a.adv.Stop()
		return parent.Err()
	}
	return a.reportAdvertisementStatus(s)
}

// Caller holds advMu; status from the helper carries conflict identity separately.
func (a *App) reportAdvertisementStatus(s mdns.AdvertisementStatus) error {
	if s.Error == "" {
		if a.advError != nil {
			a.emit("care-network-status", "", "")
		}
		a.advError = nil
		return nil
	}
	err := errors.New(s.Error)
	a.notifyAdvertisement(err, s.Conflict)
	return err
}

func (a *App) restartAdvertise() error {
	err := a.withdrawAdvertise()
	a.queueAdvertise()
	return err
}

func (a *App) withdrawAdvertise() error {
	a.advWorkMu.Lock()
	a.advGeneration++
	a.advWithdrawing = true
	if a.advCancel != nil {
		a.advCancel()
	}
	a.advWorkMu.Unlock()
	a.advMu.Lock()
	defer func() {
		a.advMu.Unlock()
		a.advWorkMu.Lock()
		a.advWithdrawing = false
		a.advWorkMu.Unlock()
	}()
	a.adv.Stop()
	a.adv = nil
	if vm.Supported() && (a.advHelper || a.loadConfig().Role == roleServer) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		status := vm.Status(ctx)
		if status.Running && !status.AdvertisementSupported {
			a.advHelper = false
			return nil
		}
		if !status.Running && !a.advHelper {
			return nil
		}
		if err := vm.WithdrawAdvertisement(ctx); err != nil {
			a.reportAdvertiseError(err)
			return err
		}
	}
	return nil
}

// Caller holds advMu. Report a continuing conflict once, not every watcher tick.
func (a *App) reportAdvertiseError(err error) {
	var conflict *mdns.ConflictError
	a.notifyAdvertisement(err, errors.As(err, &conflict))
}

func (a *App) notifyAdvertisement(err error, conflict bool) {
	if errors.Is(err, context.Canceled) {
		return
	}
	if a.advError != nil && a.advError.Error() == err.Error() {
		return
	}
	a.advError = err
	a.logln("mDNS: " + err.Error())
	title := "Clinic network advertising could not be verified"
	if conflict {
		title = failureTitle("network-name")
	}
	a.emit("care-network-status", title, err.Error())
}

func (a *App) advRunning() bool {
	a.advMu.Lock()
	defer a.advMu.Unlock()
	return a.adv != nil && a.adv.Status().Running
}

func (a *App) watchAdvertise() {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-a.advStop:
			return
		case <-t.C:
			a.queueAdvertise()
		case <-a.advWake:
			a.advWorkMu.Lock()
			if a.advWithdrawing {
				a.advWorkMu.Unlock()
				continue
			}
			ctx, cancel := context.WithCancel(context.Background())
			a.advCancel = cancel
			generation := a.advGeneration
			a.advWorkMu.Unlock()
			_ = a.startAdvertiseContext(ctx)
			cancel()
			a.advWorkMu.Lock()
			if a.advGeneration == generation {
				a.advCancel = nil
			}
			a.advWorkMu.Unlock()
		}
	}
}

func (a *App) queueAdvertise() {
	a.advWorkMu.Lock()
	defer a.advWorkMu.Unlock()
	a.advGeneration++
	if a.advCancel != nil {
		a.advCancel()
	}
	if a.advWake == nil {
		a.advWake = make(chan struct{}, 1)
	}
	select {
	case a.advWake <- struct{}{}:
	default:
	}
}
