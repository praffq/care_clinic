package backup

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/storage"
)

// PITRLatest restores every change archived so far.
const PITRLatest = "latest"

// RestorePointInTime rebuilds the database from the newest verified full copy
// plus archived change files, up to target (PITRLatest or a moment), inside a
// throwaway PostgreSQL in the stage volume. Only after replay finishes and the
// rebuilt database answers is it dumped and taken through the same journaled
// staged restore and swap as a normal restore. Uploaded files are not changed.
func (s *Store) RestorePointInTime(target, recoveryFile string, now time.Time) error {
	if s.Refresh != nil {
		if err := s.Refresh(); err != nil {
			return fmt.Errorf("the latest backup records could not be read from the CARE engine: %w", err)
		}
	}
	m, found, err := storage.ReadManifest(s.BackupDir)
	if err != nil {
		return err
	}
	if !found || !m.Verified() {
		return errors.New("there is no verified backup to restore from")
	}
	wal, err := storage.ReadWALStatus(filepath.Join(s.Dir, storage.StateDirName))
	if err != nil {
		return fmt.Errorf("the record of saved changes could not be read: %w", err)
	}
	if wal.Gap != "" {
		return fmt.Errorf("change file %s is missing, so changes after it cannot be replayed; restore a backup file instead or wait for the next verified backup", wal.Gap)
	}
	when, err := PITRTarget(target, m, SavedThrough(m, wal), now)
	if err != nil {
		return err
	}
	if recoveryFile == "" {
		return errors.New("select the backup recovery file saved when this clinic was set up")
	}
	key, err := filepath.Abs(recoveryFile)
	if err != nil {
		return err
	}
	if _, err := ReadRecoveryFile(key); err != nil {
		return err
	}
	if info, err := os.Stat(filepath.Join(s.Dir, "scripts", "pitr-stage.sh")); err != nil || !info.Mode().IsRegular() {
		return errors.New("the restore helper scripts are missing from the install; repair CARE first")
	}
	// Replay reads the guest's own backups and change files, and the guest
	// copy of the scripts and keys sent with the clinic deployment.
	return s.stagedRestore(false, func(j *restoreJournal) error {
		return s.stagePointInTime(j, s.agentBackupDir(), path.Join(s.agentDir(), "scripts"), path.Join(s.agentDir(), "keys"), key, when)
	})
}

// SavedThrough is the newest moment replay can reach: the archived
// saved-through time, and never earlier than the end of the full copy.
func SavedThrough(m storage.Manifest, wal storage.WALStatus) int64 {
	latest := m.EarliestRestoreAt()
	switch wal.State {
	case "ok", "stale", "failing":
		latest = max(latest, wal.SavedThrough)
	}
	return latest
}

// PITRTarget validates target against [the full copy's end, savedThrough]
// and returns the form recovery_target_time takes. pitr-stage.sh separately
// refuses to stage a database unless PostgreSQL reports reaching the target.
func PITRTarget(target string, m storage.Manifest, savedThrough int64, now time.Time) (string, error) {
	if target == PITRLatest {
		return PITRLatest, nil
	}
	t, err := time.Parse(time.RFC3339, target)
	if err != nil {
		return "", fmt.Errorf("choose a valid time to restore to: %q", target)
	}
	if earliest := m.EarliestRestoreAt(); t.Unix() < earliest {
		return "", fmt.Errorf("the earliest moment this backup can restore is %s",
			time.Unix(earliest, 0).Local().Format("2 Jan 2006 15:04:05"))
	}
	if t.After(now) {
		return "", errors.New("the time to restore to is in the future")
	}
	if t.Unix() > savedThrough {
		return "", fmt.Errorf("changes are only saved through %s; choose that time or earlier, or the latest saved change",
			time.Unix(savedThrough, 0).Local().Format("2 Jan 2006 15:04:05"))
	}
	return t.UTC().Format("2006-01-02 15:04:05+00"), nil
}

func (s *Store) stagePointInTime(j *restoreJournal, src, scripts, keys, key, target string) error {
	settings, err := s.restoreSettings()
	if err != nil {
		return err
	}
	inputs, err := s.newRestoreInputs()
	if err != nil {
		return err
	}
	defer inputs.remove()
	guestKey, err := inputs.addKey(key)
	if err != nil {
		return err
	}
	s.logln("Rebuilding the database from the full copy and saved changes, away from the running clinic...")
	args := s.restoreHelperArgs(j, "pitr", "none",
		"--mount", restoreMount("volume", j.stageVolume(), "/restore", false),
		"--mount", restoreMount("bind", src, "/backups", true),
		"--mount", restoreMount("bind", scripts, "/scripts", true),
		"--mount", restoreMount("bind", keys, "/keys", true),
		"--mount", restoreMount("bind", guestKey, "/restore-key.pem", true),
		"--shm-size", "256m",
		"-e", "POSTGRES_DB="+settings["POSTGRES_DB"],
		"-e", "POSTGRES_USER="+settings["POSTGRES_USER"])
	args = append(args, s.Image, "sh", "/scripts/pitr-stage.sh", target)
	if err := s.run.AgentRun("run", args...); err != nil {
		return fmt.Errorf("rebuilding the database from saved changes failed: %w", err)
	}
	return nil
}
