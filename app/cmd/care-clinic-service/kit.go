//go:build unix

package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	kitfiles "github.com/ohcnetwork/care_desktop/app/install"
)

// clinicKitFiles are the kit files a clinic edits. The desktop app keeps
// them in its install dir across updates (installUserFiles in
// app_installdir.go); everything else comes from the kit ctl was built with.
var clinicKitFiles = []string{"backend.env", "frontend.env"}

// kitFS resolves the deployment kit once: the --deployments directory, or
// by default the kit built into this binary with the clinic's own
// backend.env and frontend.env from the desktop app's install dir. A kit
// copied to disk earlier is never trusted by default: rendering one that
// predates a service would remove that service from the clinic.
func (f *coordFlags) kitFS() (fs.FS, error) {
	if f.kitResolved {
		return f.kitDir, f.kitErr
	}
	f.kitResolved = true
	f.kitDir, f.kitErr = resolveKit(*f.kit, kitfiles.Kit, appInstallDir, os.Stderr)
	return f.kitDir, f.kitErr
}

// appInstallDir is where the desktop app keeps the clinic's kit files
// (App.installDir).
func appInstallDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "care-clinic", "install"), nil
}

func resolveKit(dir string, embedded func() (fs.FS, error), clinicDir func() (string, error), log io.Writer) (fs.FS, error) {
	if dir != "" {
		if _, err := os.Stat(filepath.Join(dir, "docker-compose.yml")); err != nil {
			return nil, fmt.Errorf("deployment kit: %w (pass --deployments)", err)
		}
		return os.DirFS(dir), nil
	}
	kit, err := embedded()
	if err != nil {
		return nil, fmt.Errorf("%w: build care-clinic-service with app/cmd/care-clinic-service/build.sh, or pass --deployments", err)
	}
	cd, err := clinicDir()
	if err != nil {
		return nil, err
	}
	var used []string
	for _, name := range clinicKitFiles {
		if _, err := os.Stat(filepath.Join(cd, name)); err == nil {
			used = append(used, name)
		}
	}
	msg := "Using the deployment kit built into care-clinic-service"
	if len(used) > 0 {
		msg += fmt.Sprintf(", with the clinic's %s from %s", strings.Join(used, " and "), cd)
	}
	_, _ = fmt.Fprintln(log, msg)
	return overlayKit{kit: kit, dir: os.DirFS(cd), names: used}, nil
}

// overlayKit is kit with names read from dir.
type overlayKit struct {
	kit   fs.FS
	dir   fs.FS
	names []string
}

func (o overlayKit) Open(name string) (fs.File, error) {
	for _, n := range o.names {
		if n == name {
			f, err := o.dir.Open(name)
			if err == nil || !errors.Is(err, fs.ErrNotExist) {
				return f, err
			}
		}
	}
	return o.kit.Open(name)
}

// noKit is the kit of read-only commands when none resolves.
type noKit struct{}

func (noKit) Open(name string) (fs.File, error) {
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}
