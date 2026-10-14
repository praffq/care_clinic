package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/compose-spec/compose-go/v2/dotenv"
	"github.com/ohcnetwork/care_desktop/app/internal/envconfig"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/atomicfile"
)

func (a *App) envPath(name string) (string, error) {
	switch name {
	case "backend":
		return filepath.Join(a.installDir(), "backend.overrides.env"), nil
	case "frontend":
		return filepath.Join(a.installDir(), "frontend.overrides.env"), nil
	}
	return "", errors.New("unknown env file: " + name)
}

func (a *App) ReadEnv(name, adminPassword string) (string, error) {
	var content string
	err := a.withReadJob(func() error {
		if err := a.requireAdmin(adminPassword); err != nil {
			return err
		}
		if err := a.requireSetup(); err != nil {
			return err
		}
		p, err := a.envPath(name)
		if err != nil {
			return err
		}
		if _, err := envconfig.Overrides(a.installDir(), name); err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if os.IsNotExist(err) {
			return nil
		}
		if err == nil {
			content = string(b)
		}
		return err
	})
	return content, err
}

// ReadEnvDefaults exposes only deliberate release defaults, never effective
// configuration (which includes credentials and hostname-derived wiring).
func (a *App) ReadEnvDefaults(name, adminPassword string) (string, error) {
	var content string
	err := a.withReadJob(func() error {
		if err := a.requireAdmin(adminPassword); err != nil {
			return err
		}
		if err := a.requireSetup(); err != nil {
			return err
		}
		if _, err := a.envPath(name); err != nil {
			return err
		}
		defaults, err := envconfig.LoadDefaults(filepath.Join(a.installDir(), "env-defaults.yml"))
		if err != nil {
			return err
		}
		env := defaults.BE
		if name == "frontend" {
			env = defaults.FE
		}
		content = envconfig.Render(env)
		return nil
	})
	return content, err
}

func (a *App) WriteEnv(name, content, adminPassword string) error {
	return a.withJob(func() error {
		if err := a.requireAdmin(adminPassword); err != nil {
			return err
		}
		if err := a.requireStableClinic(); err != nil {
			return err
		}
		p, err := a.envPath(name)
		if err != nil {
			return err
		}
		nextEnv, err := dotenv.Parse(strings.NewReader(content))
		if err != nil {
			return errors.New("the environment file has invalid syntax; use KEY=value assignments and balanced quotes")
		}
		if err := envconfig.ValidateOverrides(name, nextEnv); err != nil {
			return err
		}
		if name == "backend" {
			currentEnv, err := envconfig.Overrides(a.installDir(), name)
			if err != nil {
				return fmt.Errorf("could not check the saved plugin configuration: %w", err)
			}
			currentPlugs, hadCurrentPlugs := currentEnv["ADDITIONAL_PLUGS"]
			nextPlugs, hasNextPlugs := nextEnv["ADDITIONAL_PLUGS"]
			if hadCurrentPlugs != hasNextPlugs || currentPlugs != nextPlugs {
				return errors.New("change backend plugins through the Plugins settings so CARE can stage and recover the change safely")
			}
		}
		if _, _, err := envconfig.Effective(a.installDir(), a.engine().Host()); err != nil {
			return fmt.Errorf("could not generate clinic configuration: %w", err)
		}
		previous, err := os.ReadFile(p)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := atomicfile.WritePrivate(p, []byte(content)); err != nil {
			return err
		}
		if err := envconfig.Generate(a.installDir(), a.engine().Host()); err != nil {
			restoreErr := atomicfile.WritePrivate(p, previous)
			if restoreErr == nil {
				restoreErr = envconfig.Generate(a.installDir(), a.engine().Host())
			}
			if restoreErr != nil {
				return fmt.Errorf("settings generation failed and previous configuration could not be fully restored: %w", errors.Join(err, restoreErr))
			}
			return fmt.Errorf("settings generation failed; previous overrides were restored: %w", err)
		}
		return nil
	})
}
