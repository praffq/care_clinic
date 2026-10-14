package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/backup"
	"github.com/ohcnetwork/care_desktop/app/internal/backupcopy"
	"github.com/ohcnetwork/care_desktop/app/internal/health"
	"github.com/ohcnetwork/care_desktop/app/internal/storage"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

func (a *App) ListBackups() (list []backup.Backup, err error) {
	defer a.logError(&err)
	info, err := os.Stat(filepath.Join(a.installDir(), "docker-compose.yml"))
	if os.IsNotExist(err) {
		return []backup.Backup{}, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("the installed compose file is not a regular file")
	}
	return a.engine().Backups().ListBackups()
}

func (a *App) RestoreBackup(dbDump, filesArchive, recoveryFile, adminPassword string) error {
	return a.run(func() error {
		if err := a.requireAdmin(adminPassword); err != nil {
			return err
		}
		if err := a.requireStableClinic(); err != nil {
			return err
		}
		return a.engine().Backups().Restore(dbDump, filesArchive, recoveryFile)
	}, false, "restore")
}

func (a *App) GetBackupDir() string { return a.engine().BackupDirPath() }

// Export the password-protected local key, or enroll a surviving matching PEM.
// Neither operation creates a replacement encryption identity.
func (a *App) ExportBackupRecovery(adminPassword, recoveryFile string) (bool, error) {
	return a.exportBackupRecovery(adminPassword, recoveryFile, func(opts wruntime.SaveDialogOptions) (string, error) {
		return wruntime.SaveFileDialog(a.ctx, opts)
	})
}

func (a *App) exportBackupRecovery(adminPassword, recoveryFile string, choose func(wruntime.SaveDialogOptions) (string, error)) (bool, error) {
	saved := false
	err := a.withServerJob(func() error {
		if err := a.requireAdmin(adminPassword); err != nil {
			return err
		}
		if err := a.requireStableClinic(); err != nil {
			return err
		}
		cfg := a.loadConfig()
		source := strings.TrimSpace(recoveryFile)
		enroll := source != "" || cfg.BackupKeyEncrypted == "" || cfg.BackupKeyNeedsEnrollment
		var key []byte
		var err error
		if enroll {
			if source == "" {
				source = cfg.BackupRecoveryPath
			}
			key, err = backup.ReadRecoveryFile(source)
			if err != nil {
				return errors.New("password-only downloads are not enrolled and the existing backup recovery file is unavailable or unreadable; connect its drive or select a surviving PEM. Keys lost before enrollment cannot be reconstructed")
			}
		} else {
			key, err = decryptBackupKey(cfg, adminPassword)
			if err != nil {
				return err
			}
		}
		defer clear(key)
		if err := backup.VerifyRecoveryFile([]byte(cfg.BackupCertificate), key); err != nil {
			return fmt.Errorf("the selected recovery file does not match this clinic's configured backup key: %w", err)
		}
		if err := a.engine().Backups().VerifyInstalledRecoveryFile(key); err != nil {
			return fmt.Errorf("the installed backup encryption key could not be verified; no file was exported: %w", err)
		}
		directory, err := recoverySaveDirectory()
		if err != nil {
			return fmt.Errorf("couldn't locate your Desktop: %w", err)
		}
		filename, err := recoveryFilename(cfg.MDNSName, "backup-recovery", "pem")
		if err != nil {
			return err
		}
		path, err := choose(wruntime.SaveDialogOptions{
			DefaultDirectory: directory,
			Title:            "Save another copy of your backup recovery file",
			DefaultFilename:  filename,
		})
		if err != nil || path == "" {
			return err
		}
		if err := a.recoveryLocation(path, ""); err != nil {
			return err
		}
		if err := saveRecoveryFile(path, key); err != nil {
			return err
		}
		if enroll {
			cfg.BackupKeyEncrypted, err = encryptBackupKey(cfg, adminPassword, key)
			if err == nil {
				cfg.BackupKeyNeedsEnrollment = false
				err = a.saveConfig(cfg)
			}
			if err != nil {
				return fmt.Errorf("the PEM was exported, but its encrypted local copy could not be saved; keep the exported file and retry enrollment: %w", err)
			}
		}
		saved = true
		return nil
	})
	return saved, err
}

