package main

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/service/api"
	"github.com/ohcnetwork/care_desktop/app/internal/service/backups"
	"github.com/ohcnetwork/care_desktop/app/internal/stack"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/atomicfile"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/diskspace"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/proc"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const SetupBackupFile = "setup-backup.json"

const setupBackupNeed uint64 = 1 << 30

var generateBackupKey = stack.GenerateBackupKey

type setupBackup struct {
	Certificate string `json:"certificate"`
	RecoveryKey string `json:"recovery_key"`
	Path        string `json:"path,omitempty"`
	Verified    bool   `json:"verified,omitempty"`
}

type SetupBackupStatus struct {
	Saved    bool   `json:"backup_saved"`
	Verified bool   `json:"backup_verified"`
	Path     string `json:"backup_path"`
	Problem  string `json:"backup_problem"`
}

type BackupSpace struct {
	Dir      string `json:"dir"`
	Free     uint64 `json:"free"`
	Total    uint64 `json:"total"`
	Need     uint64 `json:"need"`
	DaysLeft int    `json:"days_left"`
}

func (a *App) setupBackupPath() (string, error) {
	dir, err := a.clinicStateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, SetupBackupFile), nil
}

func (a *App) loadSetupBackup() (setupBackup, error) {
	var b setupBackup
	path, err := a.setupBackupPath()
	if err != nil {
		return b, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return b, nil
	}
	if err != nil {
		return b, err
	}
	if err := json.Unmarshal(data, &b); err != nil {
		return setupBackup{}, fmt.Errorf("%s is damaged; save a new recovery file: %w", path, err)
	}
	return b, nil
}

func (a *App) saveSetupBackup(b setupBackup) error {
	path, err := a.setupBackupPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WritePrivate(path, append(data, '\n'))
}

func (a *App) removeSetupBackup() error {
	path, err := a.setupBackupPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func matchRecoveryKey(cert, key []byte) error {
	c, err := stack.ParseBackupCert(cert)
	if err != nil {
		return err
	}
	block, rest := pem.Decode(key)
	if block == nil || block.Type != "RSA PRIVATE KEY" || len(bytes.TrimSpace(rest)) != 0 {
		return errors.New("that file isn't a CARE backup recovery file")
	}
	priv, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return fmt.Errorf("that file isn't a CARE backup recovery file: %w", err)
	}
	pub, ok := c.PublicKey.(*rsa.PublicKey)
	if !ok || !pub.Equal(&priv.PublicKey) {
		return errors.New("that recovery file doesn't match this clinic; select the file saved for this setup")
	}
	return nil
}

func (a *App) setupBackupStatus(b setupBackup) SetupBackupStatus {
	s := SetupBackupStatus{Saved: b.Certificate != "", Verified: b.Verified, Path: b.Path}
	if !s.Saved {
		s.Verified = false
		return s
	}
	data, err := stack.ReadRecoveryKey(b.Path)
	switch {
	case b.Path == "" || errors.Is(err, os.ErrNotExist):
		s.Problem = "missing"
	case err != nil:
		s.Problem = "unreadable"
	case matchRecoveryKey([]byte(b.Certificate), data) != nil:
		s.Problem = "mismatch"
	}
	if s.Problem != "" {
		s.Verified = false
	}
	return s
}

func (a *App) GetSetupBackupStatus() (status SetupBackupStatus, err error) {
	defer a.logError(&err)
	if err := a.requireServer(); err != nil {
		return status, err
	}
	b, err := a.loadSetupBackup()
	if err != nil {
		return status, err
	}
	return a.setupBackupStatus(b), nil
}

func (a *App) requireBackupSetup() error {
	if err := a.requireServer(); err != nil {
		return err
	}
	cfg := a.loadConfig()
	if cfg.SetupDone || cfg.Removing || cfg.AdminPwHash != "" {
		return errors.New("recovery setup is locked after installation starts; retry installation with the recovery file already saved")
	}
	return nil
}

