package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/ohcnetwork/care_desktop/app/internal/backup"
	"github.com/ohcnetwork/care_desktop/app/internal/health"
	"github.com/ohcnetwork/care_desktop/app/internal/prereq"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/autostart"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/mdns"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/netfix"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/reboot"
	"github.com/ohcnetwork/care_desktop/app/internal/vm"

	"golang.org/x/crypto/bcrypt"
)

type AppState struct {
	Role                  string        `json:"role"`
	ClientURL             string        `json:"client_url"`
	Version               string        `json:"version"`
	Platform              string        `json:"platform"`
	SetupDone             bool          `json:"setup_done"`
	MDNSName              string        `json:"mdns_name"`
	Docker                prereq.Status `json:"docker"`
	RestorePending        bool          `json:"restore_pending"`
	PluginRecoveryPending bool          `json:"plugin_recovery_pending"`
}

func (a *App) GetState() (state AppState, err error) {
	defer a.logError(&err)
	cfg := a.loadConfig()
	if cfg.Role != roleServer {
		return AppState{Version: a.pins.AppVersion, Platform: runtime.GOOS, Role: cfg.Role, ClientURL: cfg.ClientURL}, nil
	}
	pending, err := a.engine().Backups().PendingRestore()
	if err != nil {
		return AppState{}, err
	}
	pluginPending, pluginErr := a.engine().PendingPluginRecovery()
	if pluginErr != nil {
		// Keep the recovery controls available even when the journal is damaged.
		a.logln("error: " + pluginErr.Error())
	}
	return AppState{
		Role:                  cfg.Role,
		ClientURL:             cfg.ClientURL,
		Version:               a.pins.AppVersion,
		Platform:              runtime.GOOS,
		SetupDone:             cfg.SetupDone && !cfg.Removing,
		MDNSName:              cfg.MDNSName,
		Docker:                a.DockerStatus(),
		RestorePending:        pending,
		PluginRecoveryPending: pluginPending,
	}, nil
}

type dockerStatusCall struct {
	done   chan struct{}
	result prereq.Status
}

func (a *App) DockerStatus() prereq.Status {
	return a.readDockerStatus(func() prereq.Status { return prereq.DockerCheck(a.engine().Runner()) })
}

// Share only in-flight queries; subsequent checks always inspect current readiness.
func (a *App) readDockerStatus(check func() prereq.Status) prereq.Status {
	a.dockerCheckMu.Lock()
	if call := a.dockerCheck; call != nil {
		a.dockerCheckMu.Unlock()
		<-call.done
		return call.result
	}
	call := &dockerStatusCall{done: make(chan struct{})}
	a.dockerCheck = call
	a.dockerCheckMu.Unlock()
	call.result = check()
	a.dockerCheckMu.Lock()
	a.dockerCheck = nil
	close(call.done)
	a.dockerCheckMu.Unlock()
	return call.result
}

func (a *App) ClinicHealth() health.Health { return health.Ping() }

func (a *App) NetworkStatus() netfix.Status { return netfix.Check(a.engine().Runner()) }

func (a *App) FixNetwork() error {
	return a.withLabeledJob(jobPrereq, func() error {
		if err := netfix.Fix(a.engine().Log); err != nil {
			return err
		}
		if err := a.restartAdvertise(); err != nil {
			return fmt.Errorf("network repair completed, but clinic advertising could not be verified: %w", err)
		}
		return nil
	})
}

func (a *App) WindowsVMStatus() prereq.WindowsVMStatus { return prereq.WindowsVMCheck() }

func (a *App) EnableWindowsVM() (string, error) {
	var result string
	err := a.withLabeledJob(jobPrereq, func() error {
		var err error
		result, err = a.provisioner().EnableWindowsVM()
		return err
	})
	return result, err
}

func (a *App) DockerPlan() prereq.ToolPlan { return a.provisioner().DockerPlan() }

func (a *App) EngineDownloadInfo() (info prereq.DownloadInfo, err error) {
	defer a.logError(&err)
	if err := a.requireServer(); err != nil {
		return prereq.DownloadInfo{}, err
	}
	return a.provisioner().EngineDownloadInfo()
}

func (a *App) InstallDocker() (string, error) {
	var result string
	err := a.withLabeledJob(jobPrereq, func() error {
		var err error
		result, err = a.provisioner().InstallDocker()
		return err
	})
	return result, err
}

func (a *App) OpenDocker() error {
	return a.withLabeledJob(jobPrereq, func() error { return a.provisioner().OpenDocker() })
}

func (a *App) provisioner() *prereq.Provisioner {
	e := a.engine()
	pr := prereq.NewProvisioner(e.Runner(), a.pins, e.Log)
	pr.ReleaseDir = a.installDir()
	pr.DiagnosticsDir = a.log.Folder()
	pr.Progress = func(progress prereq.DownloadProgress) {
		a.emit("prereq-download-progress", progress)
	}
	return pr
}

func (a *App) RestartPlan() reboot.Plan { return reboot.Check() }

func (a *App) RestartNow() error {
	return a.withServerJob(func() error {
		if err := autostart.Set(true); err != nil {
			a.logln("note: couldn't set CARE Clinic to open after the restart (" + err.Error() +
				") - open it yourself once the computer is back")
		}
		return reboot.Now()
	})
}

func (a *App) ValidatePassword(pw string) string {
	if err := ValidatePassword(pw); err != nil {
		return err.Error()
	}
	return ""
}

