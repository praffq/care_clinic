//go:build unix

package main

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	kitfiles "github.com/ohcnetwork/care_desktop/app/install"
)

func TestResolveKit(t *testing.T) {
	embedded := fstest.MapFS{
		"docker-compose.yml": {Data: []byte("services:\n  backup: {}\n")},
		"backend.env":        {Data: []byte("DEFAULT=1\n")},
		"frontend.env":       {Data: []byte("REACT_APP_TITLE=CARE\n")},
	}
	clinic := t.TempDir()
	if err := os.WriteFile(filepath.Join(clinic, "backend.env"), []byte("EDITED=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A stale kit file in the app's install dir is not used.
	if err := os.WriteFile(filepath.Join(clinic, "docker-compose.yml"), []byte("services:\n  db: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	clinicDir := func() (string, error) { return clinic, nil }
	var log bytes.Buffer
	kit, err := resolveKit("", func() (fs.FS, error) { return embedded, nil }, clinicDir, &log)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"docker-compose.yml": "services:\n  backup: {}\n",
		"backend.env":        "EDITED=1\n",
		"frontend.env":       "REACT_APP_TITLE=CARE\n",
	} {
		if b, err := fs.ReadFile(kit, name); err != nil || string(b) != want {
			t.Errorf("%s = %q, %v; want %q", name, b, err, want)
		}
	}
	if got := log.String(); !strings.Contains(got, "built into care-clinic-service") || !strings.Contains(got, "backend.env from "+clinic) || strings.Contains(got, "frontend.env") {
		t.Errorf("log = %q", got)
	}

	// --deployments is used as is, and must be a kit.
	if _, err := resolveKit(t.TempDir(), nil, nil, &log); err == nil || !strings.Contains(err.Error(), "docker-compose.yml") {
		t.Errorf("empty --deployments dir: %v", err)
	}
	dir, err := resolveKit(clinic, nil, nil, &log)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := fs.ReadFile(dir, "docker-compose.yml"); string(b) != "services:\n  db: {}\n" {
		t.Errorf("--deployments compose = %q", b)
	}

	// Not staged at build time.
	if _, err := kitfiles.Kit(); err == nil {
		t.Skip("app/install is staged in this checkout")
	}
	_, err = resolveKit("", kitfiles.Kit, clinicDir, &log)
	if !errors.Is(err, kitfiles.ErrNotStaged) || !strings.Contains(err.Error(), "--deployments") {
		t.Errorf("unstaged kit: %v", err)
	}
}
