// Package backups copies backup sets out of the engine VM into the host
// backup directory, and restores them.
//
// The engine's backup container writes complete sets to
// /var/lib/care/backups/<id>/ with a manifest.json of SHA-256s. Every
// Interval, and shortly after local midnight when the nightly set is made,
// the Exporter:
//
//  1. lists the engine's sets (GET /v1/backups);
//  2. streams each file of a complete set into <dir>/<id>.partial/;
//  3. re-reads and verifies every SHA-256, writes manifest.json and renames
//     the folder to <dir>/<id>/;
//  4. records the export in <dir>/export-state.json;
//  5. only then deletes the set in the engine (DELETE /v1/backups/<id>);
//  6. deletes older host sets, so the host always keeps at least one set
//     that was verified after it was copied.
//
// A failed export leaves the set in the engine; the next pass retries it,
// and Status reports "Backup not copied out since ...". The engine keeps at
// most api.MaxPendingBackups sets (backup.sh rotates the oldest out); at
// that many the status is critical.
//
// With a copy folder (a removable drive or network share), every pass also
// copies the newest host set there the same way: <copy>/<id>.partial,
// verify, rename, then delete older sets there. The folder is never
// created; while it is missing the copy is retried every CopyRetry.
package backups

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/client"
	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
	"github.com/ohcnetwork/care_desktop/app/internal/service/api"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/diskspace"
)

// StateFile records the last export in the backup directory.
const StateFile = "export-state.json"

const partialSuffix = ".partial"

// DefaultInterval is how often the exporter looks for new sets.
const DefaultInterval = 5 * time.Minute

// DefaultCopyRetry is how often a copy folder that is missing or failed is
// tried again.
const DefaultCopyRetry = time.Minute

// afterMidnight is when, after local midnight, an extra pass picks up the
// nightly set.
const afterMidnight = 2 * time.Minute

// ErrEngineUnavailable is returned by Export when the agent is not reachable.
var ErrEngineUnavailable = errors.New("the engine is not running")

// ErrNotConnected is returned by Copy when the copy folder is missing.
var ErrNotConnected = errors.New("isn't connected")

// Agent is the part of the engine client the exporter uses.
type Agent interface {
	ListBackups(ctx context.Context) ([]protocol.BackupSet, error)
	ExportBackupFile(ctx context.Context, id, file string, w io.Writer) (int64, error)
	DeleteBackup(ctx context.Context, id string) error
}

// Config configures an Exporter.
type Config struct {
	Agent Agent
	// Ready reports whether the agent is reachable; nil means always.
	Ready func() bool
	// Dir returns the host backup directory (it may change at runtime).
	Dir func() string
	// CopyDir returns the extra copy folder; "" or nil means none.
	CopyDir func() string
	// Interval between passes; DefaultInterval when 0.
	Interval time.Duration
	// CopyRetry is the pass interval while the copy is failing;
	// DefaultCopyRetry when 0.
	CopyRetry time.Duration
	// FreeSpace reports the free bytes at a path; diskspace.Of when nil.
	FreeSpace func(path string) (uint64, error)
	Now       func() time.Time
	Log       *slog.Logger
}

// State is export-state.json.
type State struct {
	LastExported string     `json:"last_exported,omitempty"`
	VerifiedAt   time.Time  `json:"verified_at,omitzero"`
	LastError    string     `json:"last_error,omitempty"`
	LastErrorAt  time.Time  `json:"last_error_at,omitzero"`
	Copy         *CopyState `json:"copy,omitempty"`
}

// CopyState records the extra copy to Dir.
type CopyState struct {
	Dir         string    `json:"dir"`
	LastCopied  string    `json:"last_copied,omitempty"`
	CopiedAt    time.Time `json:"copied_at,omitzero"`
	LastError   string    `json:"last_error,omitempty"`
	LastErrorAt time.Time `json:"last_error_at,omitzero"`
	// Missing is set while Dir is not there.
	Missing bool `json:"missing,omitempty"`
}

// Exporter copies backup sets out of the engine; see the package doc.
type Exporter struct {
	cfg  Config
	kick chan struct{}
	run  sync.Mutex // one export pass at a time
	copy sync.Mutex // one copy pass at a time
	save sync.Mutex // serializes export-state.json writes

	mu        sync.Mutex
	dir       string // dir the state below was loaded from
	state     State
	checkedAt time.Time
	pending   []protocol.BackupSet // oldest first
}