func (a *App) ValidateDomain(name string) string {
	if err := mdns.ValidateLabel(name); err != nil {
		return err.Error()
	}
	return ""
}

func (a *App) ValidateBackupDir(dir string) string {
	if err := a.requireServer(); err != nil {
		return err.Error()
	}
	dir = strings.TrimSpace(dir)
	target := a.engine().BackupDirPath()
	if dir != "" {
		if !filepath.IsAbs(dir) {
			return "Choose an absolute folder path for the backups."
		}
		target = filepath.Join(dir, "care-db-backups")
	}
	for _, protected := range []string{a.installDir(), filepath.Dir(a.configPath()), a.log.Folder()} {
		if protected != "" {
			if err := backup.CheckLocation(target, protected); err != nil {
				return err.Error()
			}
		}
	}
	if recoveryPath := a.loadConfig().BackupRecoveryPath; recoveryPath != "" {
		if err := backup.CheckLocation(recoveryPath, target); err != nil {
			return "Keep the backup recovery file separate from the backup folder. Choose another folder."
		}
	}
	e := a.engine()
	e.BackupDir = target
	foreign, err := e.Backups().ForeignRecoveryData()
	if err != nil {
		return "Couldn't check that folder for earlier backups: " + err.Error()
	}
	if foreign {
		return "That folder already holds backups from another CARE installation. Choose a different folder and leave the existing backups intact."
	}
	if dir == "" {
		return a.backupSpaceProblem(target)
	}
	info, err := os.Stat(dir)
	switch {
	case os.IsNotExist(err):
		return "That folder isn't there any more. If it is on a removable drive, plug the drive in and choose it again."
	case err != nil:
		return "Couldn't open that folder: " + err.Error()
	case !info.IsDir():
		return "That is a file, not a folder. Choose a folder to keep the backups in."
	}
	probe, err := os.CreateTemp(dir, ".care-write-test-*")
	if err != nil {
		return backupWriteProblem(err, runtime.GOOS)
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
	return a.backupSpaceProblem(target)
}

func backupWriteProblem(err error, goos string) string {
	switch {
	case errors.Is(err, syscall.EROFS):
		hint := ""
		if goos == "darwin" {
			hint = " Disk images and USB drives formatted for Windows (NTFS) can be read-only on this Mac."
		}
		return "That drive is read-only, so backups can't be written to it." + hint + " Choose a different folder."
	case errors.Is(err, os.ErrPermission):
		return "This computer isn't allowed to write to that folder. Choose a different folder."
	default:
		return "Couldn't write to that folder: " + err.Error()
	}
}

func (a *App) SetMDNSName(name string) (err error) {
	defer a.logError(&err)
	if err := mdns.ValidateLabel(name); err != nil {
		return err
	}
	return a.withServerJob(func() error {
		cfg := a.loadConfig()
		if cfg.SetupDone || cfg.Removing {
			return errors.New("the clinic address can only be chosen before installation")
		}
		cfg.MDNSName = mdns.Label(name) + ".local"
		if err := a.saveConfig(cfg); err != nil {
			return err
		}
		return a.restartAdvertise()
	})
}

func (a *App) VerifyAdminPassword(pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(a.loadConfig().AdminPwHash), []byte(pw)) == nil
}

func (a *App) MDNSStatus(name string) mdns.NameStatus {
	if name == "" {
		return mdns.NameStatus{OK: false, Message: "No clinic address chosen yet"}
	}
	if err := mdns.ValidateLabel(name); err != nil {
		return mdns.NameStatus{Message: err.Error()}
	}
	cfg := a.loadConfig()
	if vm.Supported() && cfg.Role == roleServer && cfg.SetupDone {
		s := vm.Status(context.Background())
		if s.AdvertisementSupported {
			return persistentNameStatus(name, s)
		}
	}
	if err := mdns.CheckAvailable(name); err != nil {
		return mdns.NameStatus{OK: false, Message: err.Error()}
	}
	name = mdns.Label(name) + ".local"
	if !cfg.SetupDone && cfg.AdminPwHash == "" {
		return mdns.NameStatus{OK: true, Message: "No other device answered for " + name}
	}
	a.advMu.Lock()
	adv := a.adv
	a.advMu.Unlock()
	if adv == nil {
		return mdns.NameStatus{OK: false, Message: "Not advertising " + name}
	}
	s := adv.Status()
	if s.Error != "" {
		return mdns.NameStatus{Message: s.Error}
	}
	if s.Name+".local" != name || !s.Running || !s.Verified {
		return mdns.NameStatus{Message: "Not yet verified advertising " + name}
	}
	return mdns.NameStatus{OK: true, Message: name + " is being advertised; no conflicting device answered"}
}

func persistentNameStatus(name string, s vm.HostStatus) mdns.NameStatus {
	name = mdns.Label(name) + ".local"
	ad := s.Advertisement
	if ad.Error != "" {
		return mdns.NameStatus{Message: ad.Error}
	}
	if !persistentNameReady(name, s) {
		return mdns.NameStatus{Message: "The CARE engine is not yet advertising the available clinic at " + name}
	}
	return mdns.NameStatus{OK: true, Message: name + " is advertised by the persistent CARE engine helper"}
}

func persistentNameReady(name string, s vm.HostStatus) bool {
	return s.Running && s.AdvertisementSupported && s.Advertisement.Running &&
		s.Advertisement.Verified && s.Advertisement.Error == "" &&
		s.Advertisement.Name == mdns.Label(name)
}
