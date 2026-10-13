package clinic

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLinuxBackupDefaultDoesNotFollowLocalizedDesktop(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	config := filepath.Join(home, ".config")
	t.Setenv("XDG_CONFIG_HOME", config)
	if err := os.MkdirAll(config, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, "user-dirs.dirs"), []byte(`XDG_DESKTOP_DIR="$HOME/Bureau"`), 0o600); err != nil {
		t.Fatal(err)
	}
	desktop, err := backupDesktopDir("linux")
	if err != nil || desktop != filepath.Join(home, "Desktop") {
		t.Fatalf("unpinned existing default changed: %s, %v", desktop, err)
	}
	chosen := filepath.Join(home, "Chosen", "care-db-backups")
	e := &Clinic{BackupDir: chosen}
	if got := e.backupDir(); got != chosen {
		t.Fatalf("explicit backup destination changed: %s", got)
	}
}
