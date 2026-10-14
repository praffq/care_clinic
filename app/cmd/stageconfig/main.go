// Command stageconfig renders the .env embedded in the desktop app from the
// repository's versions.toml and deployments/.env. It prints JSON
// {"desktop_version": "...", "env": "..."} so build scripts never parse TOML.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ohcnetwork/care_desktop/app/internal/envconfig"
	"github.com/ohcnetwork/care_desktop/app/internal/release"
)

func main() {
	config := flag.String("config", "../"+release.ConfigFile, "path to versions.toml")
	env := flag.String("env", "../deployments/"+release.EnvFile, "path to the non-release deployments/.env")
	flag.Parse()
	if err := run(*config, *env); err != nil {
		fmt.Fprintln(os.Stderr, "stageconfig:", err)
		os.Exit(1)
	}
}

func run(configPath, envPath string) error {
	config, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	base, err := os.ReadFile(envPath)
	if err != nil {
		return err
	}
	staged, err := release.StageEnv(config, base)
	if err != nil {
		return err
	}
	pins, err := release.Load(staged)
	if err != nil {
		return err
	}
	defaults, err := envconfig.LoadDefaults(filepath.Join(filepath.Dir(envPath), "env-defaults.yml"))
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(struct {
		DesktopVersion string            `json:"desktop_version"`
		Env            string            `json:"env"`
		BuildEnv       map[string]string `json:"build_env"`
	}{pins.AppVersion, string(staged), defaults.FE})
}
