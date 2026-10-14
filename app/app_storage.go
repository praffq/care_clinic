package main

import (
	"context"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/backupcopy"
	"github.com/ohcnetwork/care_desktop/app/internal/health"
	"github.com/ohcnetwork/care_desktop/app/internal/storage"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/diskspace"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	storageFirstCheck = 20 * time.Second
	storageInterval   = 5 * time.Minute

	backupExportTimeout      = 30 * time.Minute
	backupStateExportTimeout = 10 * time.Second
)

type storageWatch struct {
	mu         sync.Mutex
	report     storage.Report
	checked    bool
	alerted    string
	notifyOnce sync.Once
	notifyOK   bool
	copying    bool
	copyErr    string
	exportErr  string
	stateErr   string
}

// backupHealth reads the backup state and adds failures the app itself saw
// but could not record in the state folder.
func (a *App) backupHealth(source, stateDir string, running bool, now time.Time) storage.BackupHealth {
	h := storage.ReadHealth(source, stateDir, running, now)
	if a.loadConfig().BackupCopyDir == "" && h.Copy.State != "off" {
		h.Copy = storage.CopyStatus{State: "off", CheckedAt: h.Copy.CheckedAt}
		h.Alerts = slices.DeleteFunc(h.Alerts, func(al storage.Alert) bool { return strings.HasPrefix(al.Code, "copy_") })
	}
	a.store.mu.Lock()
	copyErr := a.store.copyErr
	exportErr, stateErr := a.store.exportErr, a.store.stateErr
	a.store.mu.Unlock()
	if copyErr != "" {
		h.Alerts = append(h.Alerts, storage.Alert{Code: "copy_status_unwritable", Level: storage.LevelLow,
			Message: "The backup copy status could not be saved: " + copyErr})
	}
	if exportErr != "" || stateErr != "" {
		msg := exportErr
		if msg == "" {
			msg = stateErr
		}
		h.Alerts = append(h.Alerts, storage.Alert{Code: "engine_export_failed", Level: storage.LevelLow,
			Message: "The engine's latest backups or status could not be exported; this computer's backup view may be stale: " + msg})
	}
	return h
}

// startBackupCopy brings the optional second copy up to date in the
// background; a large first copy must never hold up the storage check.
func (a *App) startBackupCopy(source, stateDir, dest string) {
	a.store.mu.Lock()
	if a.store.copying {
		a.store.mu.Unlock()
		return
	}
	a.store.copying = true
	a.store.mu.Unlock()
	go func() {
		// The guest backup folder is the source of truth; bring the host's
		// verified export (new nightly sets and saved changes) up to date
		// before the copy reads it.
		ctx, cancel := context.WithTimeout(context.Background(), backupExportTimeout)
		exportErr := a.engine().ExportBackups(ctx)
		cancel()
		if exportErr != nil {
			a.logln("backup export: " + exportErr.Error())
			a.store.mu.Lock()
			a.store.copying = false
			a.store.exportErr = exportErr.Error()
			a.store.mu.Unlock()
			a.emit("care-backup-copy", storage.CopyStatus{State: "failed", Message: "Engine backup export failed: " + exportErr.Error()})
			return
		}
		st, err := backupcopy.Run(source, stateDir, dest, time.Now())
		if err != nil {
			a.logln("backup copy: " + err.Error())
			st.State, st.Message = "failed", err.Error()
		}
		a.store.mu.Lock()
		a.store.copying = false
		a.store.copyErr = ""
		a.store.exportErr = ""
		if err != nil {
			a.store.copyErr = err.Error()
		}
		a.store.mu.Unlock()
		a.emit("care-backup-copy", st)
	}()
}

func (a *App) DiskStatus() storage.InstallResult {
	return diskStatus(a.installDir(), a.dockerDataDir(), a.loadConfig().SetupDone, diskspace.Of)
}

func diskStatus(installDir, dockerDir string, setupDone bool,
	measure func(string) (diskspace.Usage, error)) storage.InstallResult {
	docker, err := measure(dockerDir)
	if err != nil {
		return unknownDiskStatus(err)
	}
	if setupDone {
		return storage.CheckRunning(docker)
	}
	install, err := measure(installDir)
	if err != nil {
		return unknownDiskStatus(err)
	}
	return storage.CheckInstall(install, docker)
}

func unknownDiskStatus(err error) storage.InstallResult {
	return storage.InstallResult{
		Message: "Couldn't measure free space: " + err.Error(),
		How:     "Make sure the drive is available, then check again.",
		Need:    storage.InstallMinFree,
	}
}

func (a *App) dockerDataDir() string {
	if runtime.GOOS == "linux" {
		if dir, err := a.engine().DockerRootDir(); err == nil {
			return dir
		}
	}
	return storage.DockerDataDir()
}

func (a *App) StorageStatus() storage.Report {
	a.store.mu.Lock()
	report, checked := a.store.report, a.store.checked
	a.store.mu.Unlock()
	if checked {
		return report
	}
	return a.RecheckStorage()
}

func (a *App) RecheckStorage() storage.Report {
	report := a.checkStorage()
	a.store.mu.Lock()
	a.store.report, a.store.checked = report, true
	a.store.mu.Unlock()
	a.emit("care-storage", report)
	a.alertStorage(report)
	return report
}

