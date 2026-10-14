package main

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/release"
	"github.com/ohcnetwork/care_desktop/app/internal/updates"
	"github.com/ohcnetwork/care_desktop/app/internal/vm"
)

// EngineState tells the panel whether the CARE engine matches this build of
// CARE Clinic and whether it is collecting guest and container logs.
type EngineState struct {
	Supported        bool   `json:"supported"`
	ExpectedVersion  string `json:"expected_version"`
	InstalledVersion string `json:"installed_version"`
	RunningVersion   string `json:"running_version"`
	Running          bool   `json:"running"`
	UpdateNeeded     bool   `json:"update_needed"`
	// UpdateFailed is set when the last update to ExpectedVersion was rolled
	// back (or is waiting to be), so the panel offers a retry instead of
	// updating again automatically. UpdateError says why.
	UpdateFailed bool   `json:"update_failed"`
	UpdateError  string `json:"update_error"`
	// Logs is "connecting", "collecting" or "interrupted" when running; an
	// empty value while running means an older helper that cannot report it.
	Logs string `json:"logs"`
	// Native is a Linux computer running the CARE agent as a system service
	// beside Docker rather than inside a CARE engine VM. Supported stays false
	// (there is no VM to stage, log or recover), but agent version changes are
	// still reported in UpdateNeeded and applied only by the update coordinator.
	Native bool `json:"native"`
}

// nativeAgentUpdater is the provisioner's coordinated native agent update on
// Linux: the pinned agent is downloaded and verified before beforeStop runs,
// and the previous agent is restored if clinicHealth fails.
type nativeAgentUpdater interface {
	UpdateNativeAgentWithHealth(version string, beforeStop, clinicHealth func() error) error
}

func nativeEngineState(installed, expected string, answering bool) EngineState {
	st := EngineState{Native: true, ExpectedVersion: expected,
		InstalledVersion: installed, Running: answering}
	if answering {
		st.RunningVersion = installed
	}
	// A missing agent is installed by setup; only a version change is an update.
	st.UpdateNeeded = installed != "" && expected != "" && installed != expected
	return st
}

func engineState(installed, expected string, toolsOK bool, s vm.HostStatus, u *vm.EngineUpdate) EngineState {
	st := EngineState{
		Supported: true, ExpectedVersion: expected, InstalledVersion: installed,
		Running: s.PID != 0, Logs: s.Logs,
	}
	if st.Running {
		st.RunningVersion = s.Version
	}
	if installed != "" && expected != "" {
		st.UpdateNeeded = installed != expected || !toolsOK ||
			(st.Running && st.RunningVersion != expected)
	}
	if u != nil && u.To == expected && (st.UpdateNeeded || u.Interrupted()) {
		st.UpdateNeeded, st.UpdateFailed, st.UpdateError = true, true, u.Error
		if u.Interrupted() {
			st.UpdateError = "the update didn't finish; the previous engine is restored before CARE starts"
		}
	}
	return st
}

func (a *App) EngineStatus() EngineState {
	if !vm.Supported() {
		if runtime.GOOS != "linux" {
			return EngineState{}
		}
		installed, expected, err := a.provisioner().NativeAgentVersions()
		if m, _, readErr := release.NewClient(a.installDir()).ReadVerified(); readErr == nil &&
			updates.Compatible(m.Engine.RequiresDesktop, a.pins.AppVersion) &&
			updates.Compatible(">="+a.pins.CareVMVersion, m.Engine.Version) {
			expected = m.Engine.Version
		}
		st := nativeEngineState(installed, expected, a.engineAnswers())
		if err != nil {
			st.UpdateNeeded, st.UpdateFailed, st.UpdateError = true, true, err.Error()
		}
		return st
	}
	installed, expected, toolsOK := a.provisioner().EngineVersions()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var pending *vm.EngineUpdate
	if u, ok, err := vm.PendingEngineUpdate(); err != nil {
		a.log.Writef("engine update record: %v", err)
	} else if ok {
		pending = &u
	}
	return engineState(installed, expected, toolsOK, vm.Status(ctx), pending)
}

