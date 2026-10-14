package clinic

import (
	"strconv"
	"strings"
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/storage"
)

func TestWindowsBackupReserveMatchesCriticalClinicSpacePolicy(t *testing.T) {
	got, err := strconv.ParseUint(backupReserveKB("windows"), 10, 64)
	if err != nil || got*1024 != storage.RunningCriticalFree {
		t.Fatalf("guest reserve=%d KiB, want %d bytes: %v", got, storage.RunningCriticalFree, err)
	}
	for _, goos := range []string{"darwin", "linux"} {
		if backupReserveKB(goos) != "0" {
			t.Fatalf("native shared-folder policy changed for %s", goos)
		}
	}
}

func TestWindowsBackupReservesSharedEngineCapacity(t *testing.T) {
	for _, tt := range []struct {
		free int
		code int
	}{
		{6291455, 1},
		{6291456, 0},
	} {
		t.Run(strconv.Itoa(tt.free), func(t *testing.T) {
			f := newScriptFixture(t)
			f.archive(t, seg(4))
			output, err := f.run(t, []string{"BACKUP_MIN_FREE_KB=" + backupReserveKB("windows"),
				"FAKE_FREE_KB=" + strconv.Itoa(tt.free)}, "backup.sh", "now")
			if (err == nil) != (tt.code == 0) {
				t.Fatalf("free=%d: %v\n%s", tt.free, err, output)
			}
			if tt.code != 0 && strings.Contains(readTrace(t, f), "pg_basebackup ") {
				t.Fatalf("backup started below reserve-plus-estimate boundary:\n%s", output)
			}
			if (tt.code == 0) != strings.Contains(output, "SUCCESS") {
				t.Fatalf("backup result not explicit:\n%s", output)
			}
		})
	}
}
