package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ohcnetwork/care_desktop/app/internal/release"
)

func roleApp(t *testing.T) *App {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("USERPROFILE", root)
	return &App{
		configFile: filepath.Join(root, appDirName, "config.json"),
		pins:       &release.Pins{AppVersion: "test"},
	}
}

func TestLoadConfigInfersLegacyServerRole(t *testing.T) {
	for _, data := range []string{
		`{"setup_done":true}`,
		`{"removing":true}`,
		`{"admin_pw_hash":"partial-install"}`,
		`{"mdns_name":"clinic.local"}`,
	} {
		t.Run(data, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := loadConfig(path)
			if err != nil || cfg.Role != roleServer {
				t.Fatalf("legacy settings lost their server role: %+v, %v", cfg, err)
			}
		})
	}
}

func TestLoadConfigRejectsConflictingRoles(t *testing.T) {
	for _, data := range []string{
		`{"role":"other"}`,
		`{"role":"client","setup_done":true}`,
		`{"role":"client","admin_pw_hash":"existing-server"}`,
	} {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadConfig(path); err == nil {
			t.Fatalf("accepted conflicting settings: %s", data)
		}
	}
}

func TestRoleSelectionIsLockedUntilUninstall(t *testing.T) {
	for _, role := range []string{roleServer, roleClient} {
		t.Run(role, func(t *testing.T) {
			a := roleApp(t)
			if err := a.selectRole("invalid"); err == nil {
				t.Fatal("accepted invalid role")
			}
			if err := a.selectRole(role); err != nil {
				t.Fatal(err)
			}
			if err := a.selectRole(role); err != nil {
				t.Fatalf("selecting the same role should be idempotent: %v", err)
			}
			other := roleClient
			if role == roleClient {
				other = roleServer
			}
			if err := a.selectRole(other); err == nil {
				t.Fatal("changed role")
			}
			if err := a.saveConfig(Config{Role: other}); err == nil {
				t.Fatal("saveConfig bypassed role selection")
			}
			if err := a.forgetConfig(); err != nil {
				t.Fatal(err)
			}
			cfg, err := loadConfig(a.configPath())
			if err != nil || cfg.Role != role {
				t.Fatalf("cleanup forgot selected role: %+v, %v", cfg, err)
			}
			a.cfg = cfg
			if err := a.selectRole(other); err == nil {
				t.Fatal("changed role after restart")
			}
		})
	}
}

func TestClearRoleUndoesAnUnusedChoice(t *testing.T) {
	for _, role := range []string{roleServer, roleClient} {
		t.Run(role, func(t *testing.T) {
			a := roleApp(t)
			if err := a.selectRole(role); err != nil {
				t.Fatal(err)
			}
			if role == roleServer {
				if err := a.saveConfig(Config{Role: roleServer, MDNSName: "clinic.local"}); err != nil {
					t.Fatal(err)
				}
			}
			if err := a.ClearRole(); err != nil {
				t.Fatal(err)
			}
			cfg, err := loadConfig(a.configPath())
			if err != nil || cfg != (Config{}) || a.loadConfig() != (Config{}) {
				t.Fatalf("going back kept the role: %+v, %v", cfg, err)
			}
			if err := a.ClearRole(); err != nil {
				t.Fatalf("going back twice should be harmless: %v", err)
			}
			other := roleClient
			if role == roleClient {
				other = roleServer
			}
			if err := a.selectRole(other); err != nil {
				t.Fatalf("could not choose %s after going back: %v", other, err)
			}
		})
	}
}

