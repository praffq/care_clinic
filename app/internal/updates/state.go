package updates

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
	"github.com/ohcnetwork/care_desktop/app/internal/stack"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/atomicfile"
)

// State file names in Options.Dir.
const (
	VerifiedFile  = "verified.json"
	InstalledFile = "installed.json"
	JournalFile   = "journal.json"
	HistoryFile   = "history.json"
	lockFile      = "lock"
	downloadsDir  = "downloads"
)

// MaxHistory bounds history.json; older entries are dropped.
const MaxHistory = 200

// Verified is verified.json: the last feed that passed verification, kept
// verbatim with its signature so it can be re-verified when loaded, and the
// highest revision ever accepted (passed back as MinRevision).
type Verified struct {
	HighestRevision uint64    `json:"highest_revision"`
	LastVerifiedAt  time.Time `json:"last_verified_at"`
	ManifestSHA256  string    `json:"manifest_sha256"`
	Manifest        []byte    `json:"manifest,omitempty"`  // exact updates.json bytes
	Signature       []byte    `json:"signature,omitempty"` // exact updates.json.sig bytes
}

// Image is one digest-pinned image. URL is the feed's tarball URL (dev
// feeds); Tarball is its downloaded copy.
type Image struct {
	Role    protocol.ImageRole `json:"role"`
	Repo    string             `json:"repo"`
	Digest  protocol.Digest    `json:"digest"`
	URL     string             `json:"url,omitempty"`
	Tarball string             `json:"tarball,omitempty"`
}

// StackImages converts images for the stack package.
func StackImages(imgs []Image) []stack.Image {
	out := make([]stack.Image, 0, len(imgs))
	for _, i := range imgs {
		out = append(out, stack.Image{Role: i.Role, Repo: i.Repo, Digest: i.Digest, Tarball: i.Tarball})
	}
	return out
}

// Installed is installed.json: what the coordinator last committed.
type Installed struct {
	Desktop   string           `json:"desktop,omitempty"`
	Engine    *InstalledEngine `json:"engine,omitempty"`
	Care      *InstalledCare   `json:"care,omitempty"`
	UpdatedAt time.Time        `json:"updated_at,omitzero"`
}

// InstalledEngine is the active engine slot.
type InstalledEngine struct {
	Version    string `json:"version"`
	DataSchema int    `json:"data_schema"`
	SHA256     string `json:"sha256,omitempty"`
}

// InstalledCare is the running CARE release and every image its stack env
// pins (CARE and support images).
type InstalledCare struct {
	Version    string    `json:"version"`
	ReleasedAt time.Time `json:"released_at"`
	Images     []Image   `json:"images"`
}

// JournalState is the state of an in-flight operation.
type JournalState string

const (
	// StateRunning: a process is executing the operation.
	StateRunning JournalState = "running"
	// StateUncertain: the process stopped mid-operation (crash or
	// cancellation); call Resume or Rollback.
	StateUncertain JournalState = "uncertain"
	// StateRollingBack: the operation failed and is being undone.
	StateRollingBack JournalState = "rolling_back"
	// StateRollbackUncertain: the process stopped during a rollback; Resume
	// or Rollback continues it.
	StateRollbackUncertain JournalState = "rollback_uncertain"
	// StateFailed: the rollback itself failed; the journal is kept until
	// Rollback succeeds.
	StateFailed JournalState = "failed"
)

// Step statuses.
const (
	StepPending = "pending"
	StepRunning = "running"
	StepDone    = "done"
	StepFailed  = "failed"
)

// StepRecord is one journaled step.
type StepRecord struct {
	Name     string    `json:"name"`
	Status   string    `json:"status"`
	Attempts int       `json:"attempts"`
	Error    string    `json:"error,omitempty"`
	At       time.Time `json:"at,omitzero"`
}