func (a *App) serviceBackupDir() string {
	ctx, cancel := context.WithTimeout(a.clientContext(), serviceCallTimeout)
	defer cancel()
	cfg, err := a.svc().Config(ctx)
	if err != nil {
		return ""
	}
	return cfg.BackupDir
}

func (a *App) setupBackupDir(dir string) string {
	if dir = strings.TrimSpace(dir); dir != "" {
		return dir
	}
	return a.serviceBackupDir()
}

func (a *App) setupRecoveryLocation(path, backupDir string) error {
	if err := a.recoveryLocation(path); err != nil {
		return err
	}
	if dir := a.setupBackupDir(backupDir); dir != "" && insideDir(path, dir) {
		return fmt.Errorf("keep recovery files outside CARE's settings, installation, logs and backup folders (%s)", dir)
	}
	return nil
}

func macProtectedFolders() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{
		filepath.Join(home, "Desktop"), filepath.Join(home, "Documents"), filepath.Join(home, "Downloads"),
		filepath.Join(home, "Library", "Mobile Documents"), filepath.Join(home, "Library", "CloudStorage"),
	}
}

func (a *App) ValidateBackupDir(dir string) string {
	if err := a.requireServer(); err != nil {
		return err.Error()
	}
	return a.backupDirProblem(dir)
}

func (a *App) backupDirProblem(dir string) string {
	dir = strings.TrimSpace(dir)
	current := a.serviceBackupDir()
	managed := dir == "" || (current != "" && filepath.Clean(dir) == filepath.Clean(current))
	if dir == "" {
		dir = current
	}
	if dir == "" {
		return "Couldn't read the CARE Clinic service's backup folder. Check that the service is running, then check again."
	}
	if !filepath.IsAbs(dir) || strings.ContainsAny(dir, "\x00\r\n") {
		return "Choose an absolute folder path for the backups."
	}
	dir = filepath.Clean(dir)
	if filepath.Dir(dir) == dir {
		return "Backups can't be kept at the top of a drive. Choose or create a folder on it."
	}
	protected := []string{a.installDir(), filepath.Dir(a.configPath())}
	if state, err := a.clinicStateDir(); err == nil {
		protected = append(protected, state)
	}
	if a.log != nil {
		protected = append(protected, a.log.Folder())
	}
	for _, p := range protected {
		if p != "" && insideDir(dir, p) {
			return fmt.Sprintf("Backups can't be kept inside CARE's own folders (%s).", p)
		}
	}
	if b, err := a.loadSetupBackup(); err == nil && b.Path != "" && insideDir(b.Path, dir) {
		return "Keep the backup recovery file separate from the backup folder. Choose another folder."
	}
	if !managed {
		if problem := a.chosenBackupDirProblem(dir); problem != "" {
			return problem
		}
	}
	if u, err := diskspace.Of(dir); err == nil && u.Free < setupBackupNeed {
		return fmt.Sprintf("Not enough free space there: backups need at least %s, and %s is free.", diskSize(setupBackupNeed), diskSize(u.Free))
	}
	return ""
}

