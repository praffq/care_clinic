package main

import (
	"runtime"

	"github.com/ohcnetwork/care_desktop/app/internal/sys/mdns"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/netfix"

	"golang.org/x/crypto/bcrypt"
)

type AppState struct {
	Role      string `json:"role"`
	ClientURL string `json:"client_url"`
	Version   string `json:"version"`
	Platform  string `json:"platform"`
	SetupDone bool   `json:"setup_done"`
	// SetupStarted: setup ran before but didn't finish.
	SetupStarted bool   `json:"setup_started"`
	MDNSName     string `json:"mdns_name"`
}

func (a *App) GetState() (state AppState, err error) {
	defer a.logError(&err)
	cfg := a.loadConfig()
	state = AppState{Version: a.pins.AppVersion, Platform: runtime.GOOS, Role: cfg.Role, ClientURL: cfg.ClientURL}
	if cfg.Role != roleServer {
		return state, nil
	}
	state.SetupDone = cfg.SetupDone && !cfg.Removing
	state.SetupStarted = !cfg.SetupDone && (cfg.AdminPwHash != "" || cfg.Removing)
	state.MDNSName = cfg.MDNSName
	return state, nil
}

func (a *App) NetworkStatus() netfix.Status { return netfix.Check(a.engine().Runner()) }

func (a *App) FixNetwork() error {
	return a.withLabeledJob(jobNetwork, func() error { return netfix.Fix(a.engine().Log) })
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

// SetMDNSName records the clinic address chosen during setup; RunSetup
// hands it to the service.
func (a *App) SetMDNSName(name string) (err error) {
	defer a.logError(&err)
	if err := mdns.ValidateLabel(name); err != nil {
		return err
	}
	return a.withServerJob(func() error {
		cfg := a.loadConfig()
		if cfg.SetupDone || cfg.Removing {
			return errStartedSetup
		}
		cfg.MDNSName = mdns.Label(name) + ".local"
		return a.saveConfig(cfg)
	})
}

func (a *App) VerifyAdminPassword(pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(a.loadConfig().AdminPwHash), []byte(pw)) == nil
}

// MDNSStatus checks a clinic address before setup: free on the network, or
// already this computer's own.
func (a *App) MDNSStatus(name string) mdns.NameStatus {
	if name == "" {
		return mdns.NameStatus{OK: false, Message: "No clinic address chosen yet"}
	}
	if err := mdns.ValidateLabel(name); err != nil {
		return mdns.NameStatus{OK: false, Message: err.Error()}
	}
	host := mdns.Label(name) + ".local"
	if a.serviceAdvertises(a.clientContext(), name) {
		return mdns.NameStatus{OK: true, Message: "This computer already answers for " + host}
	}
	if err := a.checkName(name); err != nil {
		return mdns.NameStatus{OK: false, Message: err.Error()}
	}
	return mdns.NameStatus{OK: true, Message: "No other device answered for " + host}
}

// ChangeAdminPassword replaces the CARE Clinic admin password.
func (a *App) ChangeAdminPassword(currentPassword, newPassword string) error {
	return a.withServerJob(func() error {
		if err := a.requireAdmin(currentPassword); err != nil {
			return err
		}
		if err := ValidatePassword(newPassword); err != nil {
			return err
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		cfg := a.loadConfig()
		cfg.AdminPwHash = string(hash)
		return a.saveConfig(cfg)
	})
}