// New returns an Exporter; call Run to start it.
func New(cfg Config) *Exporter {
	if cfg.Interval == 0 {
		cfg.Interval = DefaultInterval
	}
	if cfg.CopyRetry == 0 {
		cfg.CopyRetry = DefaultCopyRetry
	}
	if cfg.CopyDir == nil {
		cfg.CopyDir = func() string { return "" }
	}
	if cfg.FreeSpace == nil {
		cfg.FreeSpace = func(p string) (uint64, error) {
			u, err := diskspace.Of(p)
			return u.Free, err
		}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.Ready == nil {
		cfg.Ready = func() bool { return true }
	}
	return &Exporter{cfg: cfg, kick: make(chan struct{}, 1)}
}

// Run exports every Interval and shortly after local midnight until ctx is
// done, and then updates the extra copy. Exports are skipped while the
// engine is down; a failing copy is retried every CopyRetry.
func (e *Exporter) Run(ctx context.Context) {
	for {
		if e.cfg.Ready() {
			if err := e.Export(ctx); err != nil && ctx.Err() == nil {
				e.cfg.Log.Warn("backup export failed", "err", err)
			}
		}
		copyErr := e.Copy(ctx)
		if copyErr != nil && ctx.Err() == nil && !errors.Is(copyErr, ErrNotConnected) {
			e.cfg.Log.Warn("extra backup copy failed", "err", copyErr)
		}
		wait := e.nextWait()
		if copyErr != nil {
			wait = min(wait, e.cfg.CopyRetry)
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		case <-e.kick:
			t.Stop()
		}
	}
}

// Kick asks Run for a pass now (e.g. after the folders changed).
func (e *Exporter) Kick() {
	select {
	case e.kick <- struct{}{}:
	default:
	}
}

// nextWait is the time to the next pass: Interval, or less when the
// after-midnight pass comes first.
func (e *Exporter) nextWait() time.Duration {
	now := e.cfg.Now()
	y, m, d := now.Date()
	next := time.Date(y, m, d, 0, 0, 0, 0, now.Location()).Add(afterMidnight)
	if !next.After(now) {
		next = time.Date(y, m, d+1, 0, 0, 0, 0, now.Location()).Add(afterMidnight)
	}
	return min(e.cfg.Interval, next.Sub(now))
}

// Export runs one pass and returns the error of every set that failed;
// those stay in the engine for the next pass.
func (e *Exporter) Export(ctx context.Context) error {
	e.run.Lock()
	defer e.run.Unlock()
	dir := e.cfg.Dir()
	e.loadState(dir)
	if err := ensureDir(dir); err != nil {
		e.fail(err)
		return err
	}
	err := e.export(ctx, dir)
	if err != nil {
		e.fail(err)
	}
	e.mu.Lock()
	if err == nil {
		e.state.LastError, e.state.LastErrorAt = "", time.Time{}
	}
	e.mu.Unlock()
	if serr := e.saveState(dir); serr != nil && err == nil {
		err = serr
	}
	return err
}

// ensureDir creates the backup folder, but only below an existing parent:
// a folder on a drive that is not connected must not be created on the
// boot disk under its mount point.
func ensureDir(dir string) error {
	if fi, err := os.Stat(dir); err == nil {
		if !fi.IsDir() {
			return fmt.Errorf("backup folder %s is not a folder", dir)
		}
		return nil
	}
	if fi, err := os.Stat(filepath.Dir(dir)); err != nil || !fi.IsDir() {
		return fmt.Errorf("backup folder %s is not available (is its drive connected?)", dir)
	}
	return os.Mkdir(dir, 0o755)
}

// saveState writes the current state to dir/export-state.json.
func (e *Exporter) saveState(dir string) error {
	e.save.Lock()
	defer e.save.Unlock()
	e.mu.Lock()
	st := e.state
	if st.Copy != nil {
		c := *st.Copy
		st.Copy = &c
	}
	same := e.dir == dir
	e.mu.Unlock()
	if !same {
		return nil
	}
	if _, err := os.Stat(dir); err != nil {
		return err
	}
	return writeJSONFile(filepath.Join(dir, StateFile), st)
}

func (e *Exporter) fail(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.state.LastError, e.state.LastErrorAt = err.Error(), e.cfg.Now()
}

func (e *Exporter) export(ctx context.Context, dir string) error {
	if !e.cfg.Ready() {
		return ErrEngineUnavailable
	}
	cleanPartials(dir, e.cfg.Log)
	sets, err := e.cfg.Agent.ListBackups(ctx)
	if err != nil {
		return fmt.Errorf("list engine backups: %w", err)
	}
	var pending []protocol.BackupSet
	for _, s := range sets {
		if s.Complete {
			pending = append(pending, s)
		}
	}
	slices.SortFunc(pending, func(a, b protocol.BackupSet) int {
		return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), strings.Compare(a.ID, b.ID))
	})
	e.mu.Lock()
	e.pending, e.checkedAt = slices.Clone(pending), e.cfg.Now()
	e.mu.Unlock()

	var errs []error
	for _, s := range pending {
		err := e.exportSet(ctx, dir, s)
		if errors.Is(err, client.ErrNotFound) {
			// The engine rotated it out (backup.sh keeps a few) while this
			// pass ran; a newer set replaced it.
			e.cfg.Log.Warn("backup left the engine before it was copied out", "id", s.ID)
			err = nil
		}
		if err != nil {
			e.cfg.Log.Warn("backup export failed", "id", s.ID, "err", err)
			errs = append(errs, fmt.Errorf("backup %s: %w", s.ID, err))
			if ctx.Err() != nil {
				break
			}
			continue
		}
		e.mu.Lock()
		e.pending = slices.DeleteFunc(e.pending, func(p protocol.BackupSet) bool { return p.ID == s.ID })
		e.mu.Unlock()
	}
	return errors.Join(errs...)
}

