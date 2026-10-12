package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/synctest"

	"github.com/ohcnetwork/care_desktop/app/internal/prereq"
)

func TestDockerStatusSharesOnlyInFlightChecks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a := &App{}
		blocked := make(chan struct{})
		results := make(chan prereq.Status, 20)
		var calls atomic.Int32
		failure := prereq.Status{Message: "The CARE engine is not running."}
		check := func() prereq.Status {
			calls.Add(1)
			<-blocked
			return failure
		}
		for range 20 {
			go func() { results <- a.readDockerStatus(check) }()
		}
		synctest.Wait()
		if calls.Load() != 1 {
			t.Fatalf("concurrent status queries ran %d checks", calls.Load())
		}
		close(blocked)
		synctest.Wait()
		for range 20 {
			if result := <-results; result != failure {
				t.Fatalf("readiness failure was changed: %+v", result)
			}
		}
		ready := prereq.Status{OK: true, Message: "Docker ready"}
		if result := a.readDockerStatus(func() prereq.Status {
			calls.Add(1)
			return ready
		}); result != ready || calls.Load() != 2 {
			t.Fatalf("completed readiness was cached: %+v, calls=%d", result, calls.Load())
		}
	})
}

func TestBackupDirRejectsSettingsRootAndDescendants(t *testing.T) {
	for _, layout := range []string{"settings-with-install-child", "Roaming-settings"} {
		t.Run(layout, func(t *testing.T) {
			a := settingsApp(t)
			if layout == "Roaming-settings" {
				a.configFile = filepath.Join(t.TempDir(), "AppData", "Roaming", appDirName, "config.json")
			}
			settings := filepath.Dir(a.configPath())
			if result := a.ValidateBackupDir(a.installDir()); !strings.Contains(result, "choose a location outside "+a.installDir()) {
				t.Fatalf("installation protection was lost: %s", result)
			}
			for _, parent := range []string{settings, filepath.Join(settings, "extra")} {
				result := a.ValidateBackupDir(parent)
				if !strings.Contains(result, "choose a location outside "+settings) {
					t.Fatalf("settings backup destination accepted: %q: %s", parent, result)
				}
			}
			a.cfg.BackupDir = filepath.Join(settings, "care-db-backups")
			if result := a.ValidateBackupDir(""); !strings.Contains(result, "choose a location outside "+settings) {
				t.Fatalf("saved default bypassed settings isolation: %s", result)
			}
		})
	}
}

func TestBackupDirRejectsSettingsSymlinkAlias(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixture requires Windows developer mode or privilege")
	}
	a := settingsApp(t)
	settings := filepath.Dir(a.configPath())
	alias := filepath.Join(t.TempDir(), "settings-alias")
	if err := os.Symlink(settings, alias); err != nil {
		t.Fatal(err)
	}
	if result := a.ValidateBackupDir(filepath.Join(alias, "new-folder")); !strings.Contains(result, "choose a location outside "+settings) {
		t.Fatalf("symlink bypassed settings isolation: %s", result)
	}
}

func TestBackupDirRecoveryIsolationIndependentOfLogFolder(t *testing.T) {
	a := settingsApp(t)
	if a.log.Folder() != "" {
		t.Fatal("fixture must not have a log folder")
	}
	parent := t.TempDir()
	a.cfg.BackupRecoveryPath = filepath.Join(parent, "care-db-backups", "missing-recovery.pem")
	if result := a.ValidateBackupDir(parent); result != "Keep the backup recovery file separate from the backup folder. Choose another folder." {
		t.Fatalf("recovery location was not checked independently: %s", result)
	}
	a.cfg.BackupRecoveryPath = filepath.Join(t.TempDir(), "missing-recovery.pem")
	// A missing recovery file outside the destination is not a placement error.
	result := a.ValidateBackupDir(parent)
	if strings.Contains(result, "Keep the backup recovery file separate") || strings.Contains(result, "choose a location outside") {
		t.Fatalf("separate destination rejected: %s", result)
	}
}

func TestBackupWriteProblemUsesPlatformAppropriateReadOnlyHint(t *testing.T) {
	for _, goos := range []string{"darwin", "windows", "linux"} {
		t.Run(goos, func(t *testing.T) {
			result := backupWriteProblem(&os.PathError{Op: "open", Path: "test", Err: syscall.EROFS}, goos)
			if !strings.Contains(result, "That drive is read-only") || !strings.HasSuffix(result, "Choose a different folder.") {
				t.Fatalf("read-only guidance lost: %s", result)
			}
			if strings.Contains(result, "Mac") != (goos == "darwin") || strings.Contains(result, "NTFS") != (goos == "darwin") {
				t.Fatalf("platform-inaccurate guidance for %s: %s", goos, result)
			}
		})
	}
	if result := backupWriteProblem(fmt.Errorf("write: %w", os.ErrPermission), "linux"); !strings.Contains(result, "isn't allowed") {
		t.Fatalf("permission error changed classification: %s", result)
	}
	if result := backupWriteProblem(fmt.Errorf("disk unavailable"), "windows"); result != "Couldn't write to that folder: disk unavailable" {
		t.Fatalf("other write failure lost detail: %s", result)
	}
}