func (a *App) BackupDirSpace(dir string) storage.BackupSpace {
	target := a.engine().BackupDirPath()
	if dir != "" && filepath.IsAbs(dir) {
		target = filepath.Join(dir, "care-db-backups")
	}
	b, _ := a.backupSpace(target)
	return b
}

func (a *App) backupSpace(target string) (storage.BackupSpace, error) {
	e := a.engine()
	u, err := diskspace.Of(target)
	if err != nil {
		return storage.BackupSpace{Dir: target, Level: storage.LevelUnknown,
			Message: "Couldn't measure free space: " + err.Error()}, err
	}
	set, found, err := storage.LatestSet(e.BackupDirPath())
	if err != nil {
		found = false
	}
	shares := false
	if docker, err := diskspace.Of(a.dockerDataDir()); err == nil {
		shares = docker.Volume == u.Volume
	}
	return storage.AssessBackup(target, u, set, found, shares), nil
}

func (a *App) backupSpaceProblem(target string) string {
	b, err := a.backupSpace(target)
	if err != nil || b.Level != storage.LevelCritical {
		return ""
	}
	return b.Message + " Choose a folder on a drive with more free space."
}

func (a *App) checkStorage() storage.Report {
	now := time.Now()
	r := storage.Report{CheckedAt: now.Unix(), Level: storage.LevelOK}
	cfg := a.loadConfig()
	if cfg.Role != roleServer || !cfg.SetupDone {
		return r
	}
	e := a.engine()
	running := health.Ping().Active
	if docker, err := diskspace.Of(a.dockerDataDir()); err == nil {
		d := storage.AssessDrive("docker", "Clinic data", docker)
		d.Cleanable = running && runtime.GOOS == "linux"
		if runtime.GOOS != "linux" {
			d.Label = "This computer's drive"
			if d.Level == storage.LevelOK {
				d.Message = "Plenty of room for the CARE engine's disk to grow."
			}
		}
		r.Drives = append(r.Drives, d)
	}
	if running && runtime.GOOS != "linux" {
		if free, total, err := e.DockerDiskFree(); err == nil {
			d := storage.AssessDrive("vm", "CARE engine disk",
				diskspace.Usage{Path: "CARE engine", Free: free, Total: total})
			d.Cleanable = true
			r.Drives = append(r.Drives, d)
		}
	}
	stateDir := filepath.Join(a.installDir(), backupStateDir)
	if running {
		a.exportBackupState()
	}
	r.Backup, _ = a.backupSpace(e.BackupDirPath())
	a.startBackupCopy(e.BackupDirPath(), stateDir, cfg.BackupCopyDir)
	r.Health = a.backupHealth(e.BackupDirPath(), stateDir, running, now)
	r.LastRun = r.Health.LastRun
	r.NewestBackupAt = r.Health.LastBackupAt
	storage.Summarise(&r, running, now)
	return r
}

func (a *App) watchStorage() {
	timer := time.NewTimer(storageFirstCheck)
	defer timer.Stop()
	for {
		select {
		case <-a.advStop:
			return
		case <-timer.C:
			if a.loadConfig().SetupDone {
				a.RecheckStorage()
			}
			timer.Reset(storageInterval)
		}
	}
}

func (a *App) alertStorage(r storage.Report) {
	a.store.mu.Lock()
	if r.Level != storage.LevelCritical {
		a.store.alerted = ""
		a.store.mu.Unlock()
		return
	}
	fresh := r.Headline != a.store.alerted
	a.store.alerted = r.Headline
	a.store.mu.Unlock()
	if !fresh {
		return
	}
	a.logln("storage: " + r.Headline)
	if a.ctx == nil || !a.notificationsReady() {
		return
	}
	_ = wruntime.SendNotification(a.ctx, wruntime.NotificationOptions{
		ID:    "care-storage",
		Title: "CARE needs attention",
		Body:  r.Headline + " Open CARE Clinic to fix it.",
	})
}

func (a *App) notificationsReady() bool {
	a.store.notifyOnce.Do(func() {
		if wruntime.InitializeNotifications(a.ctx) != nil || !wruntime.IsNotificationAvailable(a.ctx) {
			return
		}
		ok, err := wruntime.CheckNotificationAuthorization(a.ctx)
		if err == nil && !ok {
			ok, err = wruntime.RequestNotificationAuthorization(a.ctx)
		}
		a.store.notifyOK = err == nil && ok
	})
	return a.store.notifyOK
}

// exportBackupState refreshes the host's copy of the guest backup status
// files that backup health reads. The exported backups themselves follow in
// the background copy pass.
func (a *App) exportBackupState() {
	ctx, cancel := context.WithTimeout(context.Background(), backupStateExportTimeout)
	defer cancel()
	err := a.engine().ExportBackupState(ctx)
	a.store.mu.Lock()
	a.store.stateErr = ""
	if err != nil {
		a.store.stateErr = err.Error()
	}
	a.store.mu.Unlock()
	if err != nil {
		a.logln("backup status export: " + err.Error())
	}
}
