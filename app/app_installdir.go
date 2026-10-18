package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ohcnetwork/care_desktop/app/internal/clinic"
	"github.com/ohcnetwork/care_desktop/app/internal/stack"
)

func (a *App) installDir() string {
	base := filepath.Dir(a.configPath())
	if runtime.GOOS == "windows" {
		if home, err := os.UserHomeDir(); err == nil && filepath.IsAbs(home) {
			base = filepath.Join(home, appDirName)
		}
	}
	return filepath.Join(base, installSubdir)
}

const installSubdir = "install"

var installUserFiles = map[string]bool{"backend.env": true, "frontend.env": true}

const gitkeepPlaceholder = ".gitkeep"

// kitPackage is the Go file that embeds the kit for care-clinic-service; it
// is not part of the kit.
const kitPackage = "kit.go"

func (a *App) ensureInstallDir() (string, error) {
	dest := a.installDir()
	err := fs.WalkDir(a.installFS, "install", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(p, "install")
		rel = strings.TrimPrefix(rel, "/")
		if rel == "" || rel == gitkeepPlaceholder || rel == kitPackage {
			return nil
		}
		target := filepath.Join(dest, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if installUserFiles[rel] {
			if _, err := os.Stat(target); err == nil {
				return nil
			}
		}
		data, err := fs.ReadFile(a.installFS, p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if strings.HasSuffix(rel, ".sh") {
			mode = 0o755
		}
		return os.WriteFile(target, data, mode)
	})
	return dest, err
}

func (a *App) engine() *clinic.Clinic {
	cfg := a.loadConfig()
	images, err := devImages()
	if err != nil {
		a.logln("warning: " + err.Error())
	}
	return &clinic.Clinic{
		Stack:       &stack.Runtime{Service: a.svc(), StateDir: a.stateDir, Log: a.logln},
		StackImages: images,
		InstallDir:  a.installDir(),
		MDNSName:    strings.TrimSuffix(strings.TrimSpace(cfg.MDNSName), ".local"),
		Log:         a.logln,
		Confirm:     a.confirmDialog,
	}
}

// devImages reads the image tarballs from scripts/dev-images.sh named by
// CARE_CLINIC_DEV_IMAGES. Without them the clinic reuses the images the
// engine already has, or installs the newest compatible release from the
// update feed (clinic.Setup).
func devImages() ([]stack.Image, error) {
	dir := os.Getenv("CARE_CLINIC_DEV_IMAGES")
	if dir == "" {
		return nil, nil
	}
	return stack.DevImages(dir, nil)
}
