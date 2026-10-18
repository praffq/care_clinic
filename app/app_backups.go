package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
	"github.com/ohcnetwork/care_desktop/app/internal/service/api"
	"github.com/ohcnetwork/care_desktop/app/internal/service/backups"
	"github.com/ohcnetwork/care_desktop/app/internal/stack"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// Job labels (care-done) of the backup and storage actions.
const (
	restoreJob = "restore"
	pruneJob   = "prune-images"
)

// backupDialogs are the native dialogs the backup actions open; tests
// substitute them.
type backupDialogs struct {
	save func(wruntime.SaveDialogOptions) (string, error)
	dir  func(wruntime.OpenDialogOptions) (string, error)
	file func(wruntime.OpenDialogOptions) (string, error)
}

func (a *App) dialogs() backupDialogs {
	if a.backupDialogs != nil {
		return *a.backupDialogs
	}
	return backupDialogs{
		save: func(o wruntime.SaveDialogOptions) (string, error) { return wruntime.SaveFileDialog(a.ctx, o) },
		dir:  func(o wruntime.OpenDialogOptions) (string, error) { return wruntime.OpenDirectoryDialog(a.ctx, o) },
		file: func(o wruntime.OpenDialogOptions) (string, error) { return wruntime.OpenFileDialog(a.ctx, o) },
	}
}

func (a *App) recoveryKeyPath() (string, error) {
	dir, err := a.clinicStateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, stack.BackupRecoveryKey), nil
}

// ExportBackupRecoveryKey saves a copy of the clinic's backup recovery key
// where the operator chooses and returns the file; "" means the dialog was
// cancelled. Backups cannot be restored without it, on this computer or a
// new one.
func (a *App) ExportBackupRecoveryKey(adminPassword string) (string, error) {
	var saved string
	err := a.withServerJob(func() error {
		if err := a.requireAdmin(adminPassword); err != nil {
			return err
		}
		keyPath, err := a.recoveryKeyPath()
		if err != nil {
			return err
		}
		key, err := stack.ReadRecoveryKey(keyPath)
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("this computer has no backup recovery key: it is created with the clinic's first backup certificate, so finish setup first")
		}
		if err != nil {
			return err
		}
		filename, err := recoveryFilename(a.loadConfig().MDNSName, "backup-recovery-key", "pem")
		if err != nil {
			return err
		}
		path, err := a.dialogs().save(wruntime.SaveDialogOptions{
			DefaultDirectory: recoverySaveDirectory(),
			Title:            "Save the CARE Clinic backup recovery key",
			DefaultFilename:  filename,
		})
		if err != nil || path == "" {
			return err
		}
		if err := a.recoveryLocation(path); err != nil {
			return err
		}
		if err := saveRecoveryFile(path, key); err != nil {
			return err
		}
		a.logln("Saved a copy of the backup recovery key to " + path)
		saved = path
		return nil
	})
	return saved, err
}

// RestoreChoice is a backup set picked to restore and the recovery key to
// decrypt it with.
type RestoreChoice struct {
	SetDir    string    `json:"set_dir"`
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	// KeyPath is the chosen recovery key file; "" means this computer's.
	KeyPath string `json:"key_path,omitempty"`
}

// ChooseBackupToRestore asks for a backup folder (the host backup folder,
// the extra copy, either one copied from another computer, or one set's
// folder) and, when otherKey is set, the recovery key file that came with
// it. It changes nothing; nil means a dialog was cancelled.
func (a *App) ChooseBackupToRestore(otherKey bool) (choice *RestoreChoice, err error) {
	defer a.logError(&err)
	if err := a.requireSetup(); err != nil {
		return nil, err
	}
	d := a.dialogs()
	start := ""
	ctx, cancel := context.WithTimeout(a.clientContext(), serviceCallTimeout)
	defer cancel()
	if st, err := a.svc().Status(ctx); err == nil && st.Backups != nil {
		start = st.Backups.Dir
	}
	folder, err := d.dir(wruntime.OpenDialogOptions{
		Title: "Choose the backup folder to restore from", DefaultDirectory: start,
	})
	if err != nil || folder == "" {
		return nil, err
	}
	setDir, err := backups.FindSet(folder, "")
	if err != nil {
		return nil, err
	}
	m, err := backups.ReadManifest(setDir, filepath.Base(setDir))
	if err != nil {
		return nil, err
	}
	c := &RestoreChoice{SetDir: setDir, ID: filepath.Base(setDir), CreatedAt: m.CreatedAt}
	if otherKey {
		keyPath, err := d.file(wruntime.OpenDialogOptions{
			Title: "Choose the backup recovery key saved from the old computer",
			Filters: []wruntime.FileFilter{
				{DisplayName: "Recovery key (*.pem)", Pattern: "*.pem"},
			},
		})
		if err != nil || keyPath == "" {
			return nil, err
		}
		if _, err := stack.ReadRecoveryKey(keyPath); err != nil {
			return nil, err
		}
		c.KeyPath = keyPath
	}
	return c, nil
}