func TestClearRoleIsLockedOnceSetUp(t *testing.T) {
	for name, cfg := range map[string]Config{
		"installed server":        {Role: roleServer, SetupDone: true, MDNSName: "clinic.local"},
		"partial server":          {Role: roleServer, AdminPwHash: "partial"},
		"removing server":         {Role: roleServer, Removing: true},
		"connected client":        {Role: roleClient, ClientURL: "https://clinic.local"},
		"client with certificate": {Role: roleClient, ClientCertificate: "pinned"},
		"client owning trust":     {Role: roleClient, ClientCertificateOwned: true},
	} {
		t.Run(name, func(t *testing.T) {
			a := roleApp(t)
			if err := a.saveConfig(cfg); err != nil {
				t.Fatal(err)
			}
			if err := a.ClearRole(); err == nil {
				t.Fatal("cleared a role that is in use")
			}
			if a.loadConfig() != cfg {
				t.Fatalf("refused clear changed settings: %+v", a.loadConfig())
			}
		})
	}
	t.Run("install files", func(t *testing.T) {
		a := roleApp(t)
		if err := a.selectRole(roleServer); err != nil {
			t.Fatal(err)
		}
		dir := a.installDir()
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "earlier-installation"), []byte("preserve"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := a.ClearRole(); err == nil || a.loadConfig().Role != roleServer {
			t.Fatalf("cleared the role of a computer with install files: %v", err)
		}
	})
}

