package clinic

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/careversions"
	"github.com/ohcnetwork/care_desktop/app/internal/compose"
	"github.com/ohcnetwork/care_desktop/app/internal/health"
	"github.com/ohcnetwork/care_desktop/app/internal/updates"
)

type ChannelStatus struct {
	Version         string `json:"version"`
	Backend         string `json:"backend"`
	Frontend        string `json:"frontend"`
	PendingBackend  string `json:"pending_backend"`
	PendingFrontend string `json:"pending_frontend"`
}

// ChannelStatus reports the installed CARE images without network access.
func (e *Clinic) ChannelStatus() ChannelStatus {
	lock := compose.ReadLock(e.InstallDir)
	out := ChannelStatus{
		Version:  e.Pins.CAREVersion,
		Backend:  lock.Backend.Current.Commit,
		Frontend: lock.Frontend.Current.Commit,
	}
	if lock.Backend.Next != nil {
		out.PendingBackend = lock.Backend.Next.Commit
	}
	if lock.Frontend.Next != nil {
		out.PendingFrontend = lock.Frontend.Next.Commit
	}
	return out
}

func (e *Clinic) CheckForUpdate() (compose.Update, error) {
	return e.Builder().PrepareUpdate()
}

func (e *Clinic) CheckForUpdateManifest(m *careversions.Manifest) (compose.Update, error) {
	return e.Builder().PrepareUpdateManifest(m)
}

func (e *Clinic) DeclineUpdate() error { return e.Builder().DeclineUpdate() }

func (e *Clinic) ApplyUpdateWithJournal(j *updates.Journal, checkpoint updates.Checkpoint) error {
	if j == nil || checkpoint == nil {
		return errors.New("CARE updates require a coordinator journal and durable checkpoint")
	}
	if err := e.Backups().RecoverRestore(); err != nil {
		return err
	}
	b := e.Builder()
	waiting := b.Waiting()
	if !waiting.Any() {
		return errors.New("there is no prepared update to apply")
	}
	var backupID string
	if waiting.Backend != "" {
		var err error
		backupID, err = e.backupBeforeUpdate()
		if err != nil {
			return err
		}
		j.Rollback.BackupID = backupID
		if err := checkpoint(); err != nil {
			return err
		}
		if err := e.stopWorkers(); err != nil {
			return err
		}
	}
	j.Rollback.ImagesKept = nil
	if waiting.Backend != "" {
		j.Rollback.ImagesKept = append(j.Rollback.ImagesKept, e.Pins.BackendImage+"-previous")
	}
	if waiting.Frontend != "" {
		j.Rollback.ImagesKept = append(j.Rollback.ImagesKept, e.Pins.FrontendImage+"-previous")
	}
	if err := checkpoint(); err != nil {
		return err
	}
	applied, err := b.ApplyPending()
	if err != nil {
		return err
	}
	if !applied.Any() {
		return errors.New("the prepared update is no longer available - it will be built again on the next check")
	}
	rollback := func(cause error, dbMigrated bool) error {
		j.State, j.Rollback.DBMigrated = updates.RollingBack, dbMigrated
		if err := checkpoint(); err != nil {
			return errors.Join(cause, err)
		}
		if err := e.RollbackUpdate(backupID, dbMigrated); err != nil {
			return fmt.Errorf("%w; CARE rollback also failed: %v", cause, err)
		}
		j.State, j.Rollback.DBMigrated = updates.Running, false
		if err := checkpoint(); err != nil {
			return errors.Join(cause, err)
		}
		return cause
	}
	if applied.Backend != "" {
		if err := e.dc("up", "-d", "--wait", "--wait-timeout", "300", "backend"); err != nil {
			return rollback(fmt.Errorf("the updated backend did not start: %w", err), false)
		}
		e.logln("Applying database migrations...")
		j.Rollback.DBMigrated = true
		if err := checkpoint(); err != nil {
			return err
		}
		if err := e.migrate(); err != nil {
			return rollback(fmt.Errorf("database migration failed: %w", err), true)
		}
	}
	if err := e.dc("up", "-d", "--wait", "--wait-timeout", "300"); err != nil {
		return rollback(err, applied.Backend != "")
	}
	if err := health.Wait(e.Log, 3*time.Minute); err != nil {
		return rollback(fmt.Errorf("updated CARE did not become healthy: %w", err), applied.Backend != "")
	}
	e.logln("CARE is up to date.")
	return nil
}

func (e *Clinic) RollbackUpdate(backupID string, dbMigrated bool) error {
	lock := compose.ReadLock(e.InstallDir)
	if lock.Backend.Previous == nil && lock.Frontend.Previous == nil && !dbMigrated {
		return nil
	}
	var failures []error
	if err := e.Builder().RollbackApplied(); err != nil {
		failures = append(failures, err)
	}
	if dbMigrated {
		if backupID == "" {
			failures = append(failures, errors.New("database migration changed data but the safety backup ID is missing"))
		} else if e.BackupRecoveryPath == "" {
			failures = append(failures, errors.New("database data rollback needs the clinic's verified backup recovery file"))
		} else {
			backups, err := e.Backups().ListBackups()
			if err != nil {
				failures = append(failures, err)
			} else {
				found := false
				for _, b := range backups {
					if b.DBDump != backupID {
						continue
					}
					found = true
					if err := e.Backups().Restore(b.DBDump, b.FilesArchive, e.BackupRecoveryPath); err != nil {
						failures = append(failures, fmt.Errorf("restore pre-update data backup %s: %w", backupID, err))
					}
					break
				}
				if !found {
					failures = append(failures, fmt.Errorf("pre-update data backup %s is unavailable", backupID))
				}
			}
		}
	}
	if len(failures) > 0 {
		return errors.Join(failures...)
	}
	if err := e.dc("up", "-d", "--wait", "--wait-timeout", "300"); err != nil {
		return err
	}
	return health.Wait(e.Log, 3*time.Minute)
}

func (e *Clinic) backupBeforeUpdate() (string, error) {
	if !e.Backups().BackupEncryptionOn() {
		return "", errors.New("this install cannot write encrypted backups, so the update was not applied - " +
			"recover the backup encryption certificate first")
	}
	ts := time.Now().Format("20060102-150405")
	e.logln("Taking a safety backup before the update changes the database...")
	if err := e.run(nil, "docker", "compose", "run", "--rm", "-T", "backup", "once", "pre-update-"+ts); err != nil {
		return "", fmt.Errorf("couldn't take a safety backup, so the update was not applied: %w", err)
	}
	name := "care-pre-update-" + ts + ".dump.enc"
	if info, err := os.Stat(filepath.Join(e.backupDir(), name)); err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		if err != nil {
			return "", fmt.Errorf("the update safety backup was not created at %s: %w", filepath.Join(e.backupDir(), name), err)
		}
		return "", fmt.Errorf("the update safety backup is not a nonempty regular file: %s", filepath.Join(e.backupDir(), name))
	}
	e.logln("Safety backup written: " + filepath.Join(e.backupDir(), name))
	return name, nil
}
