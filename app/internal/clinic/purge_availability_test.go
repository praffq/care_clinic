package clinic

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/release"
	"github.com/ohcnetwork/care_desktop/app/internal/vm"
)

func TestPurgeCannotSkipInspectionOfManagedDiskWhenCLIIsMissing(t *testing.T) {
	if !vm.Supported() {
		t.Skip("managed VM absence policy does not apply to native Linux")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("LOCALAPPDATA", home)
	t.Setenv("PATH", filepath.Join(home, "empty"))
	if err := os.MkdirAll(vm.StateDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	disk := filepath.Join(vm.StateDir(), "data.img")
	if err := os.WriteFile(disk, []byte("retained clinic disk"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := &Clinic{InstallDir: home, Pins: &release.Pins{}}
	if available, err := e.dockerCLIAvailable(); available || err == nil || !strings.Contains(err.Error(), "retained clinic volumes") {
		t.Fatalf("retained disk treated as safely absent: %v, %v", available, err)
	}
	if err := os.Remove(disk); err != nil {
		t.Fatal(err)
	}
	if available, err := e.dockerCLIAvailable(); available || err != nil {
		t.Fatalf("proved absence still required missing CLI: %v, %v", available, err)
	}
}
