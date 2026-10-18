package main

import "errors"

type UpdateChecks struct {
	App bool `json:"app"`
}

var errAppUpdateChecksOff = errors.New("CARE Clinic app update checks are turned off; an administrator can turn them back on in Advanced")

func (cfg Config) updateChecks() UpdateChecks {
	return UpdateChecks{App: !cfg.AppUpdateChecksOff}
}

func (a *App) appUpdateChecksOn() bool { return a.loadConfig().updateChecks().App }

func (a *App) GetUpdateChecks() UpdateChecks {
	return a.loadConfig().updateChecks()
}

func (a *App) SetUpdateChecks(app bool, adminPassword string) error {
	return a.withJob(func() error {
		if err := a.requireAdmin(adminPassword); err != nil {
			return err
		}
		cfg := a.loadConfig()
		cfg.AppUpdateChecksOff = !app
		if err := a.saveConfig(cfg); err != nil {
			return err
		}
		a.logln("CARE Clinic app update checks " + onOff(app) + ".")
		return nil
	})
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}
