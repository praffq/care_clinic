package main

import (
	"archive/zip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSupportLogsIncludeUpdatesHealthAndSecretEnvironmentFiles(t *testing.T) {
	logs, updates, install := t.TempDir(), t.TempDir(), t.TempDir()
	for _, file := range []struct {
		root string
		name string
		data string
	}{
		{logs, "care-log.log", "app log"},
		{logs, "guest-system.log", "engine log"},
		{updates, "journal.json", `{"state":"done"}`},
		{updates, "history.json", `[]`},
		{install, "backend.env", "DB_PASSWORD=backend-secret"},
		{install, "secrets.env", "POSTGRES_PASSWORD=clinic-secret"},
	} {
		if err := os.WriteFile(filepath.Join(file.root, file.name), []byte(file.data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(install, "secrets.env"), filepath.Join(logs, "secret.log")); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "diagnostics.zip")
	if err := writeDiagnostics(logs, updates, install, dest,
		map[string]string{"app_version": "0.1.12"}, map[string]string{"containers": "healthy"}); err != nil {
		t.Fatal(err)
	}
	z, err := zip.OpenReader(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = z.Close() }()
	if len(z.File) != 8 {
		t.Fatalf("unexpected archive entries: %v", z.File)
	}
	found := map[string]string{}
	for _, f := range z.File {
		data, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(data)
		_ = data.Close()
		if err != nil {
			t.Fatal(err)
		}
		found[f.Name] = string(content)
	}
	for _, name := range []string{"versions.json", "health.json", "logs/care-log.log", "logs/guest-system.log",
		"updates/journal.json", "updates/history.json", "env/backend.env", "env/secrets.env"} {
		if _, ok := found[name]; !ok {
			t.Errorf("support bundle is missing %s", name)
		}
	}
	if !strings.Contains(found["env/secrets.env"], "clinic-secret") {
		t.Fatal("secret environment values were not included for support")
	}
	if _, ok := found["logs/secret.log"]; ok {
		t.Fatal("a symbolic link was followed into the archive")
	}
}

func TestDiagnosticsPathTreatsDifferentVolumesAsOutside(t *testing.T) {
	if diagnosticsPathInsideLogDir("", errors.New("paths are on different volumes")) {
		t.Fatal("a relative-path error was treated as a destination inside the logs folder")
	}
	if !diagnosticsPathInsideLogDir(filepath.Join("nested", "export.zip"), nil) {
		t.Fatal("a destination inside the logs folder was allowed")
	}
	if diagnosticsPathInsideLogDir(filepath.Join("..", "export.zip"), nil) {
		t.Fatal("a destination outside the logs folder was rejected")
	}
}
