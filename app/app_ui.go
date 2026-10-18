package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/ohcnetwork/care_desktop/app/internal/sys/proc"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

func (a *App) OpenURL(url string) { wruntime.BrowserOpenURL(a.ctx, url) }

func affirmative(sel, yes string) bool { return sel == yes || sel == "Yes" }

func (a *App) askToProceed(title, message, yes string) (bool, error) {
	sel, err := wruntime.MessageDialog(a.ctx, wruntime.MessageDialogOptions{
		Type:          wruntime.QuestionDialog,
		Title:         title,
		Message:       message,
		Buttons:       []string{yes, "No"},
		DefaultButton: "No",
		CancelButton:  "No",
	})
	if err != nil {
		return false, err
	}
	return affirmative(sel, yes), nil
}

func (a *App) ChooseFolder(title string) (dir string, err error) {
	defer a.logError(&err)
	opts := wruntime.OpenDialogOptions{Title: title}
	if runtime.GOOS == "windows" {
		desktop, err := proc.DesktopDir()
		if err != nil {
			return "", fmt.Errorf("couldn't locate your Desktop: %w", err)
		}
		opts.DefaultDirectory = desktop
	} else if home, err := os.UserHomeDir(); err == nil {
		opts.DefaultDirectory = home
	}
	return wruntime.OpenDirectoryDialog(a.ctx, opts)
}

func (a *App) LogPath() string { return a.log.Path() }

func (a *App) OpenLogFolder() (err error) {
	defer a.logError(&err)
	path := a.log.Path()
	if path == "" {
		return errors.New("this run isn't writing a log file - the log folder couldn't be opened for writing")
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = proc.Command("open", "-R", path)
	case "windows":
		cmd = proc.Command("explorer.exe", "/select,"+path)
	default:
		cmd = proc.Command("xdg-open", filepath.Dir(path))
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("couldn't open the log folder: %w", err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