// exportSet copies s to dir/<id>, records it, deletes it in the engine and
// prunes older host sets.
func (e *Exporter) exportSet(ctx context.Context, dir string, s protocol.BackupSet) error {
	if err := validSet(s); err != nil {
		return err
	}
	final := filepath.Join(dir, s.ID)
	m := protocol.BackupManifest{ID: s.ID, CreatedAt: s.CreatedAt, Files: s.Files}
	switch _, err := os.Stat(final); {
	case err == nil:
		// Copied before but not deleted in the engine (the service stopped
		// in between): keep the copy if it is intact.
		if verr := VerifySet(final, m); verr != nil {
			e.cfg.Log.Warn("host backup does not match the engine's, copying it again", "id", s.ID, "err", verr)
			if err := os.RemoveAll(final); err != nil {
				return err
			}
			if err := e.download(ctx, dir, s); err != nil {
				return err
			}
		}
	case errors.Is(err, os.ErrNotExist):
		if err := e.download(ctx, dir, s); err != nil {
			return err
		}
	default:
		return err
	}

	e.mu.Lock()
	e.state.LastExported, e.state.VerifiedAt = s.ID, e.cfg.Now()
	e.mu.Unlock()
	if err := e.saveState(dir); err != nil {
		return err
	}
	e.cfg.Log.Info("backup copied out", "id", s.ID, "dir", final)

	var delErr error
	if err := e.cfg.Agent.DeleteBackup(ctx, s.ID); err != nil && !errors.Is(err, client.ErrNotFound) {
		delErr = fmt.Errorf("copied, but deleting it in the engine failed: %w", err)
	}
	// Older host sets can go once this one is verified on the host, whether
	// or not the engine still has it.
	return errors.Join(delErr, prune(dir, m, e.cfg.Log))
}

// download streams every file of s into dir/<id>.partial, verifies it from
// disk and renames it to dir/<id>.
func (e *Exporter) download(ctx context.Context, dir string, s protocol.BackupSet) error {
	if err := e.checkSpace(dir, s.Files); err != nil {
		return err
	}
	partial := filepath.Join(dir, s.ID+partialSuffix)
	if err := os.RemoveAll(partial); err != nil {
		return err
	}
	if err := os.Mkdir(partial, 0o755); err != nil {
		return err
	}
	done := false
	defer func() {
		if !done {
			_ = os.RemoveAll(partial)
		}
	}()
	for _, f := range s.Files {
		if err := e.downloadFile(ctx, partial, s.ID, f); err != nil {
			return err
		}
	}
	m := protocol.BackupManifest{ID: s.ID, CreatedAt: s.CreatedAt, Files: s.Files}
	if err := writeJSONFile(filepath.Join(partial, protocol.BackupManifestName), m); err != nil {
		return err
	}
	// Verify what landed on disk, not what streamed past.
	if err := VerifySet(partial, m); err != nil {
		return err
	}
	if err := syncDir(partial); err != nil {
		return err
	}
	if err := os.Rename(partial, filepath.Join(dir, s.ID)); err != nil {
		return err
	}
	done = true
	return syncDir(dir)
}