// GetBackupHealth reports the newest verified backup, how far changes are
// saved, the weekly trial restore, the optional copy and any alerts. Backups
// run nightly and only the newest verified set is kept; there is no
// retention setting.
func (a *App) GetBackupHealth() (h storage.BackupHealth, err error) {
	defer a.logError(&err)
	if err := a.requireSetup(); err != nil {
		return h, err
	}
	return a.backupHealth(a.engine().BackupDirPath(), filepath.Join(a.installDir(), backupStateDir),
		health.Ping().Active, time.Now()), nil
}

// SetBackupCopyDir chooses an optional second place (USB drive, second disk
// or network share) that follows the local backup. The folder must exist now;
// later it may be disconnected, and the copy catches up when it returns.
func (a *App) SetBackupCopyDir(dir string) (target string, err error) {
	defer a.logError(&err)
	dir = strings.TrimSpace(dir)
	if err := a.requireSetup(); err != nil {
		return "", err
	}
	if dir == "" || !filepath.IsAbs(dir) {
		return "", errors.New("choose an absolute folder for the backup copy")
	}
	dir = filepath.Clean(dir)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return "", errors.New("that folder is not available; connect the drive and choose it again")
	}
	local := a.engine().BackupDirPath()
	for _, protected := range []string{local, a.installDir(), filepath.Dir(a.configPath())} {
		if err := backup.CheckLocation(dir, protected); err != nil {
			return "", errors.New("the backup copy must be in a different place from the backups and CARE's own folders")
		}
		if err := backup.CheckLocation(protected, dir); err != nil {
			return "", errors.New("the backup copy must be in a different place from the backups and CARE's own folders")
		}
	}
	if recoveryPath := a.loadConfig().BackupRecoveryPath; recoveryPath != "" {
		if err := backup.CheckLocation(recoveryPath, dir); err != nil {
			return "", errors.New("keep the backup recovery file separate from the backup copy; choose another folder")
		}
	}
	err = a.withJob(func() error {
		cfg := a.loadConfig()
		cfg.BackupCopyDir = dir
		return a.saveConfig(cfg)
	})
	if err != nil {
		return "", err
	}
	a.logln("Backups will also be copied to " + filepath.Join(dir, backupcopy.Folder))
	a.startBackupCopy(local, filepath.Join(a.installDir(), backupStateDir), dir)
	return filepath.Join(dir, backupcopy.Folder), nil
}

// ClearBackupCopyDir stops copying. Files already copied are left in place.
func (a *App) ClearBackupCopyDir() (err error) {
	defer a.logError(&err)
	if err := a.requireSetup(); err != nil {
		return err
	}
	err = a.withJob(func() error {
		cfg := a.loadConfig()
		cfg.BackupCopyDir = ""
		return a.saveConfig(cfg)
	})
	if err == nil {
		if werr := storage.WriteCopyStatus(filepath.Join(a.installDir(), backupStateDir),
			storage.CopyStatus{State: "off", CheckedAt: time.Now().Unix()}); werr != nil {
			a.logln("ERROR: could not save the backup copy status: " + werr.Error())
		}
		a.logln("Backups are no longer copied to a second place; earlier copies were left as they are.")
	}
	return err
}

// RestorePointInTime rebuilds the database to target ("latest" or an RFC 3339
// time) from the verified full copy plus saved changes, in a staging
// database, and swaps it in only after replay and checks succeed.
func (a *App) RestorePointInTime(target, recoveryFile, adminPassword string) error {
	return a.run(func() error {
		if err := a.requireAdmin(adminPassword); err != nil {
			return err
		}
		if err := a.requireStableClinic(); err != nil {
			return err
		}
		return a.engine().Backups().RestorePointInTime(target, recoveryFile, time.Now())
	}, false, "restore")
}

