package main

import (
	"context"
	"errors"
	"fmt"
	"os/user"
	"runtime"
	"strings"
	"time"

	"github.com/ohcnetwork/care_desktop/app/cmd/care-clinic-service/launchd"
	"github.com/ohcnetwork/care_desktop/app/internal/clinic"
	"github.com/ohcnetwork/care_desktop/app/internal/hostcheck"
	"github.com/ohcnetwork/care_desktop/app/internal/service/api"
	"github.com/ohcnetwork/care_desktop/app/internal/service/install"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/diskspace"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/mdns"

	"golang.org/x/crypto/bcrypt"
)

const serviceStartTimeout = 60 * time.Second

func (a *App) hostProbes() hostcheck.Probes {
	if a.host != nil {
		return *a.host
	}
	diskPath := a.installDir()
	if runtime.GOOS == "darwin" {
		diskPath = install.Root
	}
	return hostcheck.Probes{
		Virtualization:   hostcheck.HostVirtualization,
		Memory:           hostcheck.HostMemory,
		Disk:             func() (diskspace.Usage, error) { return diskspace.Of(diskPath) },
		ServiceInstalled: install.Installed,
		ServicePing: func(ctx context.Context) error {
			_, err := a.svc().Status(ctx)
			return err
		},
		CanInstallService: install.Supported,
	}
}

// HostChecks runs onboarding's computer checks.
func (a *App) HostChecks() hostcheck.Report {
	return hostcheck.Run(a.clientContext(), a.hostProbes())
}

// InstallService installs (or repairs) care-clinic-service from the copy
// inside the app, behind one administrator prompt, and waits for it to
// answer.
func (a *App) InstallService() error {
	return a.withLabeledJob(jobService, func() error {
		path, err := a.bundleLayout().ServicePath()
		if err != nil {
			return err
		}
		u, err := user.Current()
		if err != nil {
			return fmt.Errorf("couldn't identify the logged-in user: %w", err)
		}
		a.logln("Installing the CARE Clinic service from " + path + "...")
		inst := a.installService
		if inst == nil {
			inst = install.Install
		}
		if err := inst(install.Plan{Binary: path, Plist: launchd.Plist, User: u.Username}); err != nil {
			return fmt.Errorf("couldn't install the CARE Clinic service: %w", err)
		}
		if err := a.waitServiceReachable(serviceStartTimeout); err != nil {
			return err
		}
		a.logln("The CARE Clinic service is installed and running.")
		return nil
	})
}

func (a *App) waitServiceReachable(timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(a.clientContext(), timeout)
	defer cancel()
	var err error
	for {
		if _, err = a.svc().Status(ctx); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			if strings.Contains(err.Error(), "permission denied") {
				return errors.New("the service is installed, but this session can't reach it yet; " +
					"log out of this Mac and back in (so it picks up the care-clinic group), then open CARE Clinic again")
			}
			return fmt.Errorf("the service was installed but isn't answering: %w", err)
		case <-time.After(pollInterval(timeout)):
		}
	}
}

// SetupFailure tells the setup screen whether RetrySetup can resume.
type SetupFailure struct {
	CanRetry bool `json:"can_retry"`
}

type setupAttempt struct {
	config   Config
	prepare  func() error
	start    func() error
	prepared bool
}

func (s *setupAttempt) run() error {
	if !s.prepared {
		if err := s.prepare(); err != nil {
			return err
		}
		s.prepared = true
	}
	return s.start()
}

func (a *App) setupFailure(error) SetupFailure {
	cfg := a.loadConfig()
	return SetupFailure{
		CanRetry: a.setupAttempt != nil && a.setupAttempt.config == cfg && cfg.Role == roleServer &&
			cfg.AdminPwHash != "" && !cfg.SetupDone && !cfg.Removing,
	}
}