// RestoreBackup replaces the clinic's database and files with the set in
// setDir, decrypted with the recovery key at keyPath ("" for this
// computer's), as a background job (care-done "restore"). The clinic is
// down while it runs.
func (a *App) RestoreBackup(setDir, keyPath, adminPassword string) (err error) {
	defer a.logError(&err)
	if err := a.requireAdmin(adminPassword); err != nil {
		return err
	}
	if err := a.requireSetup(); err != nil {
		return err
	}
	if keyPath == "" {
		if keyPath, err = a.recoveryKeyPath(); err != nil {
			return err
		}
	}
	key, err := stack.ReadRecoveryKey(keyPath)
	if err != nil {
		return err
	}
	id := filepath.Base(setDir)
	if _, err := backups.ReadManifest(setDir, id); err != nil {
		return err
	}
	return a.run(func() error {
		a.logln("Restoring the backup " + id + "; the clinic is down until it finishes...")
		ctx := a.clientContext()
		restore := a.restoreSet
		if restore == nil {
			restore = func(ctx context.Context, dir string, key []byte) error {
				return backups.Restore(ctx, a.svc().Agent(), dir, key, a.logFrame)
			}
		}
		if err := restore(ctx, setDir, key); err != nil {
			return fmt.Errorf("the backup %s wasn't restored: %w", id, err)
		}
		a.logln("Restored the backup " + id + ".")
		return nil
	}, false, restoreJob)
}

func (a *App) logFrame(f protocol.Frame) {
	switch {
	case f.Log != nil:
		a.logln(f.Log.Line)
	case f.Progress != nil && f.Progress.Message != "":
		a.logln(f.Progress.Message)
	}
}

// SetBackupCopyDir asks for a folder on a USB drive or network share and
// makes it the extra backup copy; it returns the folder, or "" when the
// dialog was cancelled. The first copy starts straight away.
func (a *App) SetBackupCopyDir(adminPassword string) (string, error) {
	var chosen string
	err := a.withServerJob(func() error {
		if err := a.requireAdmin(adminPassword); err != nil {
			return err
		}
		dir, err := a.dialogs().dir(wruntime.OpenDialogOptions{
			Title: "Choose a folder on a USB drive or network share for an extra backup copy", CanCreateDirectories: true,
		})
		if err != nil || dir == "" {
			return err
		}
		if err := a.setBackupCopyDir(dir); err != nil {
			return err
		}
		a.logln("Backups are also copied to " + dir)
		chosen = dir
		return nil
	})
	return chosen, err
}

// ClearBackupCopyDir stops the extra backup copy; copies already made stay.
func (a *App) ClearBackupCopyDir(adminPassword string) error {
	return a.withServerJob(func() error {
		if err := a.requireAdmin(adminPassword); err != nil {
			return err
		}
		if err := a.setBackupCopyDir(""); err != nil {
			return err
		}
		a.logln("Backups are no longer copied to a second place")
		return nil
	})
}

func (a *App) setBackupCopyDir(dir string) error {
	ctx, cancel := context.WithTimeout(a.clientContext(), serviceCallTimeout)
	defer cancel()
	if _, err := a.svc().UpdateConfig(ctx, api.ServiceConfigUpdate{BackupCopyDir: &dir}); err != nil {
		return err
	}
	// The service starts the first copy as soon as its settings change.
	return nil
}

// CopyBackupsNow copies the newest backup to the extra copy folder now,
// e.g. right after its drive was plugged in, and reports the result.
func (a *App) CopyBackupsNow() (err error) {
	defer a.logError(&err)
	ctx, cancel := context.WithTimeout(a.clientContext(), 30*time.Minute)
	defer cancel()
	st, err := a.svc().CopyBackups(ctx)
	if err != nil {
		return err
	}
	if st.Copy != nil && st.Copy.LastError != "" {
		return errors.New(strings.TrimSpace(firstNonEmpty(st.Copy.Message, st.Copy.LastError)))
	}
	return nil
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

// PruneEngineImages removes container images the clinic no longer uses
// from the engine disk, as a background job (care-done "prune-images"). It
// keeps the running and previous versions' images.
func (a *App) PruneEngineImages() (err error) {
	defer a.logError(&err)
	if err := a.requireSetup(); err != nil {
		return err
	}
	return a.run(func() error {
		ctx := a.clientContext()
		prune := a.pruneImages
		if prune == nil {
			prune = func(ctx context.Context) (protocol.ImagePruneResult, error) {
				u, err := a.pluginCoordinator(ctx)
				if err != nil {
					return protocol.ImagePruneResult{}, err
				}
				return u.PruneImages(ctx)
			}
		}
		a.logln("Removing container images the clinic no longer uses...")
		res, err := prune(ctx)
		if err != nil {
			return err
		}
		a.logln(fmt.Sprintf("Removed %d unused images; %d are still in use.", res.Removed, res.Skipped))
		return nil
	}, false, pruneJob)
}
