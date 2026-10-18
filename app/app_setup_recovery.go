package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/ohcnetwork/care_desktop/app/internal/sys/proc"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const recoverySheetLimit = 64 * 1024

type SetupRecoveryStatus struct {
	CodesSaved   bool   `json:"codes_saved"`
	CodesPath    string `json:"codes_path"`
	CodesProblem string `json:"codes_problem"`
}

func (a *App) GetSetupRecoveryStatus() (status SetupRecoveryStatus, err error) {
	defer a.logError(&err)
	if err := a.requireServer(); err != nil {
		return SetupRecoveryStatus{}, err
	}
	r, err := a.loadRecovery()
	if err != nil {
		return SetupRecoveryStatus{}, err
	}
	return setupRecoveryStatus(r), nil
}

func setupRecoveryStatus(r adminRecovery) SetupRecoveryStatus {
	status := SetupRecoveryStatus{CodesSaved: r.count() == recoveryCodeCount, CodesPath: r.Path}
	if status.CodesSaved {
		status.CodesProblem = adminCodesProblem(r)
	}
	return status
}

func adminCodesProblem(r adminRecovery) (problem string) {
	if r.Path == "" {
		return "missing"
	}
	info, err := os.Lstat(r.Path)
	if errors.Is(err, os.ErrNotExist) {
		return "missing"
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > recoverySheetLimit {
		return "unreadable"
	}
	file, err := os.Open(r.Path)
	if err != nil {
		return "unreadable"
	}
	defer func() {
		if err := file.Close(); err != nil {
			problem = "unreadable"
		}
	}()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return "unreadable"
	}
	data, err := io.ReadAll(io.LimitReader(file, recoverySheetLimit+1))
	if err != nil || len(data) > recoverySheetLimit {
		return "unreadable"
	}
	found := make(map[string]bool)
	for _, line := range strings.Split(string(data), "\n") {
		if code, ok := strings.CutPrefix(line, "[ ] "); ok {
			found[recoveryHash(strings.TrimSpace(code))] = true
		}
	}
	expected := make(map[string]bool)
	for _, hash := range r.Hashes {
		if hash == "" || expected[hash] || !found[hash] {
			return "mismatch"
		}
		expected[hash] = true
	}
	return ""
}

func (a *App) requireRecoverySetup() error {
	if err := a.requireServer(); err != nil {
		return err
	}
	cfg := a.loadConfig()
	if cfg.SetupDone || cfg.Removing || cfg.AdminPwHash != "" {
		return errors.New("recovery setup is locked after installation starts; retry installation with the recovery codes already saved")
	}
	return nil
}

func (a *App) SaveSetupRecoveryCodes() (string, error) {
	return a.saveSetupRecoveryCodesWith(func(opts wruntime.SaveDialogOptions) (string, error) {
		return wruntime.SaveFileDialog(a.ctx, opts)
	})
}

func (a *App) saveSetupRecoveryCodesWith(choose func(wruntime.SaveDialogOptions) (string, error)) (string, error) {
	var saved string
	err := a.withServerJob(func() error {
		if err := a.requireRecoverySetup(); err != nil {
			return err
		}
		filename, err := recoveryFilename(a.loadConfig().MDNSName, "clinic-admin-codes", "txt")
		if err != nil {
			return err
		}
		path, err := choose(wruntime.SaveDialogOptions{
			DefaultDirectory: recoverySaveDirectory(),
			Title:            "Save six CARE Clinic admin recovery codes",
			DefaultFilename:  filename,
		})
		if err != nil || path == "" {
			return err
		}
		if err := a.recoveryLocation(path); err != nil {
			return err
		}
		if err := a.saveAdminRecoveryCodes(path); err != nil {
			return err
		}
		saved = path
		return nil
	})
	return saved, err
}

func (a *App) setupRecoveryCodesPath() (string, error) {
	if err := a.requireRecoverySetup(); err != nil {
		return "", err
	}
	r, err := a.loadRecovery()
	if err != nil {
		return "", err
	}
	if r.count() != recoveryCodeCount || adminCodesProblem(r) != "" {
		return "", errors.New("the recovery codes file is unavailable; save a fresh set before continuing")
	}
	return r.Path, nil
}

func (a *App) OpenSetupRecoveryCodes() error {
	return a.withReadJob(func() error {
		path, err := a.setupRecoveryCodesPath()
		if err != nil {
			return err
		}
		return openDocument(path)
	})
}

func (a *App) OpenSetupRecoveryFolder() error {
	return a.withReadJob(func() error {
		if runtime.GOOS != "windows" {
			return errors.New("opening the recovery codes folder is available only on Windows")
		}
		path, err := a.setupRecoveryCodesPath()
		if err != nil {
			return err
		}
		cmd := proc.Command("explorer.exe", "/select,"+path)
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("couldn't open the recovery codes folder: %w", err)
		}
		go func() { _ = cmd.Wait() }()
		return nil
	})
}

func openDocument(path string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = proc.Command("open", path)
	case "windows":
		cmd = proc.Command("rundll32.exe", "url.dll,FileProtocolHandler", path)
	default:
		cmd = proc.Command("xdg-open", path)
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("couldn't open the saved document: %w", err)
	}
	return nil
}
