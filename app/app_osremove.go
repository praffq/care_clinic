package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/sys/appremoval"
	"github.com/ohcnetwork/care_desktop/app/internal/vm"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	uninstallFlag      = "--uninstall"
	uninstallCheckFlag = "--uninstall-check"

	otherInstanceWait = 10 * time.Second
)

const (
	exitRemovable = 0
	exitSetUp     = 1
	exitRunning   = 3
	exitOtherUser = 4
)

func removalPreflight() (int, bool) {
	if appremoval.OtherInstanceRunning(otherInstanceWait) {
		return exitRunning, true
	}
	if appremoval.ForeignSession() {
		return exitOtherUser, true
	}
	return 0, false
}

func (a *App) setUp() (bool, error) {
	cfg := a.loadConfig()
	if cfg.Role == "" {
		return false, nil
	}
	installed, err := a.installDirInUse()
	if err != nil {
		return true, err
	}
	wizard := Config{
		Role: cfg.Role, MDNSName: cfg.MDNSName,
		BackupCertificate: cfg.BackupCertificate, BackupRecoveryPath: cfg.BackupRecoveryPath,
		BackupRecoveryVerified: cfg.BackupRecoveryVerified, AdminRecoveryHashes: cfg.AdminRecoveryHashes,
		AdminRecoveryPath: cfg.AdminRecoveryPath,
	}
	return installed || cfg != wizard, nil
}

func (a *App) removalExitCode() int {
	if set, err := a.setUp(); err != nil || set {
		return exitSetUp
	}
	return exitRemovable
}

func (a *App) UninstallRequested() bool { return a.osUninstall }

func (a *App) ExitUninstall() {
	go a.quitWhenIdle()
}

func (a *App) CanRemoveApp() bool {
	if a.osUninstall {
		return false
	}
	_, err := appremoval.Target()
	return err == nil
}

func (a *App) RemoveApp() (err error) {
	defer a.logError(&err)
	if a.osUninstall {
		return errors.New("the uninstaller is already removing CARE Clinic")
	}
	a.jobMu.Lock()
	defer a.jobMu.Unlock()
	if a.closing {
		return errors.New("CARE Clinic is closing")
	}
	set, err := a.setUp()
	if err != nil {
		return err
	}
	if set {
		return errors.New("uninstall the clinic setup or disconnect this computer before removing the app")
	}
	target, err := appremoval.Target()
	if err != nil {
		return err
	}
	if err := stopVMBeforeRemoval(); err != nil {
		return fmt.Errorf("could not stop the CARE engine before removing the app: %w", err)
	}
	a.removeTarget = target
	go a.quitWhenIdle()
	return nil
}

func stopEngineForRemoval(ctx context.Context, supported bool, stop func(context.Context) error) error {
	if !supported {
		return nil
	}
	return stop(ctx)
}

func stopVMBeforeRemoval() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return stopEngineForRemoval(ctx, vm.Supported(), vm.Stop)
}

func (a *App) quitWhenIdle() {
	a.jobMu.Lock()
	a.closing = true
	a.jobMu.Unlock()
	wruntime.Quit(a.ctx)
}
