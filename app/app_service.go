package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/bundle"
	"github.com/ohcnetwork/care_desktop/app/internal/service/api"
	"github.com/ohcnetwork/care_desktop/app/internal/service/control"
	"github.com/ohcnetwork/care_desktop/app/internal/service/install"
	"github.com/ohcnetwork/care_desktop/app/internal/service/slots"
	"github.com/ohcnetwork/care_desktop/app/internal/stack"
	"github.com/ohcnetwork/care_desktop/app/internal/versions"
)

// serviceAPI is the part of care-clinic-service's control client the app
// uses; tests substitute a fake.
type serviceAPI interface {
	stack.Service
	Status(ctx context.Context) (api.ServiceStatus, error)
	Watch(ctx context.Context, fn func(api.ServiceStatus)) error
	Start(ctx context.Context) (api.EngineStatus, error)
	Slots(ctx context.Context) (api.SlotList, error)
	InstallEngine(ctx context.Context, tarball io.Reader, size int64, sha256 string) (slots.Slot, error)
	Activate(ctx context.Context, version string) (api.EngineStatus, error)
}

const (
	engineReadyTimeout = 5 * time.Minute
	serviceCallTimeout = 10 * time.Second
	watchRetry         = 3 * time.Second
)

func (a *App) svc() serviceAPI {
	if a.service != nil {
		return a.service
	}
	return control.NewClient(control.SystemSocket)
}

func (a *App) bundleLayout() bundle.Layout {
	if a.layout != nil {
		return *a.layout
	}
	return bundle.Default()
}

func (a *App) serviceInstalled() bool {
	if a.host != nil && a.host.ServiceInstalled != nil {
		return a.host.ServiceInstalled()
	}
	return install.Installed()
}

// ServiceState is what the control panel shows: the service's own status
// plus a summary of it.
type ServiceState struct {
	Installed bool               `json:"installed"`
	Reachable bool               `json:"reachable"`
	Error     string             `json:"error,omitempty"`
	Status    *api.ServiceStatus `json:"status,omitempty"`
	Summary   Summary            `json:"summary"`
}

// Summary levels.
const (
	levelOK    = "ok"
	levelBusy  = "busy"
	levelWarn  = "warn"
	levelError = "error"
)

// Summary is one line about the clinic for the panel header. Running says
// whether Stop (rather than Start) is the useful action.
type Summary struct {
	Level   string   `json:"level"`
	Title   string   `json:"title"`
	Detail  string   `json:"detail,omitempty"`
	Running bool     `json:"running"`
	Notes   []string `json:"notes,omitempty"`
}

func serviceState(installed bool, st *api.ServiceStatus, err error) ServiceState {
	s := ServiceState{Installed: installed, Reachable: err == nil && st != nil, Status: st}
	if err != nil {
		s.Error = err.Error()
	}
	s.Summary = summarize(s)
	return s
}