func (e *Exporter) downloadFile(ctx context.Context, dir, id string, bf protocol.BackupFile) error {
	f, err := os.OpenFile(filepath.Join(dir, bf.Name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	n, err := e.cfg.Agent.ExportBackupFile(ctx, id, bf.Name, f)
	if err == nil && n != bf.Size {
		err = fmt.Errorf("%s: got %d bytes, manifest says %d", bf.Name, n, bf.Size)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// checkSpace fails early, with a clear message, when dir has no room for
// files.
func (e *Exporter) checkSpace(dir string, files []protocol.BackupFile) error {
	var need uint64
	for _, f := range files {
		need += uint64(max(f.Size, 0))
	}
	free, err := e.cfg.FreeSpace(dir)
	if err != nil || free >= need {
		return nil // the copy itself reports a full disk
	}
	return fmt.Errorf("not enough space in %s: the backup needs %s, %s free", dir, mib(need), mib(free))
}

func mib(n uint64) string {
	if n >= 10<<30 {
		return fmt.Sprintf("%d GB", n>>30)
	}
	return fmt.Sprintf("%d MB", (n+(1<<20)-1)>>20)
}

func validSet(s protocol.BackupSet) error {
	if err := (protocol.DBRestore{BackupID: s.ID}).Validate(); err != nil || !protocol.ValidFileName(s.ID) {
		return fmt.Errorf("invalid backup id %q", s.ID)
	}
	if len(s.Files) == 0 {
		return errors.New("the set lists no files")
	}
	seen := map[string]bool{}
	for _, f := range s.Files {
		if !protocol.ValidFileName(f.Name) || f.Name == protocol.BackupManifestName || f.Name == protocol.BackupKeyName || seen[f.Name] {
			return fmt.Errorf("invalid file name %q", f.Name)
		}
		seen[f.Name] = true
	}
	return nil
}

// VerifySet checks that dir holds every file of m with its size and SHA-256.
func VerifySet(dir string, m protocol.BackupManifest) error {
	for _, bf := range m.Files {
		if err := verifyFile(filepath.Join(dir, bf.Name), bf); err != nil {
			return err
		}
	}
	return nil
}

func verifyFile(path string, bf protocol.BackupFile) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return err
	}
	if n != bf.Size {
		return fmt.Errorf("%s: %d bytes, manifest says %d", bf.Name, n, bf.Size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != bf.SHA256 {
		return fmt.Errorf("%s: sha256 mismatch: manifest %s, got %s", bf.Name, bf.SHA256, got)
	}
	return nil
}

// ReadManifest reads dir/manifest.json; it must be the manifest of set id.
func ReadManifest(dir, id string) (protocol.BackupManifest, error) {
	var m protocol.BackupManifest
	b, err := os.ReadFile(filepath.Join(dir, protocol.BackupManifestName))
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("%s: %w", protocol.BackupManifestName, err)
	}
	if m.ID != id {
		return m, fmt.Errorf("%s: set %q, want %q", protocol.BackupManifestName, m.ID, id)
	}
	if err := validSet(protocol.BackupSet{ID: m.ID, Files: m.Files}); err != nil {
		return m, err
	}
	return m, nil
}

// HostSets lists the exported sets in dir, newest first. Folders without a
// valid manifest are not backups and are left out.
func HostSets(dir string) ([]api.HostBackup, error) {
	ents, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := []api.HostBackup{}
	for _, ent := range ents {
		if !ent.IsDir() || strings.HasSuffix(ent.Name(), partialSuffix) {
			continue
		}
		p := filepath.Join(dir, ent.Name())
		m, err := ReadManifest(p, ent.Name())
		if err != nil {
			continue
		}
		var size int64
		for _, f := range m.Files {
			size += f.Size
		}
		out = append(out, api.HostBackup{ID: m.ID, CreatedAt: m.CreatedAt, Size: size, Path: p})
	}
	slices.SortFunc(out, func(a, b api.HostBackup) int {
		return cmp.Or(b.CreatedAt.Compare(a.CreatedAt), strings.Compare(b.ID, a.ID))
	})
	return out, nil
}

// prune deletes host sets older than keep, which was just verified.
func prune(dir string, keep protocol.BackupManifest, log *slog.Logger) error {
	sets, err := HostSets(dir)
	if err != nil {
		return err
	}
	var errs []error
	for _, s := range sets {
		if s.ID == keep.ID || !s.CreatedAt.Before(keep.CreatedAt) {
			continue
		}
		if err := os.RemoveAll(s.Path); err != nil {
			errs = append(errs, err)
			continue
		}
		log.Info("deleted older host backup", "id", s.ID)
	}
	return errors.Join(errs...)
}

// cleanPartials removes copies a crash left unfinished.
func cleanPartials(dir string, log *slog.Logger) {
	ents, _ := os.ReadDir(dir)
	for _, ent := range ents {
		id, ok := strings.CutSuffix(ent.Name(), partialSuffix)
		if !ok || !ent.IsDir() || !protocol.ValidFileName(id) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, ent.Name())); err != nil {
			log.Warn("removing an unfinished backup copy failed", "dir", ent.Name(), "err", err)
		}
	}
}

func (e *Exporter) loadState(dir string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.dir == dir {
		return
	}
	e.dir, e.state = dir, State{}
	if b, err := os.ReadFile(filepath.Join(dir, StateFile)); err == nil {
		_ = json.Unmarshal(b, &e.state)
	}
}

// Status reports the export state.
func (e *Exporter) Status() api.BackupStatus {
	dir := e.cfg.Dir()
	e.loadState(dir)
	e.mu.Lock()
	defer e.mu.Unlock()
	st := api.BackupStatus{
		Dir: dir, LastExported: e.state.LastExported, VerifiedAt: e.state.VerifiedAt,
		LastError: e.state.LastError, LastErrorAt: e.state.LastErrorAt,
		CheckedAt: e.checkedAt, Pending: len(e.pending),
	}
	if len(e.pending) > 0 {
		st.OldestPendingAt = e.pending[0].CreatedAt
	}
	st.Critical = st.Pending >= api.MaxPendingBackups
	switch {
	case st.Critical:
		st.Message = fmt.Sprintf("Backup not copied out since %s. %d backups are waiting in the engine, which keeps no more: the next one replaces the oldest.", since(st), st.Pending)
	case st.LastError != "":
		st.Message = "Backup not copied out since " + since(st)
	}
	st.Copy = e.copyStatus()
	return st
}

// copyStatus reports the extra copy; e.mu is held.
func (e *Exporter) copyStatus() *api.BackupCopyStatus {
	cdir := e.cfg.CopyDir()
	if cdir == "" {
		return nil
	}
	c := &api.BackupCopyStatus{Dir: cdir, Connected: true}
	cs := e.state.Copy
	if cs == nil || cs.Dir != cdir {
		return c
	}
	c.LastCopied, c.CopiedAt = cs.LastCopied, cs.CopiedAt
	c.LastError, c.LastErrorAt, c.Connected = cs.LastError, cs.LastErrorAt, !cs.Missing
	last := "never"
	if !cs.CopiedAt.IsZero() {
		last = cs.CopiedAt.Local().Format(time.DateTime)
	}
	switch {
	case cs.Missing:
		c.Message = fmt.Sprintf("The backup copy folder %s isn't connected; last copied %s. It is copied when the folder is back.", cdir, last)
	case cs.LastError != "":
		c.Message = fmt.Sprintf("Copying the backup to %s failed; last copied %s.", cdir, last)
	}
	return c
}

func since(st api.BackupStatus) string {
	t := st.VerifiedAt
	if t.IsZero() {
		t = st.OldestPendingAt
	}
	if t.IsZero() {
		return "the service started"
	}
	return t.Local().Format(time.DateTime)
}

// writeJSONFile replaces path with v atomically (0644).
func writeJSONFile(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	_, err = f.Write(append(b, '\n'))
	if err == nil {
		err = f.Chmod(0o644)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	if err := d.Sync(); err != nil && !errors.Is(err, os.ErrInvalid) {
		return err
	}
	return nil
}

// List returns the sets on the host and those still in the engine.
func (e *Exporter) List(ctx context.Context) (api.BackupList, error) {
	dir := e.cfg.Dir()
	host, err := HostSets(dir)
	if err != nil {
		return api.BackupList{}, err
	}
	l := api.BackupList{Dir: dir, Host: host, Engine: []protocol.BackupSet{}}
	if !e.cfg.Ready() {
		l.EngineError = ErrEngineUnavailable.Error()
		return l, nil
	}
	sets, err := e.cfg.Agent.ListBackups(ctx)
	if err != nil {
		l.EngineError = err.Error()
		return l, nil
	}
	l.Engine = append(l.Engine, sets...)
	return l, nil
}

// Copy copies the newest host set to the copy folder, if one is set: into
// <copy>/<id>.partial, verified there, renamed to <copy>/<id>, and only then
// are older sets there deleted. The folder is never created: while it is
// missing, Copy returns ErrNotConnected and the state keeps the last copy.
func (e *Exporter) Copy(ctx context.Context) error {
	e.copy.Lock()
	defer e.copy.Unlock()
	dir, cdir := e.cfg.Dir(), e.cfg.CopyDir()
	e.loadState(dir)
	e.mu.Lock()
	cs := e.state.Copy
	switch {
	case cdir == "" && cs == nil:
		e.mu.Unlock()
		return nil
	case cdir == "":
		e.state.Copy = nil
	case cs == nil || cs.Dir != cdir:
		cs = &CopyState{Dir: cdir}
	default:
		c := *cs
		cs = &c
	}
	e.mu.Unlock()
	if cdir == "" {
		return e.saveState(dir)
	}

	id, err := e.copySet(ctx, dir, cs)
	now := e.cfg.Now()
	cs.Missing = errors.Is(err, ErrNotConnected)
	if err != nil {
		cs.LastError, cs.LastErrorAt = err.Error(), now
	} else {
		cs.LastError, cs.LastErrorAt = "", time.Time{}
		if id != "" && id != cs.LastCopied {
			cs.LastCopied, cs.CopiedAt = id, now
		}
	}
	e.mu.Lock()
	e.state.Copy = cs
	e.mu.Unlock()
	if serr := e.saveState(dir); serr != nil && err == nil && !errors.Is(serr, os.ErrNotExist) {
		err = serr
	}
	return err
}

// copySet brings the copy folder up to the newest host set and returns its
// id ("" when the host has none yet).
func (e *Exporter) copySet(ctx context.Context, dir string, cs *CopyState) (string, error) {
	cdir := cs.Dir
	if fi, err := os.Stat(cdir); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("%s %w", cdir, ErrNotConnected)
	}
	sets, err := HostSets(dir)
	if err != nil {
		return "", err
	}
	if len(sets) == 0 {
		return "", nil
	}
	newest := sets[0]
	m, err := ReadManifest(newest.Path, newest.ID)
	if err != nil {
		return "", err
	}
	final := filepath.Join(cdir, m.ID)
	cleanPartials(cdir, e.cfg.Log)
	_, statErr := os.Stat(filepath.Join(final, protocol.BackupManifestName))
	switch {
	case statErr == nil && cs.LastCopied == m.ID:
		// Verified when it was copied; re-reading a whole set from a slow
		// drive every pass is not worth it.
	case statErr == nil && VerifySet(final, m) == nil:
	default:
		if err := os.RemoveAll(final); err != nil {
			return "", err
		}
		if err := e.copyFiles(ctx, newest.Path, cdir, m); err != nil {
			return "", err
		}
		e.cfg.Log.Info("backup copied to the copy folder", "id", m.ID, "dir", final)
	}
	return m.ID, prune(cdir, m, e.cfg.Log)
}

// copyFiles copies set m from src into cdir/<id>.partial, verifies it from
// disk and renames it to cdir/<id>.
func (e *Exporter) copyFiles(ctx context.Context, src, cdir string, m protocol.BackupManifest) error {
	if err := e.checkSpace(cdir, m.Files); err != nil {
		return err
	}
	partial := filepath.Join(cdir, m.ID+partialSuffix)
	if err := os.Mkdir(partial, 0o755); err != nil {
		return err
	}
	done := false
	defer func() {
		if !done {
			_ = os.RemoveAll(partial)
		}
	}()
	for _, f := range m.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := copyFile(ctx, filepath.Join(src, f.Name), filepath.Join(partial, f.Name)); err != nil {
			return err
		}
	}
	if err := writeJSONFile(filepath.Join(partial, protocol.BackupManifestName), m); err != nil {
		return err
	}
	if err := VerifySet(partial, m); err != nil {
		return err
	}
	if err := syncDir(partial); err != nil {
		return err
	}
	if err := os.Rename(partial, filepath.Join(cdir, m.ID)); err != nil {
		return err
	}
	done = true
	return syncDir(cdir)
}

func copyFile(ctx context.Context, src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, ctxReader{ctx, in})
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

// ctxReader stops a long copy when ctx is done.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