func (a *App) chosenBackupDirProblem(dir string) string {
	if runtime.GOOS == "darwin" {
		for _, p := range macProtectedFolders() {
			if insideDir(dir, p) {
				return "macOS doesn't let the CARE Clinic service write to your Desktop, Documents or Downloads folders, or to iCloud Drive. Choose another folder, for example one on an external drive."
			}
		}
	}
	info, err := os.Stat(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "That folder isn't there any more. If it is on a removable drive, plug the drive in and choose it again."
	case err != nil:
		return "Couldn't open that folder: " + err.Error()
	case !info.IsDir():
		return "That is a file, not a folder. Choose a folder to keep the backups in."
	}
	if sets, err := backups.HostSets(dir); err != nil {
		return "Couldn't check that folder for earlier backups: " + err.Error()
	} else if len(sets) > 0 {
		return "That folder already holds backups from another CARE installation. Choose a different folder and leave the existing backups intact."
	}
	probe, err := os.CreateTemp(dir, ".care-write-test-*")
	if err != nil {
		switch {
		case errors.Is(err, syscall.EROFS):
			return "That drive is read-only, so backups can't be written to it. Choose a different folder."
		case errors.Is(err, os.ErrPermission):
			return "This computer isn't allowed to write to that folder. Choose a different folder."
		default:
			return "Couldn't write to that folder: " + err.Error()
		}
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
	return ""
}

func diskSize(n uint64) string {
	return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
}

func (a *App) BackupDirSpace(dir string) BackupSpace {
	dir = a.setupBackupDir(dir)
	s := BackupSpace{Dir: dir, Need: setupBackupNeed, DaysLeft: -1}
	if dir == "" || !filepath.IsAbs(dir) {
		return s
	}
	if u, err := diskspace.Of(dir); err == nil {
		s.Free, s.Total = u.Free, u.Total
	}
	return s
}

func (a *App) ChooseFolder(title string) (dir string, err error) {
	defer a.logError(&err)
	opts := wruntime.OpenDialogOptions{Title: title, CanCreateDirectories: true}
	if home, err := os.UserHomeDir(); err == nil {
		opts.DefaultDirectory = home
	}
	return a.dialogs().dir(opts)
}

func (a *App) SaveSetupBackupRecovery(backupDir string) (bool, error) {
	return a.saveSetupBackupRecovery(backupDir, false)
}

func (a *App) ReplaceSetupBackupRecovery(backupDir string) (bool, error) {
	return a.saveSetupBackupRecovery(backupDir, true)
}

func (a *App) saveSetupBackupRecovery(backupDir string, replace bool) (bool, error) {
	saved := false
	err := a.withServerJob(func() error {
		if err := a.requireBackupSetup(); err != nil {
			return err
		}
		b, err := a.loadSetupBackup()
		if err != nil && !replace {
			return err
		}
		if b.Certificate != "" && !replace {
			return errors.New("the recovery file has already been saved; select it using Select saved file")
		}
		filename, err := recoveryFilename(a.loadConfig().MDNSName, "backup-recovery-key", "pem")
		if err != nil {
			return err
		}
		path, err := a.dialogs().save(wruntime.SaveDialogOptions{
			DefaultDirectory: recoverySaveDirectory(),
			Title:            "Save backup recovery file on a separate secure drive",
			DefaultFilename:  filename,
		})
		if err != nil || path == "" {
			return err
		}
		if err := a.setupRecoveryLocation(path, backupDir); err != nil {
			return err
		}
		cert, key, err := generateBackupKey()
		if err != nil {
			return fmt.Errorf("could not generate the backup recovery file: %w", err)
		}
		if err := saveRecoveryFile(path, key); err != nil {
			return err
		}
		if err := a.saveSetupBackup(setupBackup{Certificate: string(cert), RecoveryKey: string(key), Path: path}); err != nil {
			return fmt.Errorf("the saved file was not activated; save a new recovery file before installing: %w", err)
		}
		a.logln("Saved the backup recovery file to " + path)
		saved = true
		return nil
	})
	return saved, err
}

func (a *App) VerifySetupBackupRecovery(backupDir string) (bool, error) {
	verified := false
	err := a.withServerJob(func() error {
		if err := a.requireBackupSetup(); err != nil {
			return err
		}
		b, err := a.loadSetupBackup()
		if err != nil {
			return err
		}
		if b.Certificate == "" {
			return errors.New("save the recovery file before checking it")
		}
		path, err := a.dialogs().file(wruntime.OpenDialogOptions{
			Title:   "Choose your backup recovery file",
			Filters: []wruntime.FileFilter{{DisplayName: "CARE backup recovery file (*.pem)", Pattern: "*.pem"}},
		})
		if err == nil && path == "" {
			return nil
		}
		b.Verified = false
		if serr := a.saveSetupBackup(b); serr != nil {
			return errors.Join(err, serr)
		}
		if err != nil {
			return err
		}
		if err := a.setupRecoveryLocation(path, backupDir); err != nil {
			return err
		}
		data, err := stack.ReadRecoveryKey(path)
		if err != nil {
			return fmt.Errorf("couldn't read the recovery file: %w", err)
		}
		if err := matchRecoveryKey([]byte(b.Certificate), data); err != nil {
			return err
		}
		b.Verified, b.Path = true, path
		if err := a.saveSetupBackup(b); err != nil {
			return err
		}
		a.logln("Checked the backup recovery file " + path)
		verified = true
		return nil
	})
	return verified, err
}

func (a *App) OpenSetupBackupRecoveryFolder() error {
	return a.withReadJob(func() error {
		if err := a.requireBackupSetup(); err != nil {
			return err
		}
		b, err := a.loadSetupBackup()
		if err != nil {
			return err
		}
		if b.Path == "" {
			return errors.New("save the recovery file before opening its folder")
		}
		if _, err := os.Stat(b.Path); err != nil {
			return fmt.Errorf("couldn't find the saved recovery file: %w", err)
		}
		var cmd *exec.Cmd
		switch runtime.GOOS {
		case "darwin":
			cmd = proc.Command("open", "-R", b.Path)
		case "windows":
			cmd = proc.Command("explorer.exe", "/select,"+b.Path)
		default:
			cmd = proc.Command("xdg-open", filepath.Dir(b.Path))
		}
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("couldn't open the recovery folder: %w", err)
		}
		go func() { _ = cmd.Wait() }()
		return nil
	})
}

