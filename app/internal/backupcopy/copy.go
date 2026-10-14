// Package backupcopy keeps an optional second copy of the newest verified
// backup on a USB drive, second disk or network share. The main backup always
// stays on the local backup folder; this copy only follows it.
//
// Only files listed in a verified manifest.json, and archived change files
// with a published checksum, are copied. Every file is written to a temporary
// name, flushed, hashed back and only then renamed into place; the copy's
// manifest.json is written last; and files the new copy no longer needs are
// deleted only after everything else was verified. An absent destination is
// reported as disconnected and nothing is created on the parent path, so a
// missing drive letter or unmounted share never fills the local disk.
package backupcopy

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/storage"
)

// Folder is created inside the chosen destination to hold the copy.
const Folder = "care-backup-copy"

var (
	walFile     = regexp.MustCompile(`^([0-9A-F]{24}|[0-9A-F]{8}\.history)\.(gz\.enc|sha256)$`)
	historyFile = regexp.MustCompile(`^[0-9A-F]{8}\.history$`)
	setEntry    = regexp.MustCompile(`^(care-\d{8}-\d{6}\.dump\.enc|files-\d{8}-\d{6}\.tar\.gz\.enc|base-\d{8}-\d{6})$`)
	walSeg      = regexp.MustCompile(`^[0-9A-F]{24}$`)
)

var mu sync.Mutex

var (
	errNothing = errors.New("no verified backup")
	errPrune   = errors.New("prune")
)

// Run brings the copy at chosen up to date with the backup in source and
// records the result in stateDir. chosen == "" switches the copy off. The
// error reports only a failure to record the status, which the caller must
// surface because the Backups screen would otherwise show stale state.
func Run(source, stateDir, chosen string, now time.Time) (st storage.CopyStatus, err error) {
	mu.Lock()
	defer mu.Unlock()
	prev, readErr := storage.ReadCopyStatus(stateDir)
	if readErr != nil {
		prev = storage.CopyStatus{}
	}
	st = storage.CopyStatus{
		State: "off", Dir: chosen, CheckedAt: now.Unix(),
		LastCopiedAt: prev.LastCopiedAt, Set: prev.Set, WALThrough: prev.WALThrough,
	}
	if prev.Dir != chosen {
		st.LastCopiedAt, st.Set, st.WALThrough = 0, "", ""
	}
	defer func() {
		if werr := storage.WriteCopyStatus(stateDir, st); werr != nil && err == nil {
			err = fmt.Errorf("could not record the backup copy status: %w", werr)
		}
	}()
	if chosen == "" {
		return st, nil
	}
	if info, err := os.Stat(chosen); err != nil || !info.IsDir() {
		st.State = "disconnected"
		st.Message = "the copy destination is not connected"
		return st, nil
	}
	working := st
	working.State, working.Message = "copying", ""
	if werr := storage.WriteCopyStatus(stateDir, working); werr != nil {
		st.State, st.Message = "failed", "could not record the copy status: "+werr.Error()
		return st, fmt.Errorf("could not record the backup copy status: %w", werr)
	}
	set, through, cerr := copyOnce(source, filepath.Join(chosen, Folder))
	if set != "" {
		st.LastCopiedAt, st.Set, st.WALThrough = now.Unix(), set, through
	}
	switch {
	case cerr == nil:
		st.State, st.Message = "ok", ""
	case errors.Is(cerr, errNothing):
		st.State, st.Message = "waiting", "there is no verified backup to copy yet"
	default:
		if _, statErr := os.Stat(chosen); statErr != nil {
			st.State, st.Message = "disconnected", "the copy destination was disconnected while copying"
		} else {
			st.State, st.Message = "failed", cerr.Error()
		}
	}
	return st, nil
}

type want struct {
	rel    string
	sha256 string
	size   int64
}

