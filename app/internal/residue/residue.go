package residue

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/ohcnetwork/care_desktop/app/internal/sys/hosts"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/netfix"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/proc"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/trust"
)

type Trace struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Detail string `json:"detail"`
}

type Report struct {
	Clean  bool    `json:"clean"`
	Traces []Trace `json:"traces"`
}

type Engine struct {
	Installed  bool
	Reachable  bool
	Services   int
	Configured bool
}

type Options struct {
	Runner     proc.Runner
	InstallDir string
	ConfigPath string
	StateDir   string
	Engine     func() (Engine, error)
}

const SecretsFile = "secrets.env"

var nonBlocking = map[string]bool{"images": true, "firewall": true}

func Scan(o Options) (Report, error) {
	return scan(o, systemTraces)
}

func scan(o Options, inspectSystem func(proc.Runner) ([]Trace, error)) (Report, error) {
	traces := []Trace{}
	var failed []error
	add := func(id, label, detail string) {
		traces = append(traces, Trace{ID: id, Label: label, Detail: detail})
	}

	if o.Engine != nil {
		found, err := engineTraces(o.Engine)
		traces = append(traces, found...)
		if err != nil {
			failed = append(failed, err)
		}
	}

	docker, err := dockerTraces(o.Runner)
	traces = append(traces, docker...)
	if err != nil {
		failed = append(failed, err)
	}

	if _, err := os.Stat(o.InstallDir); err == nil {
		add("install-dir", "Installed files", o.InstallDir)
	} else if !os.IsNotExist(err) {
		failed = append(failed, fmt.Errorf("could not inspect installed files: %w", err))
	}
	if o.StateDir != "" {
		secrets := filepath.Join(o.StateDir, SecretsFile)
		if _, err := os.Stat(secrets); err == nil {
			add("secrets", "Clinic passwords", "the database passwords of an earlier clinic, in "+secrets)
		} else if !os.IsNotExist(err) {
			failed = append(failed, fmt.Errorf("could not inspect the clinic passwords: %w", err))
		}
	}
	if o.ConfigPath != "" {
		if _, err := os.Stat(o.ConfigPath); err == nil {
			add("config", "Saved settings", o.ConfigPath)
		} else if !os.IsNotExist(err) {
			failed = append(failed, fmt.Errorf("could not inspect saved settings: %w", err))
		}
	}

	system, err := inspectSystem(o.Runner)
	traces = append(traces, system...)
	if err != nil {
		failed = append(failed, err)
	}

	blocking := 0
	for _, trace := range traces {
		if !nonBlocking[trace.ID] {
			blocking++
		}
	}
	return Report{Clean: blocking == 0 && len(failed) == 0, Traces: traces}, errors.Join(failed...)
}

func engineTraces(inspect func() (Engine, error)) ([]Trace, error) {
	e, err := inspect()
	if err != nil {
		return nil, fmt.Errorf("could not check the CARE engine for an earlier clinic: %w", err)
	}
	switch {
	case !e.Installed:
		return nil, nil
	case !e.Reachable:
		return []Trace{{ID: "engine", Label: "Clinic engine", Detail: "the engine from an earlier setup isn't running, so its clinic data couldn't be checked"}}, nil
	case e.Services > 0 || e.Configured:
		detail := "the database and files of an earlier clinic"
		if e.Services > 0 {
			detail = plural(e.Services, "service", "services") + " and the data of an earlier clinic"
		}
		return []Trace{{ID: "engine", Label: "Old clinic data", Detail: detail}}, nil
	}
	return nil, nil
}

func systemTraces(run proc.Runner) ([]Trace, error) {
	var traces []Trace
	var failed []error
	add := func(id, label, detail string) {
		traces = append(traces, Trace{ID: id, Label: label, Detail: detail})
	}
	hostsEntry, err := hosts.Inspect()
	if err != nil {
		failed = append(failed, err)
	} else if hostsEntry {
		add("hosts", "Hosts file entry", "this computer still resolves the old clinic address to itself")
	}
	trusted, err := trust.Inspect()
	if err != nil {
		failed = append(failed, err)
	} else if trusted {
		add("certificate", "Security certificate", "this computer still trusts the old clinic's certificate")
	}
	firewall, err := netfix.InspectRules(run)
	if err != nil {
		failed = append(failed, err)
	} else if firewall {
		add("firewall", "Firewall rules", "the clinic's inbound rules are still in place")
	}
	if AutostartEnabled() {
		add("autostart", "Start at login", "an earlier CARE Clinic is set to open when this computer starts")
	}
	return traces, errors.Join(failed...)
}

func plural(n int, one, many string) string {
	word := many
	if n == 1 {
		word = one
	}
	return strconv.Itoa(n) + " " + word
}