func (a *App) prepareSetupBackupKey(backupDir string) (setupBackup, error) {
	b, err := a.loadSetupBackup()
	if err != nil {
		return b, err
	}
	if b.Certificate == "" {
		return b, errors.New("save the backup recovery file before installing")
	}
	if err := matchRecoveryKey([]byte(b.Certificate), []byte(b.RecoveryKey)); err != nil {
		return b, fmt.Errorf("the backup key made during setup is damaged; save a new recovery file: %w", err)
	}
	if s := a.setupBackupStatus(b); !s.Verified {
		return b, errors.New("select and check the saved backup recovery file before installing")
	}
	if problem := a.backupDirProblem(backupDir); problem != "" {
		return b, errors.New(problem)
	}
	return b, nil
}

func (a *App) applySetupBackups(ctx context.Context, backupDir string, b setupBackup) error {
	if backupDir = strings.TrimSpace(backupDir); backupDir != "" {
		cctx, cancel := context.WithTimeout(ctx, serviceCallTimeout)
		cfg, err := a.svc().UpdateConfig(cctx, api.ServiceConfigUpdate{BackupDir: &backupDir})
		cancel()
		if err != nil {
			return fmt.Errorf("couldn't set the backup folder: %w", err)
		}
		a.logln("Backups go to " + cfg.BackupDir)
	}
	if err := a.installBackupKey([]byte(b.Certificate), []byte(b.RecoveryKey)); err != nil {
		return fmt.Errorf("couldn't install the backup key: %w", err)
	}
	if err := a.removeSetupBackup(); err != nil {
		a.logln("warning: couldn't remove " + SetupBackupFile + ": " + err.Error())
	}
	return nil
}

func (a *App) installBackupKey(cert, key []byte) error {
	dir, err := a.clinicStateDir()
	if err != nil {
		return err
	}
	certPath, keyPath := filepath.Join(dir, stack.BackupCert), filepath.Join(dir, stack.BackupRecoveryKey)
	oldCert, certErr := os.ReadFile(certPath)
	oldKey, keyErr := os.ReadFile(keyPath)
	if certErr == nil && keyErr == nil && bytes.Equal(oldCert, cert) && bytes.Equal(oldKey, key) {
		return nil
	}
	suffix := ".replaced-" + time.Now().UTC().Format("20060102T150405Z")
	for path, err := range map[string]error{certPath: certErr, keyPath: keyErr} {
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if err := os.Rename(path, path+suffix); err != nil {
			return err
		}
		a.logln("Kept the earlier backup key file as " + path + suffix + "; it still unlocks earlier backups")
	}
	if err := atomicfile.WritePrivate(keyPath, key); err != nil {
		return err
	}
	if err := atomicfile.Write(certPath, cert, 0o644); err != nil {
		return err
	}
	a.logln("Installed the backup recovery key saved during setup: " + keyPath)
	return nil
}