// copyOnce returns the copied set name when the copy's manifest was written,
// even if removing older copy files then failed.
func copyOnce(source, dest string) (string, string, error) {
	raw, err := os.ReadFile(filepath.Join(source, storage.ManifestFile))
	if errors.Is(err, os.ErrNotExist) {
		return "", "", errNothing
	}
	if err != nil {
		return "", "", fmt.Errorf("could not read the backup record: %v", err)
	}
	m, err := storage.ParseManifest(raw)
	if err != nil {
		return "", "", err
	}
	if !m.Verified() {
		return "", "", errNothing
	}
	wants := make([]want, 0, len(m.Files))
	for _, f := range m.Files {
		wants = append(wants, want{rel: f.Path, sha256: f.SHA256, size: f.Bytes})
	}
	segs, through, err := walWants(source, m)
	if err != nil {
		return "", "", err
	}
	wants = append(wants, segs...)

	// Only the copy's own folder is ever created; the chosen parent must exist.
	if err := os.Mkdir(dest, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", "", fmt.Errorf("could not create the copy folder: %v", err)
	}
	if info, err := os.Lstat(dest); err != nil || !info.IsDir() {
		return "", "", errors.New("the copy folder is not a folder")
	}
	cleanTemps(dest)
	for _, w := range wants {
		if err := place(source, dest, w); err != nil {
			return "", "", err
		}
	}
	// The manifest is the commit point of the copy: written only after every
	// file it names is in place and verified.
	if err := writeVerified(filepath.Join(dest, storage.ManifestFile), raw); err != nil {
		return "", "", err
	}
	if err := prune(dest, wants); err != nil {
		return m.Set, through, err
	}
	return m.Set, through, nil
}

// walWants lists archived change files from the full copy's first segment
// on, plus timeline history files, pairing each ciphertext with its checksum
// record (copied after it). The chain must be unbroken from the first
// segment through at least the one the manifest verified; otherwise the copy
// could not restore the backup and is not marked current.
func walWants(source string, m storage.Manifest) ([]want, string, error) {
	from, segBytes := m.Base.StartWAL, m.Base.WALSegmentBytes
	if !walSeg.MatchString(from) {
		return nil, "", errors.New("the backup record names no first change file")
	}
	if segBytes <= 0 || segBytes&(segBytes-1) != 0 || segBytes > 1<<30 {
		return nil, "", errors.New("the backup record has an invalid change-file size")
	}
	dir := filepath.Join(source, "wal")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, "", fmt.Errorf("could not read the change files the backup needs: %v", err)
	}
	var segs, history []string
	for _, e := range entries {
		n, ok := strings.CutSuffix(e.Name(), ".sha256")
		if !ok || !e.Type().IsRegular() {
			continue
		}
		switch {
		case walSeg.MatchString(n) && n[:8] == from[:8] && n >= from:
			segs = append(segs, n)
		case historyFile.MatchString(n):
			history = append(history, n)
		}
	}
	sort.Strings(segs)
	sort.Strings(history)
	through := ""
	for i, n := range segs {
		want := from
		if i > 0 {
			want = nextSegment(segs[i-1], segBytes)
		}
		if n != want {
			return nil, "", fmt.Errorf("change file %s is missing from the backup folder, so the copy would not be restorable", want)
		}
		through = n
	}
	if through == "" || (m.WAL.Through != "" && through < m.WAL.Through) {
		return nil, "", errors.New("the change files the verified backup needs are missing from the backup folder")
	}
	var out []want
	for _, n := range append(history, segs...) {
		data := n + ".gz.enc"
		record := filepath.Join(dir, n+".sha256")
		sum, err := readSum(record)
		if err != nil {
			return nil, "", fmt.Errorf("change file %s has no valid checksum: %v", n, err)
		}
		recordSum, err := fileSHA(record)
		if err != nil {
			return nil, "", fmt.Errorf("could not read change file %s: %v", n, err)
		}
		out = append(out,
			want{rel: "wal/" + data, sha256: sum, size: -1},
			want{rel: "wal/" + n + ".sha256", sha256: recordSum, size: -1})
	}
	return out, through, nil
}

// nextSegment returns the WAL segment after name for the given segment size.
func nextSegment(name string, segBytes int64) string {
	var tl, log, seg uint64
	_, _ = fmt.Sscanf(name, "%08X%08X%08X", &tl, &log, &seg)
	per := uint64(0x100000000) / uint64(segBytes)
	seg++
	if seg >= per {
		seg, log = 0, log+1
	}
	return fmt.Sprintf("%08X%08X%08X", tl, log, seg)
}

