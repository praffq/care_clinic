package storage

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Files written by the backup sidecar (deployments/scripts/backup.sh) and the
// app's optional copier. ManifestFile lives in the backup folder; the others in
// the install's backup-state folder.
const (
	ManifestFile   = "manifest.json"
	WALStatusFile  = "wal-status"
	CopyStatusFile = "copy-status.json"
	// StateDirName is the install's backup-state folder (/state in the sidecar).
	StateDirName = "backup-state"
)

// Checks every published backup set must have passed; the sidecar records a
// manifest only after all of them succeed.
var RequiredChecks = []string{"checksums", "decrypt_test", "pg_restore_list", "pg_verifybackup", "pg_waldump", "wal_chain"}

type ManifestFileEntry struct {
	Path        string `json:"path"`
	SHA256      string `json:"sha256"`
	PlainSHA256 string `json:"plain_sha256"`
	Bytes       int64  `json:"bytes"`
}

type TrialRestore struct {
	State   string `json:"state"`
	At      int64  `json:"at"`
	Message string `json:"message"`
}

type Manifest struct {
	Version           int    `json:"version"`
	Set               string `json:"set"`
	CreatedAt         int64  `json:"created_at"`
	StartedAt         int64  `json:"started_at"`
	CertificateSerial string `json:"certificate_serial"`
	Base              struct {
		StartLSN        string `json:"start_lsn"`
		EndLSN          string `json:"end_lsn"`
		EndedAt         int64  `json:"ended_at"`
		StartWAL        string `json:"start_wal"`
		Timeline        int    `json:"timeline"`
		WALSegmentBytes int64  `json:"wal_segment_bytes"`
	} `json:"base"`
	Files []ManifestFileEntry `json:"files"`
	WAL   struct {
		Dir     string `json:"dir"`
		From    string `json:"from"`
		Through string `json:"through"`
	} `json:"wal"`
	Checks       map[string]string `json:"checks"`
	TrialRestore TrialRestore      `json:"trial_restore"`
}

// EarliestRestoreAt is the first moment a point-in-time restore can reach:
// the full copy is only consistent once pg_basebackup finished, so earlier
// targets would fail during replay. Manifests without that time fall back to
// the set's completion time, which is always later and therefore safe.
func (m Manifest) EarliestRestoreAt() int64 {
	if m.Base.EndedAt > 0 && m.Base.EndedAt <= m.CreatedAt {
		return m.Base.EndedAt
	}
	return m.CreatedAt
}

// Verified reports whether every required check passed.
func (m Manifest) Verified() bool {
	if m.Version != 1 || m.Set == "" || m.CreatedAt <= 0 || len(m.Files) == 0 {
		return false
	}
	for _, c := range RequiredChecks {
		if m.Checks[c] != "ok" {
			return false
		}
	}
	return true
}

var (
	setStamp   = regexpMust(`^\d{8}-\d{6}$`)
	walSegment = regexpMust(`^[0-9A-F]{24}$`)
)

// ReadManifest reads and sanity-checks backups/manifest.json. A missing file
// returns found=false and no error.
func ReadManifest(backupDir string) (Manifest, bool, error) {
	var m Manifest
	b, err := os.ReadFile(filepath.Join(backupDir, ManifestFile))
	if errors.Is(err, os.ErrNotExist) {
		return m, false, nil
	}
	if err != nil {
		return m, false, err
	}
	m, err = ParseManifest(b)
	return m, err == nil, err
}

func ParseManifest(b []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("backup manifest is unreadable: %w", err)
	}
	return m, m.validate()
}

func (m Manifest) validate() error {
	if m.Version != 1 || !setStamp.MatchString(m.Set) {
		return fmt.Errorf("backup manifest has an unknown version or set name")
	}
	if m.WAL.Dir != "" && m.WAL.Dir != "wal" {
		return fmt.Errorf("backup manifest names an unexpected change-file folder")
	}
	for _, s := range []string{m.Base.StartWAL, m.WAL.From, m.WAL.Through} {
		if s != "" && !walSegment.MatchString(s) {
			return fmt.Errorf("backup manifest names an invalid change file %q", s)
		}
	}
	for _, f := range m.Files {
		if !SafeBackupPath(f.Path) || len(f.SHA256) != 64 {
			return fmt.Errorf("backup manifest lists an unsafe file %q", f.Path)
		}
	}
	return nil
}