func TestPartialInstallationCannotBecomeClient(t *testing.T) {
	for _, source := range []string{"settings", "install"} {
		t.Run(source, func(t *testing.T) {
			a := roleApp(t)
			switch source {
			case "settings":
				a.cfg.AdminPwHash = "partial"
			case "install":
				dir := a.installDir()
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "earlier-installation"), []byte("preserve"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := a.selectRole(roleClient); err == nil {
				t.Fatal("existing server became a client")
			}
			if a.loadConfig().Role != roleServer {
				t.Fatal("existing server was not recognized")
			}
		})
	}
}

func TestClientAndUnchosenStateSkipServerChecks(t *testing.T) {
	for _, role := range []string{"", roleClient} {
		t.Run(role, func(t *testing.T) {
			a := roleApp(t)
			a.cfg = Config{Role: role, ClientURL: ""}
			if role == roleClient {
				a.cfg.ClientURL = "https://clinic.local/"
			}
			if err := os.MkdirAll(a.installDir(), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(a.installDir(), "restore-state.json"), []byte("invalid journal"), 0o600); err != nil {
				t.Fatal(err)
			}
			state, err := a.GetState()
			if err != nil || state.Role != role || state.ClientURL != a.cfg.ClientURL {
				t.Fatalf("non-server state consulted server recovery: %+v, %v", state, err)
			}
			if state.SetupDone || state.MDNSName != "" {
				t.Fatalf("non-server state included server checks: %+v", state)
			}
			a.refreshInstallDir()
			if a.beforeClose(context.Background()) || !a.closing {
				t.Fatal("non-server quit required a server shutdown")
			}
			a.shutdown(context.Background())
		})
	}
}

func TestClientRejectsServerMutations(t *testing.T) {
	a := roleApp(t)
	if err := a.selectRole(roleClient); err != nil {
		t.Fatal(err)
	}
	for name, action := range map[string]func() error{
		"setup": func() error {
			return a.RunSetup("clinic", settingsPassword, "")
		},
		"set address": func() error { return a.SetMDNSName("clinic") },
		"cleanup":     a.CleanupFailedInstall,
		"remove":      a.beginRemoval,
		"start":       func() error { return a.ClinicAction("start") },
		"uninstall":   func() error { return a.RunUninstall(false, "") },
	} {
		t.Run(name, func(t *testing.T) {
			if err := action(); err == nil || !strings.Contains(err.Error(), "server") {
				t.Fatalf("client bypassed server guard: %v", err)
			}
		})
	}
	cfg := a.loadConfig()
	if cfg.Role != roleClient || cfg.hasServerSettings() {
		t.Fatalf("client configuration was mutated into a server: %+v", cfg)
	}
}

func TestClientResetPreservesTrustOwnership(t *testing.T) {
	a := roleApp(t)
	a.cfg = Config{
		Role: roleClient, ClientURL: "https://clinic.local/",
		ClientCertificate: "certificate", ClientCertificateOwned: true,
	}
	if err := a.forgetConfig(); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(a.configPath())
	if err != nil || cfg != a.cfg || !cfg.ClientCertificateOwned {
		t.Fatalf("reset lost client trust ownership: %+v, %v", cfg, err)
	}
}

func TestNonClientsRejectClientOperations(t *testing.T) {
	for _, role := range []string{"", roleServer} {
		t.Run(role, func(t *testing.T) {
			a := roleApp(t)
			before := Config{Role: role}
			if err := a.saveConfig(before); err != nil {
				t.Fatal(err)
			}
			if err := a.DisconnectClient(); err == nil {
				t.Fatal("non-client bypassed the clinic access removal guard")
			}
			cfg, err := loadConfig(a.configPath())
			if err != nil || cfg != before || a.loadConfig() != before {
				t.Fatalf("rejected client operation changed settings: %+v, %v", cfg, err)
			}
		})
	}
}

// The first screen only asks a question. Nothing it can reach may write to the
// settings file, or a computer that was merely looked at comes back with a role.
func TestStartPageOperationsWriteNothing(t *testing.T) {
	a := roleApp(t)
	if _, err := a.FindClinic("not a clinic address"); err == nil {
		t.Fatal("accepted an invalid clinic address")
	}
	if err := a.ConnectClient("not a clinic address"); err == nil {
		t.Fatal("accepted an invalid clinic address")
	}
	if _, err := os.Stat(a.configPath()); !os.IsNotExist(err) {
		t.Fatalf("looking for a clinic saved settings: %v", err)
	}
	if a.loadConfig() != (Config{}) {
		t.Fatalf("looking for a clinic recorded a role: %+v", a.loadConfig())
	}
	if err := a.clientRoleAvailable(); err != nil {
		t.Fatalf("a computer with no role cannot connect: %v", err)
	}
}

func TestBeginServerSetupRecordsTheServerRole(t *testing.T) {
	a := roleApp(t)
	if err := a.BeginServerSetup(); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(a.configPath())
	if err != nil || cfg.Role != roleServer {
		t.Fatalf("setup did not record the server role: %+v, %v", cfg, err)
	}
	if err := a.BeginServerSetup(); err != nil {
		t.Fatalf("re-entering setup should be idempotent: %v", err)
	}
	if err := a.clientRoleAvailable(); err == nil || err.Error() != unfinishedServerSetup {
		t.Fatalf("an unfinished clinic setup allowed a client connection: %v", err)
	}
	if err := a.ConnectClient("care.local"); err == nil || err.Error() != unfinishedServerSetup {
		t.Fatalf("connect bypassed the unfinished clinic setup: %v", err)
	}
	if _, err := a.FindClinic("care.local"); err == nil || err.Error() != unfinishedServerSetup {
		t.Fatalf("find bypassed the unfinished clinic setup: %v", err)
	}
}

func TestInstalledFilesBlockConnectingWithoutASavedRole(t *testing.T) {
	a := roleApp(t)
	if err := os.MkdirAll(a.installDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.installDir(), "earlier-installation"), []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.ConnectClient("care.local"); err == nil || err.Error() != unfinishedServerSetup {
		t.Fatalf("a half-installed clinic accepted a client connection: %v", err)
	}
	if a.loadConfig() != (Config{}) {
		t.Fatalf("the refused connection recorded a role: %+v", a.loadConfig())
	}
}

func TestDisconnectUnownedCertificateResetsRole(t *testing.T) {
	a := roleApp(t)
	// Invalid PEM would fail certificate removal if the unowned root were touched.
	cfg := Config{Role: roleClient, ClientURL: "https://clinic.local", ClientCertificate: "unowned certificate"}
	if err := a.saveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if err := a.DisconnectClient(); err != nil {
		t.Fatal(err)
	}
	saved, err := loadConfig(a.configPath())
	if want := (Config{}); err != nil || saved != want || a.loadConfig() != want {
		t.Fatalf("disconnect did not clear the connection and role: %+v, %v", saved, err)
	}
	if err := a.selectRole(roleServer); err != nil {
		t.Fatalf("completed uninstall did not allow a new role: %v", err)
	}
}

func TestClientCannotChangeClinicBeforeDisconnecting(t *testing.T) {
	a := roleApp(t)
	cfg := Config{Role: roleClient, ClientURL: "https://first.local", ClientCertificate: "pinned certificate"}
	if err := a.saveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if err := a.ConnectClient("second.local"); err == nil || !strings.Contains(err.Error(), "remove") {
		t.Fatalf("changed clinic without removing its access: %v", err)
	}
	if a.loadConfig() != cfg {
		t.Fatal("rejected clinic change discarded the saved certificate")
	}
}

func TestDisconnectSaveFailureRetainsClientRetryState(t *testing.T) {
	a := roleApp(t)
	cfg := Config{Role: roleClient, ClientURL: "https://clinic.local", ClientCertificate: "unowned certificate"}
	a.cfg = cfg
	if err := os.MkdirAll(a.configPath(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := a.DisconnectClient(); err == nil {
		t.Fatal("reported successful disconnect without saving it")
	}
	if a.loadConfig() != cfg {
		t.Fatal("failed disconnect lost the connection retry state")
	}
}

func TestDisconnectCertificateFailureKeepsRole(t *testing.T) {
	a := roleApp(t)
	cfg := Config{Role: roleClient, ClientURL: "https://clinic.local",
		ClientCertificate: "invalid certificate", ClientCertificateOwned: true}
	if err := a.saveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if err := a.DisconnectClient(); err == nil || a.loadConfig() != cfg {
		t.Fatalf("failed certificate removal reset client settings: %v", err)
	}
	if err := a.selectRole(roleServer); err == nil {
		t.Fatal("incomplete uninstall allowed a role change")
	}
}

func TestNewAppKeepsFreshAndClientInstallsOutOfServerFlow(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "XDG_CONFIG_HOME", "XDG_STATE_HOME"} {
		t.Setenv(name, root)
	}
	t.Setenv("PATH", os.Getenv("PATH"))
	t.Setenv("SHELL", filepath.Join(root, "no-login-shell"))
	files := fstest.MapFS{
		"install/.env": {Data: []byte(`CARE_CLINIC_VERSION=0.0.0-dev
POSTGRES_IMAGE=postgres:test
REDIS_IMAGE=redis:test
MINIO_IMAGE=minio:test
CADDY_IMAGE=caddy:test
CORAZA_VERSION=test
BACKUP_IMAGE=backup:test
CADDY_WAF_IMAGE=waf:test
BACKEND_IMAGE=backend:test
FRONTEND_IMAGE=frontend:test
CARE_BE_REPO=backend
CARE_FE_REPO=frontend
CARE_BE_REF=main
CARE_FE_REF=main
`)},
		"install/docker-compose.yml": {Data: []byte("must not be installed\n")},
	}
	path, err := configPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "application.log"), []byte("normal startup"), 0o600); err != nil {
		t.Fatal(err)
	}
	create := func() *App {
		t.Helper()
		a, err := NewApp(files, nil)
		if err != nil {
			t.Fatal(err)
		}
		a.startup(context.Background())
		a.shutdown(context.Background())
		if _, err := os.Stat(a.installDir()); !os.IsNotExist(err) {
			t.Fatalf("non-server startup created installation files: %v", err)
		}
		return a
	}

	a := create()
	if cfg := a.loadConfig(); cfg.Role != "" || cfg.MDNSName != "" {
		t.Fatalf("fresh app or its log was mistaken for a server: %+v", cfg)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("fresh startup unexpectedly saved settings: %v", err)
	}
	if err := a.selectRole(roleClient); err != nil {
		t.Fatal(err)
	}
	a = create()
	if a.loadConfig().Role != roleClient {
		t.Fatal("client selection was lost after restart")
	}
	cfg := a.loadConfig()
	cfg.ClientURL = "https://clinic.local"
	cfg.ClientCertificate = "public certificate"
	cfg.ClientCertificateOwned = true
	if err := a.saveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	a = create()
	state, err := a.GetState()
	if err != nil || state.Role != roleClient || state.ClientURL != cfg.ClientURL {
		t.Fatalf("saved client returned to server startup: %+v, %v", state, err)
	}
	if a.loadConfig() != cfg {
		t.Fatal("client restart lost connection ownership")
	}
}

func TestChangeAdminPassword(t *testing.T) {
	a := settingsApp(t)
	a.cfg.Role = roleServer
	if err := a.ChangeAdminPassword("wrong", "AnotherPassword456"); err == nil {
		t.Fatal("changed with the wrong password")
	}
	if err := a.ChangeAdminPassword(settingsPassword, "AnotherPassword456"); err != nil {
		t.Fatal(err)
	}
	if !a.VerifyAdminPassword("AnotherPassword456") || a.VerifyAdminPassword(settingsPassword) {
		t.Fatal("password not replaced")
	}
}
