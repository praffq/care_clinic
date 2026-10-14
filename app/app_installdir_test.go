package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"testing/fstest"

	"github.com/ohcnetwork/care_desktop/app/internal/release"
)

func installKitApp(t *testing.T, kit fstest.MapFS) *App {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("USERPROFILE", root)
	return &App{configFile: filepath.Join(root, "care-clinic", "config.json"), installFS: kit}
}

func putKitFile(t *testing.T, a *App, rel, data string) {
	t.Helper()
	path := filepath.Join(a.installDir(), filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestKitRefreshRemovesOnlyPreviouslyShippedFiles(t *testing.T) {
	a := installKitApp(t, fstest.MapFS{
		"install/.env":          {Data: []byte("PINS=old")},
		"install/old/nested.sh": {Data: []byte("old script")},
		"install/backend.env":   {Data: []byte("DEFAULT=old")},
	})
	if _, err := a.ensureInstallDir(); err != nil {
		t.Fatal(err)
	}
	retained := []string{"backend.env", "frontend.env", "notes.txt", "keys/backup-cert.pem",
		"backup-state/restore-state.json", "plugin-recovery.json", "plugins.json", "plugins-pending.json", "care-images.json"}
	for _, rel := range retained {
		putKitFile(t, a, rel, "operator data")
	}
	a.installFS = fstest.MapFS{"install/.env": {Data: []byte("PINS=new")}}
	if _, err := a.ensureInstallDir(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(a.installDir(), "old", "nested.sh")); !os.IsNotExist(err) {
		t.Fatalf("obsolete shipped file retained: %v", err)
	}
	for _, rel := range retained {
		if data, err := os.ReadFile(filepath.Join(a.installDir(), filepath.FromSlash(rel))); err != nil || string(data) != "operator data" {
			t.Fatalf("retained file %s changed: %q %v", rel, data, err)
		}
	}
	files, err := readKitInventory(a.installDir())
	if err != nil || len(files) != 1 || files[0] != ".env" {
		t.Fatalf("incorrect final ownership: %v %v", files, err)
	}
}

func TestKitFirstUpgradeDoesNotGuessOldOwnership(t *testing.T) {
	a := installKitApp(t, fstest.MapFS{"install/new.txt": {Data: []byte("new")}})
	putKitFile(t, a, "old-shipped-or-user.txt", "unknown owner")
	if _, err := a.ensureInstallDir(); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(a.installDir(), "old-shipped-or-user.txt")); err != nil || string(data) != "unknown owner" {
		t.Fatalf("first upgrade deleted unknown ownership: %q %v", data, err)
	}
}

func TestKitCopyReplacesInodeAndSetsModes(t *testing.T) {
	a := installKitApp(t, fstest.MapFS{
		"install/file.txt":    {Data: []byte("complete new file")},
		"install/script.sh":   {Data: []byte("#!/bin/sh\n")},
		"install/backend.env": {Data: []byte("SECRET=initial")},
	})
	putKitFile(t, a, "file.txt", "previous complete file")
	alias := filepath.Join(t.TempDir(), "old-inode")
	if err := os.Link(filepath.Join(a.installDir(), "file.txt"), alias); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	if _, err := a.ensureInstallDir(); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(alias); err != nil || string(data) != "previous complete file" {
		t.Fatalf("copy truncated existing inode rather than atomically replacing: %q %v", data, err)
	}
	if data, _ := os.ReadFile(filepath.Join(a.installDir(), "file.txt")); string(data) != "complete new file" {
		t.Fatalf("incomplete new payload: %q", data)
	}
	for rel, mode := range map[string]os.FileMode{"file.txt": 0o644, "script.sh": 0o755, "backend.env": 0o600, kitInventoryFile: 0o600} {
		info, err := os.Stat(filepath.Join(a.installDir(), rel))
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != mode {
			t.Fatalf("%s mode %o, want %o", rel, info.Mode().Perm(), mode)
		}
	}
}

type failingKitRead struct{ fs.FS }

func (f failingKitRead) ReadFile(name string) ([]byte, error) {
	if name == "install/z-failure.txt" {
		return nil, errors.New("injected kit read failure")
	}
	return fs.ReadFile(f.FS, name)
}

func TestKitPartialCopyDoesNotPruneAndRetryCompletes(t *testing.T) {
	a := installKitApp(t, fstest.MapFS{"install/obsolete.txt": {Data: []byte("old")}})
	if _, err := a.ensureInstallDir(); err != nil {
		t.Fatal(err)
	}
	next := fstest.MapFS{
		"install/a-first.txt":   {Data: []byte("new")},
		"install/z-failure.txt": {Data: []byte("complete")},
	}
	a.installFS = failingKitRead{next}
	if _, err := a.ensureInstallDir(); err == nil {
		t.Fatal("copy failure hidden")
	}
	if data, err := os.ReadFile(filepath.Join(a.installDir(), "obsolete.txt")); err != nil || string(data) != "old" {
		t.Fatal("obsolete files pruned before copies completed")
	}
	a.installFS = next
	if _, err := a.ensureInstallDir(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(a.installDir(), "obsolete.txt")); !os.IsNotExist(err) {
		t.Fatal("retry lost obsolete ownership")
	}
}

func TestKitPendingCleanupInventoryRetainsProtectedFiles(t *testing.T) {
	a := installKitApp(t, fstest.MapFS{"install/new.txt": {Data: []byte("new")}})
	pending := map[string]bool{"gone.txt": true, "already-removed.txt": true, "backend.env": true,
		"keys/secret.pem": true, "backup-state/restore-state.json": true, "plugin-recovery.json": true}
	for rel := range pending {
		if rel != "already-removed.txt" {
			putKitFile(t, a, rel, "retain unless owned kit")
		}
	}
	if err := writeKitInventory(a.installDir(), pending); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ensureInstallDir(); err != nil {
		t.Fatal(err)
	}
	for rel := range pending {
		_, err := os.Stat(filepath.Join(a.installDir(), filepath.FromSlash(rel)))
		if protectedKitPath(rel) && err != nil {
			t.Fatalf("protected artifact removed: %s %v", rel, err)
		}
		if !protectedKitPath(rel) && !os.IsNotExist(err) {
			t.Fatalf("interrupted cleanup not completed: %s %v", rel, err)
		}
	}
}

func TestKitRejectsUnsafePathsAndNonFileObsoleteTargets(t *testing.T) {
	for _, rel := range []string{"../outside", "/absolute", `C:\outside`, "nested/../outside", ".", "backend.env.", "keys /secret"} {
		t.Run(rel, func(t *testing.T) {
			a := installKitApp(t, fstest.MapFS{"install/new.txt": {Data: []byte("new")}})
			data, err := json.Marshal(kitInventory{Version: 1, Files: []string{rel}})
			if err != nil {
				t.Fatal(err)
			}
			putKitFile(t, a, kitInventoryFile, string(data))
			if _, err := a.ensureInstallDir(); err == nil {
				t.Fatal("unsafe inventory accepted")
			}
			if _, err := os.Stat(filepath.Join(a.installDir(), "new.txt")); !os.IsNotExist(err) {
				t.Fatal("payload modified before inventory validation")
			}
		})
	}
	a := installKitApp(t, fstest.MapFS{"install/new.txt": {Data: []byte("new")}})
	if err := writeKitInventory(a.installDir(), map[string]bool{"obsolete": true}); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(a.installDir(), "obsolete"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ensureInstallDir(); err == nil {
		t.Fatal("obsolete directory was treated as removable file")
	}
}

func TestKitFailureAfterCleanupKeepsRetryInventory(t *testing.T) {
	a := installKitApp(t, fstest.MapFS{"install/obsolete.txt": {Data: []byte("old")}})
	if _, err := a.ensureInstallDir(); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(a.installDir(), backupStateDir)
	if err := os.Remove(state); err != nil {
		t.Fatal(err)
	}
	putKitFile(t, a, backupStateDir, "blocking directory creation")
	a.installFS = fstest.MapFS{"install/current.txt": {Data: []byte("current")}}
	if _, err := a.ensureInstallDir(); err == nil {
		t.Fatal("post-cleanup failure hidden")
	}
	previous, err := readKitInventory(a.installDir())
	if err != nil || len(previous) != 2 {
		t.Fatalf("partial cleanup lost retry inventory: %v %v", previous, err)
	}
	if err := os.Remove(state); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ensureInstallDir(); err != nil {
		t.Fatal(err)
	}
	previous, err = readKitInventory(a.installDir())
	if err != nil || len(previous) != 1 || previous[0] != "current.txt" {
		t.Fatalf("retry did not finalize inventory: %v %v", previous, err)
	}
}

func TestKitCorruptInventoryAndReservedPayloadFailBeforeCopy(t *testing.T) {
	for _, data := range []string{"{", "null", `{"version":2,"files":[]}`} {
		a := installKitApp(t, fstest.MapFS{"install/new.txt": {Data: []byte("new")}})
		putKitFile(t, a, kitInventoryFile, data)
		if _, err := a.ensureInstallDir(); err == nil {
			t.Fatalf("invalid inventory ignored: %q", data)
		}
		if _, err := os.Stat(filepath.Join(a.installDir(), "new.txt")); !os.IsNotExist(err) {
			t.Fatal("invalid inventory allowed copies")
		}
	}
	for _, rel := range []string{"keys/secret.pem", "backup-state/restore-state.json", "plugin-recovery.json"} {
		a := installKitApp(t, fstest.MapFS{"install/" + rel: {Data: []byte("not kit material")}})
		if _, err := a.ensureInstallDir(); err == nil {
			t.Fatalf("reserved payload accepted: %s", rel)
		}
	}
}

func TestKitStartupStillHonorsRecoveryGates(t *testing.T) {
	for _, rel := range []string{"plugin-recovery.json", "restore-state.json"} {
		t.Run(rel, func(t *testing.T) {
			a := roleApp(t)
			a.cfg = Config{Role: roleServer, SetupDone: true}
			a.installFS = fstest.MapFS{"install/file.txt": {Data: []byte("replacement")}}
			putKitFile(t, a, "file.txt", "retain for recovery")
			putKitFile(t, a, rel, "{")
			a.refreshInstallDir()
			if data, err := os.ReadFile(filepath.Join(a.installDir(), "file.txt")); err != nil || string(data) != "retain for recovery" {
				t.Fatalf("startup refreshed kit through recovery error: %q %v", data, err)
			}
		})
	}
}

func TestKitRejectsSymlinkCopyAndCleanup(t *testing.T) {
	for _, rel := range []string{"file.txt", "nested", kitInventoryFile, "obsolete.txt"} {
		t.Run(rel, func(t *testing.T) {
			a := installKitApp(t, fstest.MapFS{
				"install/file.txt":         {Data: []byte("new")},
				"install/nested/script.sh": {Data: []byte("new")},
			})
			if err := writeKitInventory(a.installDir(), map[string]bool{"obsolete.txt": true}); err != nil {
				t.Fatal(err)
			}
			outside := t.TempDir()
			target := filepath.Join(a.installDir(), rel)
			if rel == kitInventoryFile {
				if err := os.Remove(target); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(outside, target); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			if _, err := a.ensureInstallDir(); err == nil {
				t.Fatal("symlink target accepted")
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 0 {
				t.Fatal("refresh modified data outside installation")
			}
		})
	}
}

func TestKitRejectsLinkedInstallationRootAndParent(t *testing.T) {
	for _, parent := range []bool{false, true} {
		a := installKitApp(t, fstest.MapFS{"install/file.txt": {Data: []byte("new")}})
		outside := t.TempDir()
		target := a.installDir()
		if parent {
			target = filepath.Dir(target)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, target); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, err := a.ensureInstallDir(); err == nil {
			t.Fatal("linked installation root/parent accepted")
		}
		entries, err := os.ReadDir(outside)
		if err != nil || len(entries) != 0 {
			t.Fatal("refresh escaped through installation link")
		}
	}
}

func TestEnsureInstallDirKeepsSettings(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("USERPROFILE", root)
	a := &App{
		configFile: filepath.Join(root, "care-clinic", "config.json"),
		pins:       &release.Pins{},
		installFS: fstest.MapFS{
			"install/.gitkeep":          {Data: []byte("")},
			"install/backend.env":       {Data: []byte("NEW=1\n")},
			"install/scripts/backup.sh": {Data: []byte("#!/bin/sh\n")},
		},
	}
	dest := a.installDir()
	for name, data := range map[string]string{
		"backend.env":  "USER_EDITED=1\n",
		"plugins.json": "[]\n",
	} {
		path := filepath.Join(dest, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := a.ensureInstallDir(); err != nil {
		t.Fatal(err)
	}

	if data, _ := os.ReadFile(filepath.Join(dest, "backend.env")); string(data) != "USER_EDITED=1\n" {
		t.Fatalf("operator settings were overwritten: %q", data)
	}
	if data, _ := os.ReadFile(filepath.Join(dest, "plugins.json")); string(data) != "[]\n" {
		t.Fatalf("operator plugin choices were overwritten: %q", data)
	}
	if info, err := os.Stat(filepath.Join(dest, "scripts", "backup.sh")); err != nil || info.Mode()&0o111 == 0 {
		t.Fatalf("shell script lost its execute bit: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, ".gitkeep")); !os.IsNotExist(err) {
		t.Fatal("placeholder was installed")
	}
}