// SafeBackupPath accepts only the relative names the sidecar publishes, so a
// tampered manifest cannot direct a copy or restore outside the folder.
func SafeBackupPath(p string) bool {
	if p == "" || strings.Contains(p, "\\") || filepath.IsAbs(p) || filepath.Clean(p) != p {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." || strings.HasPrefix(part, ".") {
			return false
		}
	}
	return true
}

type WALStatus struct {
	State          string `json:"state"`
	CheckedAt      int64  `json:"checked_at"`
	SavedThrough   int64  `json:"saved_through"`
	LastArchivedAt int64  `json:"last_archived_at"`
	LastFailedAt   int64  `json:"last_failed_at"`
	Gap            string `json:"gap"`
	Message        string `json:"message"`
}

func ReadWALStatus(stateDir string) (WALStatus, error) {
	var w WALStatus
	f, err := os.Open(filepath.Join(stateDir, WALStatusFile))
	if errors.Is(err, os.ErrNotExist) {
		return w, nil
	}
	if err != nil {
		return w, err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		n, _ := strconv.ParseInt(v, 10, 64)
		switch k {
		case "state":
			w.State = v
		case "checked_at":
			w.CheckedAt = n
		case "saved_through":
			w.SavedThrough = n
		case "last_archived_at":
			w.LastArchivedAt = n
		case "last_failed_at":
			w.LastFailedAt = n
		case "gap":
			w.Gap = v
		case "message":
			w.Message = v
		}
	}
	return w, sc.Err()
}

// CopyStatus is the optional second copy's state, written by the app's
// copier. State is off (no destination chosen), ok, disconnected, copying or
// failed. LastCopiedAt and Set describe the newest fully verified copy.
type CopyStatus struct {
	State        string `json:"state"`
	Dir          string `json:"dir"`
	CheckedAt    int64  `json:"checked_at"`
	LastCopiedAt int64  `json:"last_copied_at"`
	Set          string `json:"set"`
	WALThrough   string `json:"wal_through"`
	Message      string `json:"message"`
}

func ReadCopyStatus(stateDir string) (CopyStatus, error) {
	var c CopyStatus
	b, err := os.ReadFile(filepath.Join(stateDir, CopyStatusFile))
	if errors.Is(err, os.ErrNotExist) {
		return CopyStatus{State: "off"}, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return CopyStatus{State: "failed", Message: "the copy status file is unreadable"}, fmt.Errorf("the copy status file is unreadable: %w", err)
	}
	return c, nil
}

func WriteCopyStatus(stateDir string, c CopyStatus) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	tmp := filepath.Join(stateDir, "."+CopyStatusFile+".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(stateDir, CopyStatusFile))
}

type Alert struct {
	Code    string `json:"code"`
	Level   Level  `json:"level"`
	Message string `json:"message"`
}

// BackupHealth is what the Backups screen shows: the newest verified set, how
// far changes are archived, the trial restore, the optional copy and alerts.
type BackupHealth struct {
	CheckedAt         int64             `json:"checked_at"`
	LastBackupAt      int64             `json:"last_backup_at"`
	EarliestRestoreAt int64             `json:"earliest_restore_at"`
	Set               string            `json:"set"`
	Verified          bool              `json:"verified"`
	Checks            map[string]string `json:"checks"`
	SavedThrough      int64             `json:"saved_through"`
	WAL               WALStatus         `json:"wal"`
	Trial             TrialRestore      `json:"trial"`
	Copy              CopyStatus        `json:"copy"`
	LastRun           BackupRun         `json:"last_run"`
	Alerts            []Alert           `json:"alerts"`
}

// The sidecar refreshes the change-file status every minute; a much older
// report means it could not write status (or is not running).
const StatusStaleAfter = 15 * time.Minute

// Copies older than this while a destination is chosen are flagged.
const CopyStaleAfter = 2 * 24 * time.Hour

