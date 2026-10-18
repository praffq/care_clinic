package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/hostcheck"
	"github.com/ohcnetwork/care_desktop/app/internal/service/install"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/diskspace"
)

type probesFake struct{ hostcheck.Probes }

func hostProbesFake() *probesFake {
	return &probesFake{hostcheck.Probes{
		Virtualization: func() (hostcheck.Virtualization, error) {
			return hostcheck.Virtualization{Supported: true, Arm64: true}, nil
		},
		Memory:            func() (uint64, error) { return 16 << 30, nil },
		Disk:              func() (diskspace.Usage, error) { return diskspace.Usage{Free: 100 << 30, Total: 500 << 30}, nil },
		ServiceInstalled:  func() bool { return true },
		ServicePing:       func(context.Context) error { return nil },
		CanInstallService: true,
	}}
}

func TestHostChecksUseTheProbes(t *testing.T) {
	a, _ := serviceApp(t)
	p := hostProbesFake()
	a.host = &p.Probes
	if r := a.HostChecks(); !r.Ready {
		t.Fatalf("report = %+v", r)
	}
	p.ServiceInstalled = func() bool { return false }
	p.ServicePing = func(context.Context) error { return errors.New("no socket") }
	r := a.HostChecks()
	if r.Ready {
		t.Fatal("ready without the service")
	}
	var action bool
	for _, c := range r.Checks {
		action = action || c.Action == "install-service"
	}
	if !action {
		t.Fatalf("no install-service action: %+v", r.Checks)
	}
}

func TestInstallServiceUsesTheBundledBinary(t *testing.T) {
	a, _ := serviceApp(t)
	if err := os.MkdirAll(a.layout.MacOSDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(a.layout.MacOSDir, "care-clinic-service")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	var plans []install.Plan
	a.installService = func(p install.Plan) error { plans = append(plans, p); return nil }
	if err := a.InstallService(); err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 || plans[0].Binary != bin || plans[0].User == "" || len(plans[0].Plist) == 0 {
		t.Fatalf("plans = %+v", plans)
	}
	onboardingJobUnlocked(t, a)
}

func TestInstallServiceFailsWithoutTheBundledBinary(t *testing.T) {
	a, _ := serviceApp(t)
	t.Setenv("CARE_CLINIC_SERVICE_BINARY", "")
	called := false
	a.installService = func(install.Plan) error { called = true; return nil }
	err := a.InstallService()
	if err == nil || called || !strings.Contains(err.Error(), "care-clinic-service") {
		t.Fatalf("err = %v, installer called %v", err, called)
	}
	onboardingJobUnlocked(t, a)
}

func TestInstallServiceReportsTheInstallerError(t *testing.T) {
	a, _ := serviceApp(t)
	if err := os.MkdirAll(a.layout.MacOSDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.layout.MacOSDir, "care-clinic-service"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	a.installService = func(install.Plan) error { return errors.New("User canceled.") }
	if err := a.InstallService(); err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunSetupRejectsAnInstalledClinicSynchronously(t *testing.T) {
	a, _ := serviceApp(t)
	if err := a.saveConfig(Config{Role: roleServer, SetupDone: true}); err != nil {
		t.Fatal(err)
	}
	if err := a.RunSetup("care", settingsPassword); err == nil {
		t.Fatal("setup accepted an installed clinic")
	}
	if a.activeJob.Load() != nil && a.activeJob.Load() != "" {
		t.Fatalf("a worker started: %v", a.activeJob.Load())
	}
	onboardingJobUnlocked(t, a)
}

func TestRunSetupValidatesBeforeLocking(t *testing.T) {
	a, _ := serviceApp(t)
	if err := a.saveConfig(Config{Role: roleServer}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, pw string }{{"care", "weak"}, {"not a label!", settingsPassword}} {
		if err := a.RunSetup(tc.name, tc.pw); err == nil {
			t.Fatalf("RunSetup(%q, %q) accepted", tc.name, tc.pw)
		}
	}
	onboardingJobUnlocked(t, a)
}

func onboardingJobUnlocked(t *testing.T, a *App) {
	t.Helper()
	if !a.jobMu.TryLock() {
		t.Fatal("operation retained the job lock")
	}
	a.jobMu.Unlock()
}
