package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/ohcnetwork/care_desktop/app/internal/clinic"
	"github.com/ohcnetwork/care_desktop/app/internal/residue"
	"github.com/ohcnetwork/care_desktop/app/internal/service/api"
)

func (a *App) ScanResidue() (report residue.Report, err error) {
	err = a.withReadJob(func() error {
		if err := a.requireServer(); err != nil {
			return err
		}
		var scanErr error
		report, scanErr = a.scanResidue()
		return scanErr
	})
	return report, err
}

func (a *App) scanResidue() (residue.Report, error) {
	stateDir, err := a.clinicStateDir()
	if err != nil {
		return residue.Report{}, err
	}
	configPath := ""
	if cfg := a.loadConfig(); cfg.SetupDone || cfg.Removing || cfg.AdminPwHash != "" {
		configPath = a.configPath()
	}
	e := a.engine()
	return residue.Scan(residue.Options{
		Runner:     e.Runner(),
		InstallDir: e.InstallDir,
		ConfigPath: configPath,
		StateDir:   stateDir,
		Engine:     a.residueEngine,
	})
}

func (a *App) residueEngine() (residue.Engine, error) {
	ctx, cancel := context.WithTimeout(a.clientContext(), serviceCallTimeout)
	defer cancel()
	st, err := a.svc().Status(ctx)
	if err != nil {
		return residue.Engine{}, fmt.Errorf("the CARE Clinic service isn't answering: %w", err)
	}
	found := residue.Engine{Installed: true}
	switch st.Engine.State {
	case api.StateNoEngine:
		return residue.Engine{}, nil
	case api.StateStopped, api.StateCrashed, api.StateBackoff:
		return found, nil
	case api.StateReady:
	default:
		return found, fmt.Errorf("the CARE engine is %s; check again in a moment", st.Engine.State)
	}
	r := a.engine().Stack
	status, err := r.Status(ctx)
	if err != nil {
		return found, err
	}
	found.Reachable = true
	found.Services = len(status.Services)
	found.Configured, err = r.Configured(ctx)
	return found, err
}

func (a *App) PurgeResidue(confirmed bool) (after residue.Report, err error) {
	err = a.withServerJob(func() error {
		cfg := a.loadConfig()
		if cfg.SetupDone && !cfg.Removing {
			return errors.New("this computer already has a clinic set up - use Uninstall in the panel instead")
		}
		before, err := a.scanResidue()
		if err != nil {
			return err
		}
		after = before
		if before.Clean {
			return nil
		}
		if !confirmed {
			return errors.New("confirm the removal of the earlier CARE Clinic before it can be removed")
		}
		if err := a.beginRemoval(); err != nil {
			return err
		}
		a.logln("Removing the earlier CARE Clinic from this computer...")
		e := a.engine()
		opts := clinic.UninstallOptions{RemoveData: true, RemoveInstallDir: true, SkipStack: !hasTrace(before, "engine")}
		if !opts.SkipStack {
			if err := a.waitEngineReady(a.clientContext()); err != nil {
				return fmt.Errorf("couldn't start the CARE engine to remove the earlier clinic: %w", err)
			}
		}
		if err := e.Uninstall(opts); err != nil {
			return err
		}
		if err := residue.RemoveDocker(e.Runner(), a.logln); err != nil {
			return err
		}
		if err := residue.RemoveAutostart(); err != nil {
			return fmt.Errorf("couldn't remove the earlier start-at-login entry: %w", err)
		}
		stateDir, err := a.clinicStateDir()
		if err != nil {
			return err
		}
		if err := residue.RemoveSecrets(stateDir); err != nil {
			return err
		}
		a.setupAttempt = nil
		if err := a.forgetConfig(); err != nil {
			return err
		}
		after, err = a.scanResidue()
		if err != nil {
			return err
		}
		a.logPurge(after)
		if !after.Clean {
			return errors.New("cleanup is incomplete; remove the reported leftovers before setting up another clinic")
		}
		return nil
	})
	return after, err
}

func hasTrace(report residue.Report, id string) bool {
	for _, t := range report.Traces {
		if t.ID == id {
			return true
		}
	}
	return false
}

func (a *App) logPurge(after residue.Report) {
	if after.Clean {
		a.logln("Everything from the earlier CARE Clinic has been removed.")
		return
	}
	for _, t := range after.Traces {
		a.logln("Still here after cleanup: " + t.Label + " - " + t.Detail)
	}
}
