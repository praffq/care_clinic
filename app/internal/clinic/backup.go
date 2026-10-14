package clinic

import (
	"errors"
	"fmt"
	"path"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/storage"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/diskspace"
)

// BackupNow runs a full verified backup cycle in the running sidecar: a new
// full copy, database dump and files archive are written, verified and
// recorded in manifest.json, and only then is the previous set removed.
func (e *Clinic) BackupNow() error {
	if !e.Backups().BackupEncryptionOn() {
		return errors.New("backup encryption is not set up on this install, so a backup " +
			"would be written unencrypted - the backup encryption certificate must be recovered first")
	}
	need := e.manualBackupNeed()
	if err := e.backupRoomFor(need); err != nil {
		return err
	}
	if err := e.dc("exec", "-T", "backup", "sh", "/scripts/backup.sh", "now"); err != nil {
		if roomErr := e.backupRoomFor(need); roomErr != nil {
			return roomErr
		}
		return fmt.Errorf("the backup did not complete and the previous verified backup was kept - CARE must be running to take one (%w)", err)
	}
	if err := e.ExportBackups(e.Runner().Context()); err != nil {
		return fmt.Errorf("the backup was written inside the CARE engine but couldn't be copied to %s: %w", e.backupDir(), err)
	}
	m, found, err := storage.ReadManifest(e.backupDir())
	if err != nil || !found || !m.Verified() {
		return errors.New("the backup finished but no verified backup record was found; the previous backup was kept")
	}
	e.logln("Verified backup " + m.Set + " written to " + e.backupDir())
	return nil
}

func (e *Clinic) manualBackupNeed() uint64 {
	set, found, err := storage.LatestSet(e.backupDir())
	if err != nil {
		return storage.BackupFloor
	}
	return storage.BackupNeed(set, found)
}

func (e *Clinic) UpdateBackupNeed() uint64 {
	return e.manualBackupNeed()
}

func (e *Clinic) backupRoomFor(need uint64) error {
	u, err := diskspace.Of(e.backupDir())
	if err != nil || u.Free >= need {
		return nil
	}
	return fmt.Errorf("not enough space in the backup folder: this backup needs about %s and only %s is free. "+
		"Free up space on that drive, or choose another backup folder on the Backups tab",
		storage.Human(need), storage.Human(u.Free))
}

func (e *Clinic) DockerDiskFree() (free, total uint64, err error) {
	out, err := e.Runner().AgentCaptureIn(15*time.Second, nil, "compose", "exec", "-T", "backup", "df", "-Pk", "/")
	if err != nil {
		return 0, 0, err
	}
	return storage.ParseDF(out)
}

func (e *Clinic) DockerRootDir() (string, error) {
	out, err := e.Runner().AgentCaptureIn(5*time.Second, nil, "info", "--format", "{{.DockerRootDir}}")
	if err != nil {
		return "", err
	}
	if !path.IsAbs(out) {
		return "", fmt.Errorf("docker reported an unusable data directory %q", out)
	}
	return out, nil
}
