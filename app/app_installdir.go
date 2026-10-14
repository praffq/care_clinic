package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/ohcnetwork/care_desktop/app/internal/clinic"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/atomicfile"
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

const kitInventoryFile = ".kit-files.json"

type kitInventory struct {
	Version int      `json:"version"`
	Files   []string `json:"files"`
}

func validKitPath(rel string) bool {
	if !fs.ValidPath(rel) || rel == "." || strings.ContainsAny(rel, "\\:") {
		return false
	}
	for _, part := range strings.Split(rel, "/") {
		if strings.TrimRight(part, ". ") != part {
			return false
		}
	}
	return true
}

func protectedKitPath(rel string) bool {
	rel = strings.ToLower(rel)
	root := strings.Split(rel, "/")[0]
	switch root {
	case kitInventoryFile, backupStateDir, "keys", "plugins.json", "plugins-pending.json",
		"plugin-recovery.json", "restore-state.json", "care-images.json", "channel.lock":
		return true
	}
	return (rel != ".env" && strings.HasSuffix(rel, ".env")) ||
		strings.HasSuffix(rel, ".pem") || strings.HasSuffix(rel, ".key") || strings.HasSuffix(rel, ".enc")
}

// Reject links anywhere inside the installation before replacing or deleting.
// This is not a security boundary against a concurrent malicious OS-account user.
func kitTarget(dest, rel string) (string, error) {
	if !filepath.IsAbs(dest) || !validKitPath(rel) {
		return "", fmt.Errorf("invalid deployment-kit path %q", rel)
	}
	if info, err := os.Lstat(filepath.Dir(dest)); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("deployment-kit parent is a symbolic link: %s", filepath.Dir(dest))
	} else if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	target := dest
	for _, part := range append([]string{""}, strings.Split(rel, "/")...) {
		target = filepath.Join(target, part)
		info, err := os.Lstat(target)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("deployment-kit path is a symbolic link: %s", target)
		}
		if target != filepath.Join(dest, filepath.FromSlash(rel)) && !info.IsDir() {
			return "", fmt.Errorf("deployment-kit parent is not a directory: %s", target)
		}
	}
	return target, nil
}

func readKitInventory(dest string) ([]string, error) {
	path, err := kitTarget(dest, kitInventoryFile)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("deployment-kit inventory is not a regular file: %s", path)
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var inventory kitInventory
	if err := json.Unmarshal(data, &inventory); err != nil {
		return nil, fmt.Errorf("could not read deployment-kit inventory: %w", err)
	}
	if inventory.Version != 1 {
		return nil, fmt.Errorf("unsupported deployment-kit inventory version %d", inventory.Version)
	}
	for _, rel := range inventory.Files {
		if !validKitPath(rel) {
			return nil, fmt.Errorf("invalid deployment-kit inventory path %q", rel)
		}
	}
	return inventory.Files, nil
}

func writeKitInventory(dest string, files map[string]bool) error {
	inventory := kitInventory{Version: 1, Files: make([]string, 0, len(files))}
	for rel := range files {
		inventory.Files = append(inventory.Files, rel)
	}
	sort.Strings(inventory.Files)
	data, err := json.Marshal(inventory)
	if err != nil {
		return err
	}
	path, err := kitTarget(dest, kitInventoryFile)
	if err != nil {
		return err
	}
	return atomicfile.WritePrivate(path, data)
}