// RunSetup installs the clinic: the engine bundle through the service, then
// the CARE stack through the engine. The service must already be installed
// (HostChecks / InstallService).
func (a *App) RunSetup(mdnsName, adminPassword string) (err error) {
	defer a.logError(&err)
	if err := ValidatePassword(adminPassword); err != nil {
		return err
	}
	host := mdns.Label(mdnsName)
	if host == "" {
		host = "care"
	}
	if err := mdns.ValidateLabel(host); err != nil {
		return err
	}
	if err := a.requireServer(); err != nil {
		return err
	}
	if err := a.lockJob(); err != nil {
		return err
	}
	if cfg := a.loadConfig(); cfg.SetupDone || cfg.Removing {
		a.jobMu.Unlock()
		return errors.New("this computer already has a clinic set up")
	}
	return a.runLockedJob(func() error {
		cfg := a.loadConfig()
		ctx := a.clientContext()
		if err := a.checkNameFree(ctx, host); err != nil {
			return err
		}
		cfg.MDNSName = host + ".local"
		h, err := bcrypt.GenerateFromPassword([]byte(adminPassword), bcrypt.DefaultCost)
		if err != nil {
			return fmt.Errorf("couldn't secure the admin password: %w", err)
		}
		cfg.AdminPwHash = string(h)
		if err := a.saveConfig(cfg); err != nil {
			return err
		}
		var e *clinic.Clinic
		a.setupAttempt = &setupAttempt{
			config: cfg,
			prepare: func() error {
				if err := a.prepareEngine(ctx, host); err != nil {
					return err
				}
				if _, err := a.ensureInstallDir(); err != nil {
					return err
				}
				e = a.engine()
				e.AdminPassword = adminPassword
				return e.Setup()
			},
			start: func() error { return e.Start() },
		}
		return a.setupAttempt.run()
	}, true, "setup")
}

// prepareEngine gets the engine installed, running and advertising host.
func (a *App) prepareEngine(ctx context.Context, host string) error {
	if err := a.waitServiceReachable(10 * time.Second); err != nil {
		return fmt.Errorf("the CARE Clinic service isn't running; install it from the computer check first (%w)", err)
	}
	if err := a.ensureEngine(ctx); err != nil {
		return err
	}
	if err := a.waitEngineReady(ctx); err != nil {
		return err
	}
	if _, err := a.svc().UpdateConfig(ctx, api.ServiceConfigUpdate{MDNSName: &host}); err != nil {
		return fmt.Errorf("couldn't set the clinic address: %w", err)
	}
	return nil
}

// checkNameFree checks no other device answers for host, unless this
// computer's service already advertises it.
func (a *App) checkNameFree(ctx context.Context, host string) error {
	if a.serviceAdvertises(ctx, host) {
		return nil
	}
	return a.checkName(host)
}

func (a *App) serviceAdvertises(ctx context.Context, host string) bool {
	ctx, cancel := context.WithTimeout(ctx, serviceCallTimeout)
	defer cancel()
	cfg, err := a.svc().Config(ctx)
	return err == nil && mdns.Label(cfg.MDNSName) == mdns.Label(host)
}

func (a *App) checkName(name string) error {
	if a.nameCheck != nil {
		return a.nameCheck(name)
	}
	return mdns.CheckAvailable(name)
}

// RetrySetup resumes only the attempt this process already validated and
// prepared. It never accepts replacement settings.
func (a *App) RetrySetup() (err error) {
	defer a.logError(&err)
	if err := a.requireServer(); err != nil {
		return err
	}
	if err := a.lockJob(); err != nil {
		return err
	}
	cfg := a.loadConfig()
	if cfg.SetupDone || cfg.Removing || a.setupAttempt == nil || cfg.AdminPwHash == "" {
		a.jobMu.Unlock()
		return errors.New("there is no unfinished installation that can be retried in this session")
	}
	if cfg != a.setupAttempt.config {
		a.jobMu.Unlock()
		return errors.New("the saved setup changed; return to setup before trying again")
	}
	return a.runLockedJob(a.setupAttempt.run, true, "setup")
}

// CleanupFailedInstall removes an unfinished clinic install (its stack and
// data in the engine, and the local install files) so setup can start
// over. The service and the engine stay installed.
func (a *App) CleanupFailedInstall() error {
	return a.withServerJob(func() error {
		cfg := a.loadConfig()
		if cfg.SetupDone {
			return errors.New("this clinic is installed; use Uninstall instead of failed-install cleanup")
		}
		if err := a.beginRemoval(); err != nil {
			return err
		}
		e := a.engine()
		if _, err := a.svc().Status(a.clientContext()); err != nil {
			// Nothing reached the engine without the service.
			a.logln("The CARE Clinic service isn't answering; removing this computer's install files only.")
			if err := e.RemoveInstallFiles(); err != nil {
				return err
			}
		} else if err := e.Uninstall(clinic.UninstallOptions{RemoveData: true, RemoveInstallDir: true}); err != nil {
			return err
		}
		a.setupAttempt = nil
		return a.forgetConfig()
	})
}