func summarize(s ServiceState) Summary {
	if !s.Reachable {
		if !s.Installed {
			return Summary{Level: levelError, Title: "The CARE Clinic service isn't installed",
				Detail: "It runs the clinic in the background. Install it from setup."}
		}
		return Summary{Level: levelError, Title: "CARE Clinic can't reach its service",
			Detail: strings.TrimSpace(s.Error + ". Only members of the care-clinic group can use it; " +
				"if this Mac account isn't one, reinstall the service from setup while logged in as it.")}
	}
	st := s.Status
	e := st.Engine
	var sum Summary
	switch e.State {
	case api.StateNoEngine:
		sum = Summary{Level: levelError, Title: "No engine is installed", Detail: "Run setup again to install it."}
	case api.StateStarting:
		sum = Summary{Level: levelBusy, Title: "The engine is starting"}
	case api.StateStopping:
		sum = Summary{Level: levelBusy, Title: "The engine is stopping"}
	case api.StateStopped:
		sum = Summary{Level: levelWarn, Title: "The clinic is stopped", Detail: "The engine is off. Start it to open the clinic."}
	case api.StateCrashed, api.StateBackoff:
		sum = Summary{Level: levelError, Title: "The engine stopped unexpectedly", Detail: e.LastError}
		if !e.NextRetryAt.IsZero() {
			sum.Detail = strings.TrimSpace(sum.Detail + " The service retries at " + e.NextRetryAt.Local().Format("15:04:05") + ".")
		}
	default:
		sum = stackSummary(e.Stack, st.MDNS)
		if e.State == api.StateNotReady && sum.Level == levelOK {
			sum.Level = levelWarn
			sum.Notes = append(sum.Notes, "The engine reports it isn't fully ready.")
		}
	}
	if note, ok := lowDiskNote(e); ok {
		sum.Notes = append(sum.Notes, note)
		if sum.Level == levelOK {
			sum.Level = levelWarn
		}
	}
	if m := st.MDNS; m != nil && m.Conflict != nil {
		sum.Notes = append(sum.Notes, fmt.Sprintf("Another device (%s) also answers for %s.local; devices may reach it instead.", m.Conflict.Address, m.Name))
		if sum.Level == levelOK {
			sum.Level = levelWarn
		}
	}
	if f := st.Forward; f != nil {
		for _, l := range f.Listeners {
			if !l.Listening && l.Error != "" {
				sum.Notes = append(sum.Notes, fmt.Sprintf("Port %s isn't open: %s", l.Listen, l.Error))
				if sum.Level == levelOK {
					sum.Level = levelWarn
				}
			}
		}
	}
	return sum
}

func stackSummary(st *api.StackStatus, m *api.MDNSStatus) Summary {
	if st == nil {
		return Summary{Level: levelBusy, Title: "Checking the clinic"}
	}
	switch st.State {
	case api.StackRunning:
		s := Summary{Level: levelOK, Title: "The clinic is running", Running: true}
		if m != nil && m.Name != "" {
			s.Detail = "https://" + m.Name + ".local"
		}
		return s
	case api.StackStarting:
		return Summary{Level: levelBusy, Title: "The clinic is starting", Running: true}
	case api.StackStopping:
		return Summary{Level: levelBusy, Title: "The clinic is stopping"}
	case api.StackFailed:
		return Summary{Level: levelError, Title: "The clinic didn't start", Detail: st.Error}
	case api.StackNotInstalled:
		return Summary{Level: levelWarn, Title: "The clinic isn't installed in the engine", Detail: "Run setup again to install it."}
	}
	return Summary{Level: levelWarn, Title: "The clinic is stopped"}
}

// ServiceStatus reads the service's status once.
func (a *App) ServiceStatus() ServiceState {
	ctx, cancel := context.WithTimeout(a.clientContext(), serviceCallTimeout)
	defer cancel()
	st, err := a.svc().Status(ctx)
	if err != nil {
		return serviceState(a.serviceInstalled(), nil, err)
	}
	return serviceState(true, &st, nil)
}

// watchService emits service-status on every change until ctx ends,
// reconnecting while the service is unreachable.
func (a *App) watchService(ctx context.Context) {
	for ctx.Err() == nil {
		if a.loadConfig().Role != roleServer {
			// Clients have no service; the role can change while open.
			select {
			case <-ctx.Done():
				return
			case <-time.After(watchRetry):
			}
			continue
		}
		err := a.svc().Watch(ctx, func(st api.ServiceStatus) {
			a.publishStatus(serviceState(true, &st, nil))
		})
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			err = errors.New("the service closed the status stream")
		}
		a.publishStatus(serviceState(a.serviceInstalled(), nil, err))
		select {
		case <-ctx.Done():
			return
		case <-time.After(watchRetry):
		}
	}
}

func (a *App) publishStatus(s ServiceState) {
	a.statusMu.Lock()
	prev := a.lastStatus
	a.lastStatus = &s
	a.statusMu.Unlock()
	if prev == nil || prev.Summary.Title != s.Summary.Title || prev.Error != s.Error {
		a.log.Writef("service: %s %s", s.Summary.Title, s.Error)
	}
	a.emit("service-status", s)
}

