package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/bundle"
	"github.com/ohcnetwork/care_desktop/app/internal/service/api"
)

func serviceApp(t *testing.T) (*App, *fakeAPI) {
	t.Helper()
	a := roleApp(t)
	f := newFakeAPI(t)
	a.service = f
	a.layout = &bundle.Layout{MacOSDir: filepath.Join(t.TempDir(), "MacOS")}
	a.readyTimeout = 2 * time.Second
	if err := a.saveConfig(Config{Role: roleServer}); err != nil {
		t.Fatal(err)
	}
	return a, f
}

// shipEngine puts an engine bundle whose content is its version in the
// app's Resources.
func shipEngine(t *testing.T, a *App, version string) {
	t.Helper()
	dir := filepath.Join(a.layout.MacOSDir, "..", "Resources", "engine")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "care-engine-"+version+"-arm64.tar.gz"), []byte(version), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSummarize(t *testing.T) {
	running := func(fn func(*api.ServiceStatus)) ServiceState {
		st := api.ServiceStatus{
			Engine: api.EngineStatus{State: api.StateReady, Stack: &api.StackStatus{State: api.StackRunning}},
			MDNS:   &api.MDNSStatus{Enabled: true, Name: "care", Advertising: true},
		}
		if fn != nil {
			fn(&st)
		}
		return serviceState(true, &st, nil)
	}
	for name, tc := range map[string]struct {
		state   ServiceState
		level   string
		title   string
		running bool
		note    string
	}{
		"not installed": {serviceState(false, nil, errors.New("dial: no such file")), levelError, "isn't installed", false, ""},
		"unreachable":   {serviceState(true, nil, errors.New("permission denied")), levelError, "can't reach", false, ""},
		"running":       {running(nil), levelOK, "is running", true, ""},
		"engine stopped": {running(func(s *api.ServiceStatus) { s.Engine.State = api.StateStopped }),
			levelWarn, "is stopped", false, ""},
		"no engine": {running(func(s *api.ServiceStatus) { s.Engine.State = api.StateNoEngine }),
			levelError, "No engine", false, ""},
		"crashed": {running(func(s *api.ServiceStatus) { s.Engine.State, s.Engine.LastError = api.StateCrashed, "vm exited" }),
			levelError, "unexpectedly", false, ""},
		"stack failed": {running(func(s *api.ServiceStatus) { s.Engine.Stack = &api.StackStatus{State: api.StackFailed, Error: "boom"} }),
			levelError, "didn't start", false, ""},
		"stack stopped": {running(func(s *api.ServiceStatus) { s.Engine.Stack = &api.StackStatus{State: api.StackStopped} }),
			levelWarn, "is stopped", false, ""},
		"stack not installed": {running(func(s *api.ServiceStatus) { s.Engine.Stack = &api.StackStatus{State: api.StackNotInstalled} }),
			levelWarn, "isn't installed", false, ""},
		"starting": {running(func(s *api.ServiceStatus) { s.Engine.Stack = &api.StackStatus{State: api.StackStarting} }),
			levelBusy, "is starting", true, ""},
		"mdns conflict": {running(func(s *api.ServiceStatus) {
			s.MDNS.Conflict = &api.MDNSConflict{Host: "care.local", Address: "192.168.1.9"}
		}), levelWarn, "is running", true, "192.168.1.9"},
		"port closed": {running(func(s *api.ServiceStatus) {
			s.Forward = &api.ForwardStatus{Listeners: []api.ForwardListener{{Name: "https", Listen: ":443", Error: "address in use"}}}
		}), levelWarn, "is running", true, "address in use"},
	} {
		t.Run(name, func(t *testing.T) {
			s := tc.state.Summary
			if s.Level != tc.level || !strings.Contains(s.Title, tc.title) || s.Running != tc.running {
				t.Fatalf("summary = %+v, want level %s, title ~%q, running %v", s, tc.level, tc.title, tc.running)
			}
			if tc.note != "" && !strings.Contains(strings.Join(s.Notes, "\n"), tc.note) {
				t.Fatalf("notes %q lack %q", s.Notes, tc.note)
			}
		})
	}
	if d := running(nil).Summary.Detail; d != "https://care.local" {
		t.Fatalf("running detail = %q", d)
	}
}

