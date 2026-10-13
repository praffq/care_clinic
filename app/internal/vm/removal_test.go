package vm

import (
	"os"
	"path/filepath"
	"testing"
)

func TestManagedEngineAbsenceRequiresEveryOwnershipArtifactAbsent(t *testing.T) {
	dir := t.TempDir()
	paths := []string{filepath.Join(dir, "current"), filepath.Join(dir, "data.img"), filepath.Join(dir, "control.sock")}
	if absent, err := managedEngineAbsent(paths); err != nil || !absent {
		t.Fatalf("empty state = %v, %v", absent, err)
	}
	for _, p := range paths {
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if absent, err := managedEngineAbsent(paths); err != nil || absent {
			t.Fatalf("retained artifact %s = %v, %v", p, absent, err)
		}
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(dir, "missing"), paths[1]); err != nil {
		t.Fatal(err)
	}
	if absent, err := managedEngineAbsent(paths); err != nil || absent {
		t.Fatalf("data symlink must not prove absence: %v, %v", absent, err)
	}
}