func (a *App) startServiceWatch() {
	a.stopServiceWatch()
	ctx, cancel := context.WithCancel(a.clientContext())
	a.statusMu.Lock()
	a.stopWatch = cancel
	a.statusMu.Unlock()
	go a.watchService(ctx)
}

func (a *App) stopServiceWatch() {
	a.statusMu.Lock()
	stop := a.stopWatch
	a.stopWatch = nil
	a.statusMu.Unlock()
	if stop != nil {
		stop()
	}
}

// ensureEngine installs and activates the engine bundle shipped with the
// app, the same as "ctl install" and "ctl activate". An active engine of
// the same or a newer version is kept.
func (a *App) ensureEngine(ctx context.Context) error {
	s := a.svc()
	st, err := s.Status(ctx)
	if err != nil {
		return fmt.Errorf("couldn't reach the CARE Clinic service: %w", err)
	}
	current := st.Slots.Current
	eng, err := a.bundleLayout().Engine()
	switch {
	case errors.Is(err, bundle.ErrNoEngine) && current != "":
		a.logln("Using the installed engine " + current + ".")
		a.progress().Skip(stepEngine, "Version "+current+" is already installed")
		return nil
	case errors.Is(err, bundle.ErrNoEngine):
		return errors.New("this copy of CARE Clinic has no engine bundle and none is installed; " +
			"install one with 'care-clinic-service ctl install' or set " + bundle.EngineBundleEnv)
	case err != nil:
		return err
	}
	if current != "" && !newerEngine(eng.Version, current) {
		a.logln("Using the installed engine " + current + ".")
		a.progress().Skip(stepEngine, "Version "+current+" is already installed")
		return nil
	}
	f, err := os.Open(eng.Path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	a.logln("Installing engine " + eng.Version + "...")
	detail := "Version " + eng.Version
	meter := a.progress().Meter(stepEngine, detail, eng.Size)
	meter.Set(0)
	slot, err := s.InstallEngine(ctx, meter.Reader(f), eng.Size, eng.SHA256)
	if err != nil {
		return fmt.Errorf("install engine %s: %w", eng.Version, err)
	}
	a.logln("Starting engine " + slot.Version + "...")
	if _, err := s.Activate(ctx, slot.Version); err != nil {
		return fmt.Errorf("activate engine %s: %w", slot.Version, err)
	}
	a.progress().Finish(stepEngine, detail)
	return nil
}

// newerEngine reports whether candidate is newer than current; versions
// that don't parse are treated as different, so the bundle wins.
func newerEngine(candidate, current string) bool {
	if candidate == current {
		return false
	}
	c, err1 := versions.ParseSemver(candidate)
	cur, err2 := versions.ParseSemver(current)
	if err1 != nil || err2 != nil {
		return true
	}
	return c.Compare(cur) > 0
}

// waitEngineReady starts a stopped engine and waits until its agent is
// ready.
func (a *App) waitEngineReady(ctx context.Context) error {
	timeout := a.readyTimeout
	if timeout == 0 {
		timeout = engineReadyTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	s := a.svc()
	started := false
	var last api.EngineStatus
	for {
		st, err := s.Status(ctx)
		if err == nil {
			last = st.Engine
			switch st.Engine.State {
			case api.StateReady:
				return nil
			case api.StateNoEngine:
				return errors.New("no engine is installed")
			case api.StateStopped:
				if !started {
					a.logln("Starting the engine...")
					if _, err := s.Start(ctx); err != nil {
						return fmt.Errorf("start the engine: %w", err)
					}
					started = true
				}
			}
		}
		select {
		case <-ctx.Done():
			msg := "the engine didn't become ready in time"
			if last.State != "" {
				msg += " (" + string(last.State) + ")"
			}
			if last.LastError != "" {
				msg += ": " + last.LastError
			}
			return errors.New(msg)
		case <-time.After(pollInterval(timeout)):
		}
	}
}

func pollInterval(timeout time.Duration) time.Duration {
	return min(2*time.Second, max(timeout/50, 10*time.Millisecond))
}
