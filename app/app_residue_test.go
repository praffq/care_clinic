package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/residue"
	"github.com/ohcnetwork/care_desktop/app/internal/service/api"
)

func TestResidueEngineReadsTheService(t *testing.T) {
	a, f := serviceApp(t)
	for _, tc := range []struct {
		name  string
		state api.EngineState
		err   error
		want  residue.Engine
		fails bool
	}{
		{"fresh service", api.StateNoEngine, nil, residue.Engine{}, false},
		{"stopped engine", api.StateStopped, nil, residue.Engine{Installed: true}, false},
		{"crashed engine", api.StateCrashed, nil, residue.Engine{Installed: true}, false},
		{"booting engine", api.StateStarting, nil, residue.Engine{Installed: true}, true},
		{"ready engine with containers", api.StateReady, nil, residue.Engine{Installed: true, Reachable: true, Services: 1}, false},
		{"service down", api.StateReady, errors.New("dial: no such file"), residue.Engine{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.set(func(s *api.ServiceStatus) { s.Engine.State = tc.state })
			f.mu.Lock()
			f.statusErr = tc.err
			f.mu.Unlock()
			got, err := a.residueEngine()
			if (err != nil) != tc.fails || got != tc.want {
				t.Fatalf("engine = %+v, %v", got, err)
			}
		})
	}
}

func TestPurgeResidueLeavesAFinishedClinicToUninstall(t *testing.T) {
	a, _ := serviceApp(t)
	if err := a.saveConfig(Config{Role: roleServer, SetupDone: true, AdminPwHash: "hash"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.PurgeResidue(true); err == nil || !strings.Contains(err.Error(), "Uninstall") {
		t.Fatalf("a finished clinic was purged from setup: %v", err)
	}
	if cfg := a.loadConfig(); !cfg.SetupDone || cfg.Removing {
		t.Fatalf("the refused purge changed the settings: %+v", cfg)
	}
}

func TestResidueNeedsTheServerRole(t *testing.T) {
	a := roleApp(t)
	if err := a.selectRole(roleClient); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ScanResidue(); err == nil || !strings.Contains(err.Error(), "server") {
		t.Fatalf("a client scanned for clinic leftovers: %v", err)
	}
	if _, err := a.PurgeResidue(true); err == nil || !strings.Contains(err.Error(), "server") {
		t.Fatalf("a client purged clinic leftovers: %v", err)
	}
}
