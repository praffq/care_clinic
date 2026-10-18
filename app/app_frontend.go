package main

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"

	"github.com/ohcnetwork/care_desktop/app/internal/updates"
)

// settingsJob labels ApplySettings' background job (care-done "settings").
const settingsJob = "settings"

// ApplySettings applies the saved Advanced settings files ("backend",
// "frontend") as a background job. A frontend.env change rebuilds the CARE
// frontend with its REACT_* settings as one journaled operation (rolled back
// on failure; see docs/plugins.md), which restarts CARE with backend.env
// too; otherwise CARE is restarted.
func (a *App) ApplySettings(files []string, adminPassword string) (err error) {
	defer a.logError(&err)
	if err := a.requireAdmin(adminPassword); err != nil {
		return err
	}
	for _, f := range files {
		if f != "backend" && f != "frontend" {
			return errors.New("unknown env file: " + f)
		}
	}
	var settings map[string]string
	if slices.Contains(files, "frontend") {
		if settings, err = a.frontendSettings(); err != nil {
			return err
		}
	}
	return a.run(func() error {
		if err := a.requireSetup(); err != nil {
			return err
		}
		ctx := a.clientContext()
		if settings != nil {
			u, err := a.pluginCoordinator(ctx)
			if err != nil {
				return err
			}
			a.logln("Applying CARE's frontend settings...")
			changed, err := u.SetFrontendSettings(ctx, settings)
			if err != nil {
				return err
			}
			if changed {
				a.logln("Done.")
				return nil
			}
		}
		if err := a.waitEngineReady(ctx); err != nil {
			return err
		}
		e := a.engine()
		a.logln("Restarting CARE...")
		if err := e.Stop(); err != nil {
			return err
		}
		if err := e.Up(); err != nil {
			return err
		}
		a.logln("Done.")
		return nil
	}, false, settingsJob)
}

// frontendSettings reads the installed frontend.env's REACT_* settings.
func (a *App) frontendSettings() (map[string]string, error) {
	p, err := a.envPath("frontend")
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	settings, err := updates.ParseFrontendEnv(data)
	if err != nil {
		return nil, fmt.Errorf("frontend.env: %w", err)
	}
	// CARE Clinic's own wiring (hidden in the editor) keeps the release's.
	maps.DeleteFunc(settings, func(k, _ string) bool { return updates.ManagedFrontendKey(k) })
	if err := updates.CheckFrontendSettings(settings); err != nil {
		return nil, fmt.Errorf("frontend.env: %w", err)
	}
	return settings, nil
}
