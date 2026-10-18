package main

import (
	"errors"
	"fmt"
	"runtime/debug"
)

func (a *App) run(fn func() error, markSetup bool, label string) error {
	if err := a.requireServer(); err != nil {
		return a.logged(err)
	}
	return a.runJob(fn, markSetup, label)
}

func (a *App) runJob(fn func() error, markSetup bool, label string) error {
	if err := a.lockJob(); err != nil {
		return a.logged(err)
	}
	return a.runLockedJob(fn, markSetup, label)
}

// The caller owns jobMu; ownership passes to the worker.
func (a *App) runLockedJob(fn func() error, markSetup bool, label string) error {
	a.activeJob.Store(label)
	go func() {
		defer func() {
			a.activeJob.Store("")
			a.jobMu.Unlock()
		}()
		code := 0
		defer func() {
			if r := recover(); r != nil {
				code = 1
				a.log.Writef("PANIC in %s: %v\n%s", label, r, debug.Stack())
				detail := fmt.Sprintf("CARE hit an internal error during %s: %v", label, r)
				a.logln("error: " + detail)
				if !markSetup {
					a.reportError(failureTitle(label), detail)
				} else {
					a.setupAttempt = nil
					a.emit("setup-failed", SetupFailure{})
				}
			}
			a.emit("care-done", code, label)
		}()
		err := fn()
		if markSetup && err == nil {
			cfg := a.loadConfig()
			cfg.SetupDone = true
			err = a.saveConfig(cfg)
			if err == nil {
				a.setupAttempt = nil
				a.emit("setup-done", true)
			}
		}
		if err != nil {
			a.logln("error: " + err.Error())
			code = 1
			if !markSetup {
				a.reportError(failureTitle(label), err.Error())
			} else {
				a.emit("setup-failed", a.setupFailure(err))
			}
		}
	}()
	return nil
}

func (a *App) lockJob() error {
	if !a.jobMu.TryLock() {
		return errors.New("something else is still running - wait for it to finish")
	}
	if a.closing {
		a.jobMu.Unlock()
		return errors.New("CARE Clinic is closing")
	}
	return nil
}

// withJob and withReadJob log whatever they return. Every bound method that
// runs through them therefore leaves its failure in the log file, named, with
// no line of its own to forget.
func (a *App) withJob(fn func() error) error {
	if err := a.lockJob(); err != nil {
		return a.logged(err)
	}
	defer a.jobMu.Unlock()
	return a.logged(fn())
}

func (a *App) withServerJob(fn func() error) error {
	return a.withJob(func() error {
		if err := a.requireServer(); err != nil {
			return err
		}
		return fn()
	})
}

func (a *App) withLabeledJob(label string, fn func() error) error {
	return a.withServerJob(func() error {
		a.activeJob.Store(label)
		defer a.activeJob.Store("")
		return fn()
	})
}

func (a *App) withReadJob(fn func() error) error {
	if !a.jobMu.TryRLock() {
		return a.logged(errors.New("something else is still running - wait for it to finish"))
	}
	defer a.jobMu.RUnlock()
	if a.closing {
		return a.logged(errors.New("CARE Clinic is closing"))
	}
	return a.logged(fn())
}

func (a *App) requireSetup() error {
	if err := a.requireServer(); err != nil {
		return err
	}
	cfg := a.loadConfig()
	if cfg.Removing {
		return errors.New("cleanup is incomplete; finish removing this installation before starting or changing it")
	}
	if !cfg.SetupDone {
		return errors.New("not set up yet - run the first-time setup")
	}
	return nil
}

func (a *App) beginRemoval() error {
	if err := a.requireServer(); err != nil {
		return err
	}
	cfg := a.loadConfig()
	cfg.Removing = true
	if err := a.saveConfig(cfg); err != nil {
		return err
	}
	a.setupAttempt = nil
	return nil
}

func (a *App) requireAdmin(password string) error {
	if err := a.requireServer(); err != nil {
		return err
	}
	if !a.loadConfig().SetupDone || !a.VerifyAdminPassword(password) {
		return errors.New("the CARE Clinic admin password does not match this installation")
	}
	return nil
}

// failureTitle names the failure in the words the operator used to press,
// so a care-error event can be shown without the desktop re-deriving it.
func failureTitle(label string) string {
	switch label {
	case "start":
		return "CARE couldn't start"
	case "restart":
		return "CARE couldn't restart"
	case "stop":
		return "CARE couldn't stop"
	case "uninstall":
		return "CARE couldn't be removed"
	case "app-update":
		return "The CARE Clinic update didn't finish"
	case jobService:
		return "The CARE Clinic service couldn't be installed"
	}
	return "CARE couldn't finish that"
}

// ClinicAction starts, stops or restarts the clinic. Start boots the
// engine if it is off; stop and restart act on the CARE stack only. The
// service brings the stack up again after the computer restarts.
func (a *App) ClinicAction(action string) (err error) {
	defer a.logError(&err)
	switch action {
	case "start", "stop", "restart":
	default:
		return errors.New("action not allowed: " + action)
	}
	return a.run(func() error {
		if err := a.requireSetup(); err != nil {
			return err
		}
		ctx := a.clientContext()
		e := a.engine()
		switch action {
		case "start":
			if err := a.waitEngineReady(ctx); err != nil {
				return err
			}
			a.logln("Starting CARE...")
			if err := e.Up(); err != nil {
				return err
			}
		case "stop":
			a.logln("Stopping CARE...")
			if err := e.Stop(); err != nil {
				return err
			}
		case "restart":
			if err := a.waitEngineReady(ctx); err != nil {
				return err
			}
			a.logln("Restarting CARE...")
			if err := e.Stop(); err != nil {
				return err
			}
			if err := e.Up(); err != nil {
				return err
			}
		}
		a.logln("Done.")
		return nil
	}, false, action)
}

// ClinicStatus lists the CARE services in the engine, one "name state" per
// line.
func (a *App) ClinicStatus() (status string, err error) {
	defer a.logError(&err)
	if err := a.requireSetup(); err != nil {
		return "", err
	}
	return a.engine().Status()
}