func readSum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "sha256="); ok && len(v) == 64 {
			return v, nil
		}
	}
	return "", errors.New("missing sha256 line")
}

func fileSHA(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// place makes dest/rel a verified copy of source/rel unless it already is.
func place(source, dest string, w want) error {
	target := filepath.Join(dest, filepath.FromSlash(w.rel))
	if info, err := os.Lstat(target); err == nil && info.Mode().IsRegular() && (w.size < 0 || info.Size() == w.size) {
		if got, err := fileSHA(target); err == nil && got == w.sha256 {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return fmt.Errorf("could not create %s on the copy: %v", filepath.Dir(w.rel), err)
	}
	in, err := os.Open(filepath.Join(source, filepath.FromSlash(w.rel)))
	if err != nil {
		return fmt.Errorf("%s is missing from the backup folder: %v", w.rel, err)
	}
	defer func() { _ = in.Close() }()
	tmp := filepath.Join(filepath.Dir(target), ".tmp-"+filepath.Base(target))
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("could not write %s to the copy: %v", w.rel, err)
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(out, h), in)
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("could not write %s to the copy: %v", w.rel, err)
	}
	if hex.EncodeToString(h.Sum(nil)) != w.sha256 {
		_ = os.Remove(tmp)
		return fmt.Errorf("%s in the backup folder does not match its recorded checksum, so it was not copied", w.rel)
	}
	if got, err := fileSHA(tmp); err != nil || got != w.sha256 {
		_ = os.Remove(tmp)
		return fmt.Errorf("%s did not read back correctly from the copy", w.rel)
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("could not finish copying %s: %v", w.rel, err)
	}
	syncDir(filepath.Dir(target))
	return nil
}

func writeVerified(target string, data []byte) error {
	tmp := filepath.Join(filepath.Dir(target), ".tmp-"+filepath.Base(target))
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("could not write the copy's backup record: %v", err)
	}
	_, err = out.Write(data)
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		var back []byte
		back, err = os.ReadFile(tmp)
		if err == nil && string(back) != string(data) {
			err = errors.New("it did not read back correctly")
		}
	}
	if err == nil {
		err = os.Rename(tmp, target)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("could not write the copy's backup record: %v", err)
	}
	syncDir(filepath.Dir(target))
	return nil
}

func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}

func cleanTemps(dest string) {
	dirs := []string{dest, filepath.Join(dest, "wal")}
	if entries, err := os.ReadDir(dest); err == nil {
		for _, e := range entries {
			if e.IsDir() && setEntry.MatchString(e.Name()) {
				dirs = append(dirs, filepath.Join(dest, e.Name()))
			}
		}
	}
	for _, dir := range dirs {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".tmp-") && e.Type().IsRegular() {
				_ = os.Remove(filepath.Join(dir, e.Name()))
			}
		}
	}
}

// prune removes earlier sets and change files the new copy no longer needs.
// It touches only names the copier itself writes; anything else is left.
func prune(dest string, wants []want) error {
	keep := map[string]bool{}
	for _, w := range wants {
		keep[w.rel] = true
		keep[strings.SplitN(w.rel, "/", 2)[0]] = true
	}
	entries, err := os.ReadDir(dest)
	if err != nil {
		return fmt.Errorf("%w: could not list the copy folder: %v", errPrune, err)
	}
	var failed []string
	for _, e := range entries {
		n := e.Name()
		if keep[n] || !setEntry.MatchString(n) || e.Type()&os.ModeSymlink != 0 {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dest, n)); err != nil {
			failed = append(failed, n)
		}
	}
	if wal, err := os.ReadDir(filepath.Join(dest, "wal")); err == nil {
		for _, e := range wal {
			n := e.Name()
			if keep["wal/"+n] || !walFile.MatchString(n) || !e.Type().IsRegular() {
				continue
			}
			if err := os.Remove(filepath.Join(dest, "wal", n)); err != nil {
				failed = append(failed, "wal/"+n)
			}
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("%w: the new copy is complete, but older copy files could not be removed: %s",
			errPrune, strings.Join(failed, ", "))
	}
	return nil
}
