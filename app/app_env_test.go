package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/agent"
	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
	"github.com/ohcnetwork/care_desktop/app/internal/release"
	"github.com/ohcnetwork/care_desktop/app/internal/stack/stacktest"
	"github.com/ohcnetwork/care_desktop/app/internal/updates"
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
		service:    &fakeAPI{Service: fakeService(t)},
	}
	if err := os.MkdirAll(a.installDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"docker-compose.yml": "name: care-clinic\n",
		"backend.env":        "DB_BACKUP_RETENTION_PERIOD=14\nADDITIONAL_PLUGS=[]\n",
		"frontend.env":       "REACT_CARE_API_URL=https://care.local\n",
	} {
		if err := os.WriteFile(filepath.Join(a.installDir(), name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
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
			if text != "DB_BACKUP_RETENTION_PERIOD=14\nADDITIONAL_PLUGS=[]\n" {
				return fmt.Errorf("backend settings were not loaded from their file")
			}
			return nil
		},
		func() error {
			text, err := a.ReadEnv("frontend", settingsPassword)
			if err != nil {
				return err
			}
			if text != "REACT_CARE_API_URL=https://care.local\n" {
				return fmt.Errorf("frontend settings were not loaded from their file")
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
	if err := a.WriteEnv("backend", "DB_BACKUP_RETENTION_PERIOD=0\n", settingsPassword); err == nil {
		t.Fatal("a write bypassed the active readers")
	}
}

func TestSettingsReadsRespectMutationsAndClosing(t *testing.T) {
	a := settingsApp(t)
	a.jobMu.Lock()
	text, err := a.ReadEnv("backend", settingsPassword)
	a.jobMu.Unlock()
	if err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("a read bypassed a mutation: %v", err)
	}
	if text != "" {
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
		})
	}
}

func TestRetentionReadUsesSavedValue(t *testing.T) {
	a := settingsApp(t)
	for _, days := range []string{"14", "37", "0"} {
		want := "DB_BACKUP_RETENTION_PERIOD=" + days + "\n"
		if err := a.WriteEnv("backend", want, settingsPassword); err != nil {
			t.Fatal(err)
		}
		got, err := a.ReadEnv("backend", settingsPassword)
		if err != nil || got != want {
			t.Fatalf("saved retention was not loaded: %q, %v", got, err)
		}
	}
}

// fakeService answers like a running clinic; CARE_FAKE_STATE=stopped
// reports the backend exited.
func fakeService(t *testing.T) *stacktest.Service {
	svc := stacktest.New(t)
	svc.Fake.Handle = func(_ context.Context, op protocol.Op, _ agent.Emitter) (any, error) {
		if _, ok := op.(*protocol.StackStatus); !ok {
			return nil, nil
		}
		backend := "running"
		if os.Getenv("CARE_FAKE_STATE") == "stopped" {
			backend = "exited"
		}
		return protocol.StackStatusResult{Services: []protocol.ServiceState{{Name: protocol.ServiceBackend, State: backend}}}, nil
	}
	return svc
}

func TestFrontendSettingKeysMatch(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("frontend", "src", "screens", "panel", "env-schema.ts"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	start := strings.Index(s, "FRONTEND_SETTING_KEYS = new Set([")
	end := strings.Index(s[start:], "]")
	if start < 0 || end < 0 {
		t.Fatal("no FRONTEND_SETTING_KEYS in env-schema.ts")
	}
	var ts []string
	for _, f := range strings.Split(s[start:start+end], "\"")[1:] {
		if strings.HasPrefix(f, "REACT_") {
			ts = append(ts, f)
		}
	}
	if want := updates.FrontendKeys(); !slices.Equal(ts, want) {
		t.Fatalf("env-schema.ts FRONTEND_SETTING_KEYS = %q\nupdates.FrontendKeys = %q", ts, want)
	}
}

func TestApplySettingsChecksFrontendEnvFirst(t *testing.T) {
	a := settingsApp(t)
	if err := a.ApplySettings([]string{"frontend"}, "wrong"); err == nil {
		t.Fatal("applied with a wrong password")
	}
	if err := a.ApplySettings([]string{"secrets"}, settingsPassword); err == nil {
		t.Fatal("applied an unknown file")
	}
	p := filepath.Join(a.installDir(), "frontend.env")
	for content, want := range map[string]string{
		"REACT_SENTRY_DSN=`id`\n":           "REACT_SENTRY_DSN: invalid value",
		"REACT_APP_TITLE='<b>Clinic</b>'\n": "REACT_APP_TITLE: invalid value",
	} {
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := a.ApplySettings([]string{"frontend"}, settingsPassword); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %s", content, err, want)
		}
	}
	// Managed wiring and blank optional settings are accepted.
	got, err := func() (map[string]string, error) {
		if err := os.WriteFile(p, []byte("REACT_CARE_API_URL=https://other\nREACT_ENABLED_APPS=x\nREACT_DEFAULT_ENCOUNTER_TYPE=\nREACT_PATIENT_REGISTRATION_DEFAULT_GEO_ORG=\nREACT_APP_TITLE=Sunrise\nOTHER=1\n"), 0o600); err != nil {
			return nil, err
		}
		return a.frontendSettings()
	}()
	if err != nil || len(got) != 3 || got["REACT_APP_TITLE"] != "Sunrise" {
		t.Fatalf("frontendSettings = %v, %v", got, err)
	}
}