func TestEnsureEngine(t *testing.T) {
	for name, tc := range map[string]struct {
		current, shipped string
		installed        bool
		err              string
	}{
		"no bundle, engine installed": {current: "0.1.1"},
		"no bundle, no engine":        {err: "no engine bundle"},
		"same version":                {current: "0.1.1", shipped: "0.1.1"},
		"older bundle":                {current: "0.2.0", shipped: "0.1.1"},
		"newer bundle":                {current: "0.1.1", shipped: "0.1.2", installed: true},
		"first install":               {shipped: "0.1.1", installed: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(bundle.EngineBundleEnv, "")
			a, f := serviceApp(t)
			f.set(func(s *api.ServiceStatus) { s.Slots.Current = tc.current })
			if tc.shipped != "" {
				shipEngine(t, a, tc.shipped)
			}
			err := a.ensureEngine(context.Background())
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := len(f.installed) == 1 && slices.Equal(f.activated, []string{tc.shipped}); got != tc.installed {
				t.Fatalf("installed %v activated %v, want install %v", f.installed, f.activated, tc.installed)
			}
			if !tc.installed && (len(f.installed) != 0 || len(f.activated) != 0) {
				t.Fatalf("kept engine was replaced: %v %v", f.installed, f.activated)
			}
		})
	}
}

func TestWaitEngineReadyStartsAStoppedEngineOnce(t *testing.T) {
	a, f := serviceApp(t)
	f.set(func(s *api.ServiceStatus) { s.Engine.State = api.StateStopped })
	f.onStart = func(s *api.ServiceStatus) { s.Engine.State = api.StateStarting }
	go func() {
		time.Sleep(100 * time.Millisecond)
		f.set(func(s *api.ServiceStatus) { s.Engine.State = api.StateReady })
	}()
	if err := a.waitEngineReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.starts != 1 {
		t.Fatalf("Start called %d times", f.starts)
	}
}

func TestWaitEngineReadyReportsTheEngineError(t *testing.T) {
	a, f := serviceApp(t)
	a.readyTimeout = 100 * time.Millisecond
	f.set(func(s *api.ServiceStatus) { s.Engine.State, s.Engine.LastError = api.StateCrashed, "vm exited" })
	err := a.waitEngineReady(context.Background())
	if err == nil || !strings.Contains(err.Error(), "vm exited") {
		t.Fatalf("err = %v", err)
	}
	f.set(func(s *api.ServiceStatus) { s.Engine.State = api.StateNoEngine })
	if err := a.waitEngineReady(context.Background()); err == nil || !strings.Contains(err.Error(), "no engine") {
		t.Fatalf("err = %v", err)
	}
}

func TestServiceStatusReportsAnUnreachableService(t *testing.T) {
	a, f := serviceApp(t)
	f.statusErr = errors.New("connect: no such file or directory")
	a.host = &hostProbesFake().Probes
	a.host.ServiceInstalled = func() bool { return false }
	s := a.ServiceStatus()
	if s.Reachable || s.Installed || s.Status != nil || !strings.Contains(s.Summary.Title, "isn't installed") {
		t.Fatalf("state = %+v", s)
	}
	f.statusErr = nil
	if s := a.ServiceStatus(); !s.Reachable || !s.Installed || !s.Summary.Running {
		t.Fatalf("state = %+v", s)
	}
}

func TestWatchServicePublishesUntilStopped(t *testing.T) {
	a, _ := serviceApp(t)
	if err := a.saveConfig(Config{Role: roleServer}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.watchService(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		a.statusMu.Lock()
		got := a.lastStatus
		a.statusMu.Unlock()
		if got != nil {
			if !got.Summary.Running {
				t.Fatalf("published %+v", got)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no status published")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
}

func TestPrepareEngineSetsTheClinicAddress(t *testing.T) {
	a, f := serviceApp(t)
	if err := a.prepareEngine(context.Background(), "north"); err != nil {
		t.Fatal(err)
	}
	u := f.Updates()
	if len(u) != 1 || u[0].MDNSName == nil || *u[0].MDNSName != "north" {
		t.Fatalf("config updates = %+v", u)
	}
}

func TestCheckNameFreeSkipsTheServicesOwnName(t *testing.T) {
	a, _ := serviceApp(t)
	var checked []string
	a.nameCheck = func(name string) error { checked = append(checked, name); return errors.New("taken") }
	if err := a.checkNameFree(context.Background(), "care"); err != nil {
		t.Fatalf("own name: %v", err)
	}
	if err := a.checkNameFree(context.Background(), "north"); err == nil {
		t.Fatal("a taken name was accepted")
	}
	if !slices.Equal(checked, []string{"north"}) {
		t.Fatalf("checked %v", checked)
	}
}