// recoverEngineUpdate rolls back an engine update that the app was closed or
// the computer lost power during, before anything else uses the engine.
func (a *App) recoverEngineUpdate() {
	if !vm.Supported() && runtime.GOOS != "linux" {
		return
	}
	if runtime.GOOS != "linux" {
		if _, ok, _ := vm.PendingEngineUpdate(); !ok {
			return
		}
	}
	if err := a.withJob(a.provisioner().RecoverEngineUpdate); err != nil {
		a.logln("error: " + err.Error())
	}
}

// updateEngine runs inside a clinic job after staff choose Update. The new engine is
// downloaded, verified and unpacked first; only then are the containers
// removed (volumes kept) so they are recreated on the new engine with its
// logging settings. If the new engine does not start, the previous engine and
// tools are restored and the clinic is started again on them. The previous
// engine is retained until the clinic is healthy on the new engine.
func (a *App) updateEngine() error {
	if !vm.Supported() {
		if runtime.GOOS == "linux" {
			return a.updateNativeAgent()
		}
		return errors.New("this computer has no CARE engine to update")
	}
	p := a.provisioner()
	if err := p.RecoverEngineUpdate(); err != nil {
		return err
	}
	if err := vm.ClearFailedEngineUpdate(); err != nil {
		return err
	}
	st := a.EngineStatus()
	if !st.UpdateNeeded {
		a.logln("The CARE engine is already up to date.")
		return nil
	}
	a.logln("Updating the CARE engine to " + st.ExpectedVersion + ". The clinic is unavailable until this finishes; its data is kept.")
	e := a.engine()
	err := p.UpdateEngineWithHealth(func() error {
		if vm.Status(context.Background()).PID == 0 {
			return nil
		}
		return e.RemoveContainers()
	}, func() error {
		if err := e.RemoveContainers(); err != nil {
			return err
		}
		return e.Start()
	})
	if err != nil {
		a.logln("The CARE engine update didn't finish; starting the clinic on the previous engine...")
		if rerr := p.OpenDocker(); rerr != nil {
			return fmt.Errorf("%w (the previous engine also couldn't start: %v)", err, rerr)
		}
		if serr := e.Start(); serr != nil {
			return fmt.Errorf("%w (the clinic also couldn't start again: %v)", err, serr)
		}
		return err
	}
	return nil
}

// updateNativeAgent applies a pinned native agent version on Linux. It only
// ever runs inside the update coordinator; setup and repair refuse to change
// an installed agent's version.
func (a *App) updateNativeAgent() error {
	st := a.EngineStatus()
	return a.updateNativeAgentTo(st.ExpectedVersion)
}

func (a *App) updateNativeAgentTo(version string) error {
	st := a.EngineStatus()
	if !st.UpdateNeeded {
		if st.InstalledVersion != version {
			return fmt.Errorf("the approved engine version %s does not match the installed CARE agent %s", version, st.InstalledVersion)
		}
		a.logln("The CARE agent is already up to date.")
		return nil
	}
	updater, ok := any(a.provisioner()).(nativeAgentUpdater)
	if !ok {
		return fmt.Errorf("CARE agent %s is required but this build cannot update the installed agent %s", st.ExpectedVersion, st.InstalledVersion)
	}
	a.logln("Updating the CARE agent to " + version + ". The clinic is unavailable until this finishes; its data is kept.")
	e := a.engine()
	err := updater.UpdateNativeAgentWithHealth(version, func() error {
		if !st.Running {
			return nil
		}
		return e.RemoveContainers()
	}, func() error {
		if err := e.RemoveContainers(); err != nil {
			return err
		}
		return e.Start()
	})
	if err != nil {
		a.logln("The CARE agent update didn't finish; starting the clinic on the previous agent...")
		if serr := e.Start(); serr != nil {
			return fmt.Errorf("%w (the clinic also couldn't start again: %v)", err, serr)
		}
	}
	return err
}
