package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/envconfig"
	"github.com/ohcnetwork/care_desktop/app/internal/release"
	"golang.org/x/crypto/bcrypt"
)

const settingsPassword = "ClinicPassword123"

func settingsApp(t *testing.T) *App {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("USERPROFILE", root)
	hash, err := bcrypt.GenerateFromPassword([]byte(settingsPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{
		configFile: filepath.Join(root, "care-clinic", "config.json"),
		cfg:        Config{SetupDone: true, AdminPwHash: string(hash)},
		pins:       &release.Pins{},
	}
	if err := os.MkdirAll(a.installDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"docker-compose.yml":     "name: care-clinic\n",
		"backend.env":            "JWT_REFRESH_TOKEN_LIFETIME=14\nADDITIONAL_PLUGS=[]\n",
		"frontend.env":           "REACT_CARE_API_URL=https://care.local\n",
		"backend.overrides.env":  "JWT_REFRESH_TOKEN_LIFETIME=14\nADDITIONAL_PLUGS=[]\n",
		"frontend.overrides.env": "REACT_APP_TITLE=Clinic\n",
	} {
		if err := os.WriteFile(filepath.Join(a.installDir(), name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	defaults, err := os.ReadFile("../deployments/env-defaults.yml")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.installDir(), "env-defaults.yml"), defaults, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := envconfig.EnsureSecrets(a.installDir()); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestSettingsReadsCanRunConcurrently(t *testing.T) {
	a := settingsApp(t)
	a.jobMu.RLock()
	defer a.jobMu.RUnlock()

	reads := []func() error{
		func() error {
			text, err := a.ReadEnv("backend", settingsPassword)
			if err != nil {
				return err
			}
			if text != "JWT_REFRESH_TOKEN_LIFETIME=14\nADDITIONAL_PLUGS=[]\n" {
				return fmt.Errorf("backend settings were not loaded from their file")
			}
			return nil
		},
		func() error {
			text, err := a.ReadEnv("frontend", settingsPassword)
			if err != nil {
				return err
			}
			if text != "REACT_APP_TITLE=Clinic\n" {
				return fmt.Errorf("frontend settings were not loaded from their file")
			}

			return nil
		},
		func() error {
			list, err := a.ReadPlugins()
			if err != nil {
				return err
			}
			if list == nil || len(list) != 0 {
				return fmt.Errorf("plugins were not loaded from backend.env")
			}
			return nil
		},
	}
	results := make(chan error, len(reads))
	for _, read := range reads {
		go func() { results <- read() }()
	}
	for range reads {
		if err := <-results; err != nil {
			t.Errorf("concurrent settings read failed: %v", err)
		}
	}
	if err := a.WriteEnv("backend", "JWT_REFRESH_TOKEN_LIFETIME=0\n", settingsPassword); err == nil {
		t.Fatal("a write bypassed the active readers")
	}
}

func TestSettingsReadsRespectMutationsAndClosing(t *testing.T) {
	a := settingsApp(t)
	a.jobMu.Lock()
	text, envErr := a.ReadEnv("backend", settingsPassword)
	list, pluginErr := a.ReadPlugins()
	a.jobMu.Unlock()
	for _, err := range []error{envErr, pluginErr} {
		if err == nil || !strings.Contains(err.Error(), "still running") {
			t.Fatalf("a read bypassed a mutation: %v", err)
		}
	}
	if text != "" || list != nil {
		t.Fatal("a rejected read returned settings")
	}
	if _, err := a.ReadEnv("backend", settingsPassword); err != nil {
		t.Fatalf("finished work left settings blocked: %v", err)
	}
	a.jobMu.Lock()
	a.closing = true
	a.jobMu.Unlock()
	if _, err := a.ReadEnv("backend", settingsPassword); err == nil || !strings.Contains(err.Error(), "closing") {
		t.Fatalf("settings were read while closing: %v", err)
	}
}

func TestSettingsReadsKeepAuthorizationAndLifecycleGuards(t *testing.T) {
	for _, state := range []string{"wrong password", "not installed", "removing"} {
		t.Run(state, func(t *testing.T) {
			a := settingsApp(t)
			password := settingsPassword
			switch state {
			case "wrong password":
				password = "wrong"
			case "not installed":
				a.cfg.SetupDone = false
			case "removing":
				a.cfg.Removing = true
			}
			if text, err := a.ReadEnv("backend", password); err == nil || text != "" {
				t.Fatalf("settings read bypassed its guard: %v", err)
			}
			if state != "wrong password" {
				if list, err := a.ReadPlugins(); err == nil || list != nil {
					t.Fatalf("plugin read bypassed its guard: %v", err)
				}
			}
		})
	}
}

func TestOverrideReadUsesSavedValue(t *testing.T) {
	a := settingsApp(t)
	for _, days := range []string{"14", "37", "5"} {
		want := "JWT_REFRESH_TOKEN_LIFETIME=" + days + "\nADDITIONAL_PLUGS=[]\n"
		if err := a.WriteEnv("backend", want, settingsPassword); err != nil {
			t.Fatal(err)
		}
		got, err := a.ReadEnv("backend", settingsPassword)
		if err != nil || got != want {
			t.Fatalf("saved override was not loaded: %q, %v", got, err)
		}
	}
}

func TestWriteEnvCannotChangePluginsOutsidePluginTransaction(t *testing.T) {
	a := settingsApp(t)
	before, err := os.ReadFile(filepath.Join(a.installDir(), "backend.env"))
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(string(before), "ADDITIONAL_PLUGS=[]", `ADDITIONAL_PLUGS='[{"name":"one","package_name":"pkg_one"}]'`, 1)
	if changed == string(before) {
		t.Fatal("test did not change ADDITIONAL_PLUGS")
	}
	if err := a.WriteEnv("backend", changed, settingsPassword); err == nil || !strings.Contains(err.Error(), "Plugins settings") {
		t.Fatalf("direct plugin edit was not rejected: %v", err)
	}
	after, err := os.ReadFile(filepath.Join(a.installDir(), "backend.env"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("rejected plugin edit changed backend.env")
	}

	otherChange := strings.Replace(string(before), "JWT_REFRESH_TOKEN_LIFETIME=14", "JWT_REFRESH_TOKEN_LIFETIME=30", 1)
	if err := a.WriteEnv("backend", otherChange, settingsPassword); err != nil {
		t.Fatalf("an unrelated backend setting could not be changed with plugins preserved: %v", err)
	}
}

func TestEnvEditorNeverExposesGeneratedSecrets(t *testing.T) {
	a := settingsApp(t)
	if err := envconfig.Generate(a.installDir(), "care.local"); err != nil {
		t.Fatal(err)
	}
	secrets, err := envconfig.Read(filepath.Join(a.installDir(), "secrets.env"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"backend", "frontend"} {
		for _, read := range []func(string, string) (string, error){a.ReadEnv, a.ReadEnvDefaults} {
			text, err := read(name, settingsPassword)
			if err != nil {
				t.Fatal(err)
			}
			for key, value := range secrets {
				if strings.Contains(text, key) || strings.Contains(text, value) {
					t.Fatal("editor exposed generated credentials")
				}
			}
			if strings.Contains(text, "DATABASE_URL") || strings.Contains(text, "REACT_CARE_API_URL") {
				t.Fatal("editor exposed derived configuration")
			}
		}
	}
	for _, key := range []string{"DJANGO_SECRET_KEY", "DATABASE_URL", "POSTGRES_PASSWORD", "BUCKET_SECRET"} {
		if err := a.WriteEnv("backend", key+"=bad\n", settingsPassword); err == nil {
			t.Fatalf("editor could override %s", key)
		}
	}
	if _, err := a.ReadEnv("secrets", settingsPassword); err == nil {
		t.Fatal("editor could request secrets.env")
	}
}

func TestEnvEditorPersistsEqualDefaultEmptyAndReset(t *testing.T) {
	a := settingsApp(t)
	for _, tc := range []struct{ content, want string }{
		{"REACT_MFE_REGISTERED_COMPONENTS=UserDashboard\n", "UserDashboard"},
		{"REACT_MFE_REGISTERED_COMPONENTS=\n", ""},
		{`REACT_MFE_REGISTERED_COMPONENTS="O'Connor $$HOME \"quoted\""` + "\n", `O'Connor $HOME "quoted"`},
		{"", "UserDashboard"},
	} {
		if err := a.WriteEnv("frontend", tc.content, settingsPassword); err != nil {
			t.Fatal(err)
		}
		if got, err := a.ReadEnv("frontend", settingsPassword); err != nil || got != tc.content {
			t.Fatalf("override not preserved exactly: %q %v", got, err)
		}
		effective, err := envconfig.Read(filepath.Join(a.installDir(), "frontend.env"))
		if err != nil || effective["REACT_MFE_REGISTERED_COMPONENTS"] != tc.want {
			t.Fatalf("effective value differs from override/default: %v", err)
		}
	}
}

func TestFailedGenerationRestoresPreviousOverrides(t *testing.T) {
	a := settingsApp(t)
	before, err := a.ReadEnv("backend", settingsPassword)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.WriteEnv("backend", before+"POSTGRES_HOST=\n", settingsPassword); err == nil || !strings.Contains(err.Error(), "previous overrides were restored") {
		t.Fatalf("invalid derived database setting was saved: %v", err)
	}
	after, err := a.ReadEnv("backend", settingsPassword)
	if err != nil || after != before {
		t.Fatalf("failed generation changed persistent overrides: %v", err)
	}
}