func (a *App) ensureInstallDir() (string, error) {
	dest := a.installDir()
	previous, err := readKitInventory(dest)
	if err != nil {
		return dest, err
	}
	current := map[string]bool{}
	// Validate the complete payload and obsolete targets before changing files.
	err = fs.WalkDir(a.installFS, "install", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(p, "install/")
		if p == "install" || filepath.Base(rel) == gitkeepPlaceholder {
			return nil
		}
		target, err := kitTarget(dest, rel)
		if err != nil {
			return err
		}
		if protectedKitPath(rel) && !installUserFiles[rel] {
			return fmt.Errorf("deployment kit contains reserved runtime path %q", rel)
		}
		if d.IsDir() {
			if info, err := os.Lstat(target); err == nil && !info.IsDir() {
				return fmt.Errorf("deployment-kit directory target is not a directory: %s", target)
			} else if err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		if !d.IsDir() {
			if !d.Type().IsRegular() {
				return fmt.Errorf("deployment-kit file is not regular: %s", p)
			}
			if info, err := os.Lstat(target); err == nil && !info.Mode().IsRegular() {
				return fmt.Errorf("deployment-kit target is not a regular file: %s", target)
			} else if err != nil && !os.IsNotExist(err) {
				return err
			}
			if !installUserFiles[rel] {
				current[rel] = true
			}
		}
		return nil
	})
	if err != nil {
		return dest, err
	}
	for _, rel := range previous {
		if current[rel] || protectedKitPath(rel) {
			continue
		}
		target, err := kitTarget(dest, rel)
		if err != nil {
			return dest, err
		}
		if info, err := os.Lstat(target); err == nil && !info.Mode().IsRegular() {
			return dest, fmt.Errorf("obsolete deployment-kit target is not a regular file: %s", target)
		} else if err != nil && !os.IsNotExist(err) {
			return dest, err
		}
	}
	err = fs.WalkDir(a.installFS, "install", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(p, "install/")
		if p == "install" || filepath.Base(rel) == gitkeepPlaceholder {
			return nil
		}
		target, err := kitTarget(dest, rel)
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if installUserFiles[rel] {
			if _, err := os.Lstat(target); err == nil {
				return nil
			} else if !os.IsNotExist(err) {
				return err
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
		if installUserFiles[rel] {
			return atomicfile.WritePrivate(target, data)
		}
		return atomicfile.Write(target, data, mode)
	})
	if err != nil {
		return dest, err
	}
	// Retain obsolete ownership until every removal succeeds. A crash or failed
	// deletion therefore leaves enough inventory for the next refresh to retry.
	pending := map[string]bool{}
	for rel := range current {
		pending[rel] = true
	}
	for _, rel := range previous {
		if !protectedKitPath(rel) {
			pending[rel] = true
		}
	}
	if err := writeKitInventory(dest, pending); err != nil {
		return dest, err
	}
	for rel := range pending {
		if current[rel] {
			continue
		}
		target, err := kitTarget(dest, rel)
		if err != nil {
			return dest, err
		}
		if info, err := os.Lstat(target); err == nil && !info.Mode().IsRegular() {
			return dest, fmt.Errorf("obsolete deployment-kit target is not a regular file: %s", target)
		} else if err != nil && !os.IsNotExist(err) {
			return dest, err
		}
		if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
			return dest, fmt.Errorf("could not remove obsolete deployment-kit file %s: %w", target, err)
		}
	}
	state, err := kitTarget(dest, backupStateDir)
	if err != nil {
		return dest, err
	}
	if err := os.MkdirAll(state, 0o755); err != nil {
		return dest, err
	}
	return dest, writeKitInventory(dest, current)
}

const backupStateDir = "backup-state" // storage.StateDirName

func (a *App) engine() *clinic.Clinic {
	cfg := a.loadConfig()
	return &clinic.Clinic{
		InstallDir:         a.installDir(),
		MDNSName:           strings.TrimSuffix(strings.TrimSpace(cfg.MDNSName), ".local"),
		BackupDir:          cfg.BackupDir,
		BackupCertificate:  cfg.BackupCertificate,
		BackupRecoveryPath: cfg.BackupRecoveryPath,
		Pins:               a.pins,
		Log:                a.logln,
		Confirm:            a.confirmDialog,
	}
}

func (a *App) engineForUpdate() *clinic.Clinic {
	e := a.engine()
	e.Abandon = func() bool { return !a.updatesAllowed() }
	return e
}