// Rollback is what the coordinator needs to undo an operation. Flags are
// set (and saved) before the action they describe starts, so a crash in the
// middle of it is undone too.
type Rollback struct {
	// CARE: the pre-update dump, whether the stack was stopped, the new
	// env written or migrations started, and the images of the env the
	// stack ran before.
	BackupID       string  `json:"backup_id,omitempty"`
	StackStopped   bool    `json:"stack_stopped"`
	EnvWritten     bool    `json:"env_written"`
	DBMigrated     bool    `json:"db_migrated"`
	PreviousImages []Image `json:"previous_images,omitempty"`
	// Engine: the slot to re-activate and whether the new one was
	// activated.
	PreviousEngine  string `json:"previous_engine,omitempty"`
	EngineActivated bool   `json:"engine_activated"`
}

// CareTarget is the CARE release being installed.
type CareTarget struct {
	Version    string    `json:"version"`
	ReleasedAt time.Time `json:"released_at"`
	Images     []Image   `json:"images"`
}

// EngineTarget is the engine release being installed.
type EngineTarget struct {
	Version    string `json:"version"`
	DataSchema int    `json:"data_schema"`
	URL        string `json:"url"`
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
	// WithCare: CARE is installed, so the stack is stopped before the swap
	// and started and health-checked after it.
	WithCare bool `json:"with_care"`
}

// Journal is journal.json, present only while an operation is in flight
// (or its rollback failed).
type Journal struct {
	ID            string        `json:"id"`
	Component     string        `json:"component"` // feed.ComponentCare or feed.ComponentEngine
	From          string        `json:"from,omitempty"`
	To            string        `json:"to"`
	Revision      uint64        `json:"revision"`
	State         JournalState  `json:"state"`
	StartedAt     time.Time     `json:"started_at"`
	UpdatedAt     time.Time     `json:"updated_at"`
	Steps         []StepRecord  `json:"steps"`
	RollbackSteps []StepRecord  `json:"rollback_steps,omitempty"`
	Rollback      Rollback      `json:"rollback"`
	Error         string        `json:"error,omitempty"`
	FailedStep    string        `json:"failed_step,omitempty"`
	Care          *CareTarget   `json:"care,omitempty"`
	Engine        *EngineTarget `json:"engine,omitempty"`
}

// Results recorded in history.
const (
	ResultApplied        = "applied"
	ResultRolledBack     = "rolled_back"
	ResultRollbackFailed = "rollback_failed"
)

// HistoryEntry is one finished (or rollback-failed) operation.
type HistoryEntry struct {
	ID         string    `json:"id"`
	Component  string    `json:"component"`
	From       string    `json:"from,omitempty"`
	To         string    `json:"to"`
	Revision   uint64    `json:"revision"`
	Result     string    `json:"result"`
	Error      string    `json:"error,omitempty"`
	FailedStep string    `json:"failed_step,omitempty"`
	BackupID   string    `json:"backup_id,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
}

// store reads and atomically writes the state files.
type store struct{ dir string }

func (s store) path(name string) string { return filepath.Join(s.dir, name) }

// load decodes name into v; a missing file leaves v untouched and reports
// false.
func (s store) load(name string, v any) (bool, error) {
	data, err := os.ReadFile(s.path(name))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return false, fmt.Errorf("%s: %w", s.path(name), err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("%s: trailing data", s.path(name))
	}
	return true, nil
}

// save writes v with atomicfile (temp file, fsync, rename).
func (s store) save(name string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(s.path(name), append(data, '\n'), 0o600)
}

func (s store) remove(name string) error {
	err := os.Remove(s.path(name))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (s store) verified() (Verified, error) {
	var v Verified
	_, err := s.load(VerifiedFile, &v)
	return v, err
}

func (s store) installed() (Installed, error) {
	var in Installed
	_, err := s.load(InstalledFile, &in)
	return in, err
}

func (s store) journal() (*Journal, error) {
	var j Journal
	ok, err := s.load(JournalFile, &j)
	if !ok || err != nil {
		return nil, err
	}
	return &j, nil
}

func (s store) history() ([]HistoryEntry, error) {
	var h []HistoryEntry
	_, err := s.load(HistoryFile, &h)
	return h, err
}

func (s store) appendHistory(e HistoryEntry) error {
	h, err := s.history()
	if err != nil {
		return err
	}
	h = append(h, e)
	if len(h) > MaxHistory {
		h = h[len(h)-MaxHistory:]
	}
	return s.save(HistoryFile, h)
}
