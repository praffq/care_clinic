package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/compose-spec/compose-go/v2/dotenv"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/atomicfile"
)

func (a *App) envPath(name string) (string, error) {
	switch name {
	case "backend":
		return filepath.Join(a.installDir(), "backend.env"), nil
	case "frontend":
		return filepath.Join(a.installDir(), "frontend.env"), nil
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
		b, err := os.ReadFile(p)
		content = string(b)
		return err
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
		if name == "backend" {
			current, err := os.ReadFile(p)
			if err != nil {
				return fmt.Errorf("could not check the saved plugin configuration: %w", err)
			}
			currentEnv, err := dotenv.Parse(strings.NewReader(string(current)))
			if err != nil {
				return fmt.Errorf("could not check the saved plugin configuration: %w", err)
			}
			currentPlugs, hadCurrentPlugs := currentEnv["ADDITIONAL_PLUGS"]
			nextPlugs, hasNextPlugs := nextEnv["ADDITIONAL_PLUGS"]
			if hadCurrentPlugs != hasNextPlugs || currentPlugs != nextPlugs {
				return errors.New("change backend plugins through the Plugins settings so CARE can stage and recover the change safely")
			}
		}
		return atomicfile.Write(p, []byte(content), 0o600)
	})
}