func (a *App) SetBackupDir(dir string) (target string, err error) {
	defer a.logError(&err)
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return "", errors.New("choose a folder for the backups")
	}
	err = a.withJob(func() error {
		if err := a.requireStableClinic(); err != nil {
			return err
		}
		if problem := a.ValidateBackupDir(dir); problem != "" {
			return errors.New(problem)
		}
		target = filepath.Join(dir, "care-db-backups")
		previousConfig := a.loadConfig()
		previous := a.engine()
		running, err := previous.Runner().Lines("docker", "compose", "ps", "--services", "--filter", "status=running")
		if err != nil {
			return err
		}
		next := a.engine()
		next.BackupDir = target
		if err := next.Backups().CopyBackupCertificate(); err != nil {
			return err
		}
		cfg := previousConfig
		cfg.BackupDir = target
		if err := a.saveConfig(cfg); err != nil {
			return err
		}
		if slices.Contains(running, "backup") {
			if err := next.RestartBackupSidecar(); err != nil {
				if saveErr := a.saveConfig(previousConfig); saveErr != nil {
					return errors.Join(err, saveErr)
				}
				return errors.Join(err, previous.RestartBackupSidecar())
			}
		}
		a.logln("Backups will now go to " + target)
		if old := previous.BackupDirPath(); old != target {
			a.logln("Earlier backups were left in " + old + ". Keep your recovery file safe.")
		}
		return nil
	})
	return target, err
}

var importable = regexp.MustCompile(`^care-(?:manual-)?(\d{8}-\d{6})\.dump(?:\.enc)?$`)

type ImportedBackup struct {
	Path         string `json:"path"`
	Dir          string `json:"dir"`
	DBDump       string `json:"db_dump"`
	FilesArchive string `json:"files_archive"`
	Label        string `json:"label"`
	Encrypted    bool   `json:"encrypted"`
}

func (a *App) InspectBackupFile(path string) (out ImportedBackup, err error) {
	defer a.logError(&err)
	path = strings.TrimSpace(path)
	if path == "" {
		return out, errors.New("no file chosen")
	}
	name := filepath.Base(path)
	m := importable.FindStringSubmatch(name)
	if m == nil {
		return out, errors.New("that isn't a CARE database backup. Choose a file named like " +
			"care-20260101-020000.dump.enc - the one from the clinic's backup folder.")
	}
	info, statErr := os.Stat(path)
	if statErr != nil {
		return out, errors.New("couldn't open that file: " + statErr.Error())
	}
	if !info.Mode().IsRegular() {
		return out, errors.New("choose a regular backup file")
	}

	dir := filepath.Dir(path)
	out = ImportedBackup{
		Path:      path,
		Dir:       dir,
		DBDump:    name,
		Encrypted: strings.HasSuffix(name, ".enc"),
	}
	for _, candidate := range backup.FilesCandidates(name) {
		if info, err := os.Stat(filepath.Join(dir, candidate)); err == nil && info.Mode().IsRegular() {
			out.FilesArchive = candidate
			break
		} else if err != nil && !os.IsNotExist(err) {
			return out, err
		}
	}
	out.Encrypted = out.Encrypted || strings.HasSuffix(out.FilesArchive, ".enc")
	scope := "database only"
	if out.FilesArchive != "" {
		scope = "database + files"
	}
	out.Label = m[1] + " - " + scope
	if out.Encrypted {
		out.Label += " - encrypted"
	}
	return out, nil
}

func (a *App) RestoreFromFile(path, recoveryFile, adminPassword string) error {
	return a.run(func() error {
		if err := a.requireAdmin(adminPassword); err != nil {
			return err
		}
		if err := a.requireStableClinic(); err != nil {
			return err
		}
		found, err := a.InspectBackupFile(path)
		if err != nil {
			return err
		}
		return a.engine().Backups().RestoreFrom(found.Dir, found.DBDump, found.FilesArchive, recoveryFile)
	}, false, "restore")
}

func (a *App) ChooseBackupFile() (chosen string, err error) {
	defer a.logError(&err)
	opts := wruntime.OpenDialogOptions{
		Title:            "Choose a backup file",
		DefaultDirectory: a.engine().BackupDirPath(),
		Filters: []wruntime.FileFilter{
			{DisplayName: "CARE backups (care-*.dump, care-*.dump.enc)", Pattern: "*.dump;*.enc"},
			{DisplayName: "All files", Pattern: "*"},
		},
	}
	return wruntime.OpenFileDialog(a.ctx, opts)
}