// ReadHealth gathers the sidecar's and copier's state. running says whether
// the clinic is up, so a stopped clinic is not blamed for stale archiving.
func ReadHealth(backupDir, stateDir string, running bool, now time.Time) BackupHealth {
	h := BackupHealth{CheckedAt: now.Unix(), Checks: map[string]string{}, Alerts: []Alert{}}
	add := func(code string, level Level, msg string) {
		h.Alerts = append(h.Alerts, Alert{Code: code, Level: level, Message: msg})
	}
	m, found, err := ReadManifest(backupDir)
	switch {
	case err != nil:
		add("manifest_unreadable", LevelCritical, "The backup record could not be read: "+err.Error())
	case found:
		h.Set, h.LastBackupAt, h.EarliestRestoreAt = m.Set, m.CreatedAt, m.EarliestRestoreAt()
		h.Verified = m.Verified()
		for k, v := range m.Checks {
			h.Checks[k] = v
		}
		h.Trial = m.TrialRestore
		if !h.Verified {
			add("unverified", LevelCritical, "The newest backup has not passed every check.")
		}
	}
	if h.Trial.State == "" {
		h.Trial.State = "never"
	}
	var runErr, walErr, copyErr error
	h.LastRun, runErr = ReadBackupRun(stateDir)
	h.WAL, walErr = ReadWALStatus(stateDir)
	h.Copy, copyErr = ReadCopyStatus(stateDir)
	for _, e := range []struct {
		err  error
		what string
	}{{runErr, "backup"}, {walErr, "change-file"}, {copyErr, "copy"}} {
		if e.err != nil {
			add("status_unreadable", LevelCritical, "The "+e.what+" status could not be read: "+e.err.Error())
		}
	}
	if h.WAL.State == "" && running && found {
		add("wal_unknown", LevelLow, "No report yet on saving changes between backups.")
	}

	if !found && err == nil {
		add("no_backup", LevelCritical, "No verified backup exists yet.")
	} else if found && running && now.Sub(time.Unix(m.CreatedAt, 0)) > StaleAfter &&
		(h.LastRun.State != "running" || now.Sub(time.Unix(h.LastRun.At, 0)) >= 6*time.Hour) {
		add("backup_stale", LevelCritical, "No verified backup in over a day.")
	}
	if h.LastRun.State == "failed" {
		msg := "The last backup failed"
		if h.LastRun.Message != "" {
			msg += ": " + h.LastRun.Message
		}
		if found {
			msg += ". The previous verified backup was kept."
		}
		add("backup_failed", LevelCritical, msg)
	}

	if found {
		h.SavedThrough = m.EarliestRestoreAt()
		switch h.WAL.State {
		case "ok", "stale", "failing":
			h.SavedThrough = max(h.SavedThrough, h.WAL.SavedThrough)
		}
	}
	if running && h.WAL.CheckedAt > 0 && now.Sub(time.Unix(h.WAL.CheckedAt, 0)) > StatusStaleAfter {
		add("status_stale", LevelCritical, "The backup service has not updated its status since "+
			time.Unix(h.WAL.CheckedAt, 0).Local().Format("2 Jan 2006 15:04")+"; the status shown may be out of date. Check the backup log.")
	}
	if running {
		switch h.WAL.State {
		case "failing":
			add("wal_failing", LevelCritical, "Changes since the last backup are not being saved: "+h.WAL.Message+".")
		case "stale":
			add("wal_stale", LevelLow, "Saving recent changes is behind: "+h.WAL.Message+".")
		case "off":
			add("wal_off", LevelCritical, "Saving changes between backups is switched off.")
		}
	}
	if h.WAL.Gap != "" {
		add("wal_gap", LevelCritical, fmt.Sprintf("Change file %s is missing; changes after it cannot be restored until the next full backup.", h.WAL.Gap))
	}
	switch h.Trial.State {
	case "failed":
		add("trial_failed", LevelCritical, "The weekly test restore failed: "+h.Trial.Message+".")
	case "skipped":
		add("trial_skipped", LevelLow, "The weekly test restore was skipped: "+h.Trial.Message+".")
	}

	switch h.Copy.State {
	case "disconnected":
		msg := "The backup copy drive is not connected."
		if h.Copy.LastCopiedAt > 0 {
			msg += " Last copied " + time.Unix(h.Copy.LastCopiedAt, 0).Local().Format("2 Jan 2006 15:04") + "."
		} else {
			msg += " Nothing has been copied yet."
		}
		add("copy_missing", LevelLow, msg+" Backups continue on this computer.")
	case "failed":
		add("copy_failed", LevelLow, "Copying the backup failed: "+h.Copy.Message)
	}
	if h.Copy.State != "" && h.Copy.State != "off" && h.Copy.State != "disconnected" && found &&
		h.Copy.Set != m.Set && h.Copy.LastCopiedAt > 0 && now.Sub(time.Unix(h.Copy.LastCopiedAt, 0)) > CopyStaleAfter {
		add("copy_stale", LevelLow, "The backup copy is more than two days old.")
	}
	return h
}

func regexpMust(s string) *regexp.Regexp { return regexp.MustCompile(s) }
