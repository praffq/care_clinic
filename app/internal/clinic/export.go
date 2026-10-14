package clinic

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine"
	"github.com/ohcnetwork/care_desktop/app/internal/storage"
)

// The engine's own disk is the source of truth for backups. The host backup
// folder is a verified export of it that the Backups screen, the optional
// second copy and restore read; it is refreshed explicitly through the agent's
// file API and never shared with the engine.

var (
	exportGate = make(chan struct{}, 1)
	stateGate  = make(chan struct{}, 1)
	// exportSums remembers verified exports so an unchanged multi-gigabyte
	// file is not hashed again on every refresh.
	exportSums = map[string]exportedFile{}
)

type exportedFile struct {
	size    int64
	modTime time.Time
	sha     string
}

// exportedName is every file the backup sidecar publishes; only these are
// ever pruned from the host folder.
var exportedName = regexp.MustCompile(`^(?:care-[^/]+\.dump(?:\.enc)?|files-[^/]+\.tar\.gz(?:\.enc)?|base-\d{8}-\d{6}/[^/]+|wal/[^/]+)$`)

// ExportBackups brings the host backup folder and backup status up to date
// with the engine. Calls are serialised; a second caller waits for the first.
func (e *Clinic) ExportBackups(ctx context.Context) error {
	if installed, err := e.kitInstalled(); err != nil || !installed {
		return err
	}
	select {
	case exportGate <- struct{}{}:
		defer func() { <-exportGate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := e.exportBackupFiles(ctx); err != nil {
		return err
	}
	return e.ExportBackupState(ctx)
}

// ExportBackupState refreshes only the small status files the health view
// reads, for callers that cannot wait for a backup set to transfer.
func (e *Clinic) ExportBackupState(ctx context.Context) error {
	if installed, err := e.kitInstalled(); err != nil || !installed {
		return err
	}
	select {
	case stateGate <- struct{}{}:
		defer func() { <-stateGate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	return e.exportBackupState(ctx)
}

func (e *Clinic) kitInstalled() (bool, error) {
	info, err := os.Stat(filepath.Join(e.InstallDir, "docker-compose.yml"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, errors.New("the installed compose file is not a regular file")
	}
	return true, nil
}

func (e *Clinic) exportBackupFiles(ctx context.Context) error {
	client := e.agentClient()
	host := e.backupDir()
	entries, err := client.List(ctx, AgentBackupDir)
	if err != nil {
		return fmt.Errorf("could not list the engine's backups: %w", err)
	}
	guest := map[string]engine.Entry{}
	for _, en := range entries {
		if storage.SafeBackupPath(en.Path) {
			guest[en.Path] = en
		}
	}
	if len(guest) == 0 {
		return nil
	}
	if err := os.MkdirAll(host, 0o755); err != nil {
		return err
	}

	var manifest storage.Manifest
	var manifestBytes []byte
	manifestTmp := ""
	defer func() {
		if manifestTmp != "" {
			_ = os.Remove(manifestTmp)
		}
	}()
	if _, ok := guest[storage.ManifestFile]; ok {
		manifestTmp = filepath.Join(host, ".care-export-"+storage.ManifestFile)
		if err := client.Get(ctx, path.Join(AgentBackupDir, storage.ManifestFile), manifestTmp); err != nil {
			return fmt.Errorf("could not read the engine's backup record: %w", err)
		}
		b, err := os.ReadFile(manifestTmp)
		if err != nil {
			return err
		}
		if manifest, err = storage.ParseManifest(b); err != nil {
			return err
		}
		if !manifest.Verified() {
			return errors.New("the engine backup record is not verified; previous export retained")
		}
		manifestBytes = b
	}
	sums := map[string]string{}
	for _, f := range manifest.Files {
		entry, exists := guest[f.Path]
		if !exists || entry.Size != f.Bytes {
			return fmt.Errorf("the engine backup record lists missing or incomplete file %s; previous export retained", f.Path)
		}
		sums[f.Path] = f.SHA256
	}

	// Change-file checksum records come first so each change file can be
	// verified against its own record.
	names := make([]string, 0, len(guest))
	for rel := range guest {
		_, listed := sums[rel]
		standalone := strings.HasPrefix(rel, "care-pre-update-") || strings.HasPrefix(rel, "care-manual-") || strings.HasPrefix(rel, "files-manual-")
		wal := strings.HasPrefix(rel, "wal/") && (strings.HasSuffix(rel, ".sha256") || strings.HasSuffix(rel, ".gz.enc"))
		if listed || wal || (standalone && !strings.Contains(rel, "/")) {
			names = append(names, rel)
		}
	}
	sort.Slice(names, func(i, j int) bool {
		ri, rj := strings.HasSuffix(names[i], ".sha256"), strings.HasSuffix(names[j], ".sha256")
		if ri != rj {
			return ri
		}
		return names[i] < names[j]
	})
	for _, rel := range names {
		want := sums[rel]
		if want == "" && strings.HasPrefix(rel, "wal/") && strings.HasSuffix(rel, ".gz.enc") {
			record := strings.TrimSuffix(rel, ".gz.enc") + ".sha256"
			if _, ok := guest[record]; ok {
				if want, err = readSHARecord(filepath.Join(host, filepath.FromSlash(record))); err != nil {
					return fmt.Errorf("change file %s has an unreadable checksum record: %w", rel, err)
				}
			} else {
				// The archiver publishes the data before its checksum.
				// A later refresh will pick up this not-yet-committed file.
				continue
			}
		}
		if err := exportFile(ctx, client, path.Join(AgentBackupDir, rel), filepath.Join(host, filepath.FromSlash(rel)), guest[rel], want); err != nil {
			return fmt.Errorf("could not export backup file %s: %w", rel, err)
		}
	}
	if manifestTmp != "" {
		check := manifestTmp + ".check"
		defer func() { _ = os.Remove(check) }()
		if err := client.Get(ctx, path.Join(AgentBackupDir, storage.ManifestFile), check); err != nil {
			return fmt.Errorf("could not confirm the engine backup record: %w", err)
		}
		current, err := os.ReadFile(check)
		if err != nil {
			return err
		}
		if !bytes.Equal(current, manifestBytes) {
			return errors.New("the engine backup changed during export; previous record retained, retry the export")
		}
		// The record is published last, after every file it lists is in place.
		if err := os.Rename(manifestTmp, filepath.Join(host, storage.ManifestFile)); err != nil {
			return err
		}
		manifestTmp = ""
	}
	// Only a verified engine record allows pruning: an empty or freshly
	// replaced engine disk must never empty the host folder.
	if manifest.Verified() {
		return pruneExport(host, guest)
	}
	return nil
}

// exportFile fetches remote to local unless an identical verified copy is
// already there. The download lands on a temporary name and is checked for
// size and, when a checksum is published, content before it is renamed.
func exportFile(ctx context.Context, client *engine.Client, remote, local string, entry engine.Entry, sha string) error {
	size := entry.Size
	if info, err := os.Stat(local); err == nil && info.Mode().IsRegular() && info.Size() == size {
		if sha == "" && info.ModTime().UnixNano() == entry.MtimeNs {
			return nil
		}
		if known, ok := exportSums[local]; sha != "" && ok && known.size == size && known.modTime.Equal(info.ModTime()) && known.sha == sha {
			return nil
		}
		if got, _, err := fileSum(local); sha != "" && err == nil && got == sha {
			exportSums[local] = exportedFile{size, info.ModTime(), sha}
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(local), ".care-export-"+filepath.Base(local))
	defer func() { _ = os.Remove(tmp) }()
	if err := client.Get(ctx, remote, tmp); err != nil {
		return err
	}
	got, n, err := fileSum(tmp)
	if err != nil {
		return err
	}
	if n != size {
		return fmt.Errorf("received %d bytes, expected %d", n, size)
	}
	if sha != "" && got != sha {
		return errors.New("the received copy does not match its published checksum")
	}
	if err := os.Rename(tmp, local); err != nil {
		return err
	}
	if info, err := os.Stat(local); err == nil && sha != "" {
		exportSums[local] = exportedFile{size, info.ModTime(), sha}
	}
	return nil
}

func readSHARecord(p string) (string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "sha256="); ok && len(v) == 64 {
			return v, nil
		}
	}
	return "", errors.New("missing sha256 line")
}

// pruneExport removes published backup files the engine no longer keeps, so
// the host folder follows the sidecar's own retention.
func pruneExport(host string, guest map[string]engine.Entry) error {
	var failed []error
	err := filepath.WalkDir(host, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(host, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !exportedName.MatchString(rel) {
			return nil
		}
		if _, ok := guest[rel]; ok {
			return nil
		}
		if err := os.Remove(p); err != nil {
			failed = append(failed, err)
		}
		delete(exportSums, p)
		return nil
	})
	if err != nil {
		return err
	}
	if dirs, err := filepath.Glob(filepath.Join(host, "base-*")); err == nil {
		for _, dir := range dirs {
			_ = os.Remove(dir) // only succeeds once empty
		}
	}
	return errors.Join(failed...)
}

// exportBackupState copies the sidecar's status files into the install's
// backup-state folder. copy-status.json is the desktop's own record of the
// second copy and is never replaced from the engine.
func (e *Clinic) exportBackupState(ctx context.Context) error {
	client := e.agentClient()
	entries, err := client.List(ctx, agentStateDir)
	if err != nil {
		return fmt.Errorf("could not list the engine's backup status: %w", err)
	}
	host := filepath.Join(e.InstallDir, storage.StateDirName)
	seen := map[string]bool{}
	for _, en := range entries {
		if !guestStateFile(en.Path) {
			continue
		}
		seen[en.Path] = true
		if err := client.Get(ctx, path.Join(agentStateDir, en.Path), filepath.Join(host, en.Path)); err != nil {
			return fmt.Errorf("could not export backup status %s: %w", en.Path, err)
		}
	}
	if len(seen) == 0 {
		return nil
	}
	local, err := os.ReadDir(host)
	if err != nil {
		return err
	}
	for _, en := range local {
		name := en.Name()
		if !en.Type().IsRegular() || seen[name] || !guestStateFile(name) {
			continue
		}

		if err := os.Remove(filepath.Join(host, name)); err != nil {
			return fmt.Errorf("remove outdated backup status %s: %w", name, err)
		}
	}
	return nil
}

func guestStateFile(name string) bool {
	switch name {
	case storage.StatusFile, storage.WALStatusFile, "trial-at", "wal-clean-at", "wal-pending-since", "backup-ok-at", "backup-failed-at":
		return true
	default:
		return false
	}
}
