package residue

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/sys/proc"
	"github.com/ohcnetwork/care_desktop/app/internal/vm"
)

func TestRetainedManagedDiskAndMissingCLIAreNotClean(t *testing.T) {
	if !vm.Supported() {
		t.Skip("managed VM absence policy does not apply to native Linux")
	}
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("LOCALAPPDATA", root)
	t.Setenv("PATH", filepath.Join(root, "empty"))
	markManagedDisk(t)
	report, err := scan(Options{
		Runner: proc.Runner{Env: os.Environ()}, Project: "care-clinic",
		InstallDir: filepath.Join(root, "missing"),
	}, noSystemTraces)
	if err == nil || report.Clean || !strings.Contains(err.Error(), "retained clinic volumes") {
		t.Fatalf("missing CLI hid retained engine: %+v, %v", report, err)
	}
}
