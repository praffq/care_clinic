package agent

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
)

// ManifestName is the file that marks a backup set as complete.
const ManifestName = "manifest.json"

const maxManifestBytes = 1 << 20

var sha256HexRE = regexp.MustCompile(`^[a-f0-9]{64}$`)

// Manifest is the on-disk manifest.json of a backup set.
type Manifest struct {
	CreatedAt time.Time             `json:"created_at"`
	Files     []protocol.BackupFile `json:"files"`
}

func validBackupID(id string) bool {
	return protocol.DBRestore{BackupID: id}.Validate() == nil && id != "." && id != ".."
}

// readManifest parses and checks the manifest of the set in dir. It returns
// an error unless every listed file has a valid name and sha256.
func readManifest(root *os.Root, id string) (*Manifest, error) {
	f, err := root.Open(filepath.Join(id, ManifestName))
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var m Manifest
	dec := json.NewDecoder(io.LimitReader(f, maxManifestBytes))
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	if len(m.Files) == 0 {
		return nil, errors.New("manifest lists no files")
	}
	for _, bf := range m.Files {
		if !protocol.ValidFileName(bf.Name) || bf.Name == ManifestName || !sha256HexRE.MatchString(bf.SHA256) || bf.Size < 0 {
			return nil, errors.New("manifest has an invalid file entry")
		}
	}
	return &m, nil
}

func (s *Server) openBackups() (*os.Root, error) {
	if err := os.MkdirAll(s.cfg.BackupsRoot, 0o700); err != nil {
		return nil, err
	}
	return os.OpenRoot(s.cfg.BackupsRoot)
}

// backupSet describes the set id. Sets without a valid manifest, or whose
// files are missing or of the wrong size, are reported incomplete with the
// files found on disk (no checksums).
func backupSet(root *os.Root, id string, modTime time.Time) protocol.BackupSet {
	set := protocol.BackupSet{ID: id, CreatedAt: modTime.UTC(), Files: []protocol.BackupFile{}}
	if m, err := readManifest(root, id); err == nil {
		set.Complete = true
		if !m.CreatedAt.IsZero() {
			set.CreatedAt = m.CreatedAt.UTC()
		}
		for _, bf := range m.Files {
			fi, err := root.Stat(filepath.Join(id, bf.Name))
			if err != nil || !fi.Mode().IsRegular() || fi.Size() != bf.Size {
				set.Complete = false
			}
		}
		if set.Complete {
			set.Files = m.Files
			return set
		}
	}
	dir, err := root.Open(id)
	if err != nil {
		return set
	}
	defer func() { _ = dir.Close() }()
	entries, _ := dir.ReadDir(-1)
	for _, e := range entries {
		if !e.Type().IsRegular() || !protocol.ValidFileName(e.Name()) || e.Name() == ManifestName {
			continue
		}
		if fi, err := e.Info(); err == nil {
			set.Files = append(set.Files, protocol.BackupFile{Name: e.Name(), Size: fi.Size()})
		}
	}
	return set
}

func (s *Server) handleBackupList(w http.ResponseWriter, r *http.Request) {
	root, err := s.openBackups()
	if err != nil {
		s.fileFailure(w, "backups list", err)
		return
	}
	defer func() { _ = root.Close() }()
	dir, err := root.Open(".")
	if err != nil {
		s.fileFailure(w, "backups list", err)
		return
	}
	entries, err := dir.ReadDir(-1)
	_ = dir.Close()
	if err != nil {
		s.fileFailure(w, "backups list", err)
		return
	}
	sets := []protocol.BackupSet{}
	for _, e := range entries {
		if !e.IsDir() || !validBackupID(e.Name()) {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		sets = append(sets, backupSet(root, e.Name(), fi.ModTime()))
	}
	slices.SortFunc(sets, func(a, b protocol.BackupSet) int {
		if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	writeJSON(w, http.StatusOK, sets)
}

func (s *Server) handleBackupExport(w http.ResponseWriter, r *http.Request) {
	id, file := r.PathValue("id"), r.PathValue("file")
	if !validBackupID(id) || !protocol.ValidFileName(file) {
		writeError(w, http.StatusBadRequest, protocol.CodeInvalidParams, "invalid backup id or file name")
		return
	}
	root, err := s.openBackups()
	if err != nil {
		s.fileFailure(w, "backup export", err)
		return
	}
	defer func() { _ = root.Close() }()
	m, err := readManifest(root, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "backup set has no valid manifest")
		return
	}
	i := slices.IndexFunc(m.Files, func(bf protocol.BackupFile) bool { return bf.Name == file })
	if i < 0 {
		writeError(w, http.StatusNotFound, "not_found", "file is not listed in the backup manifest")
		return
	}
	f, err := root.Open(filepath.Join(id, file))
	if err != nil {
		s.fileFailure(w, "backup export", err)
		return
	}
	defer func() { _ = f.Close() }()
	w.Header().Set(protocol.BackupSHA256Header, m.Files[i].SHA256)
	serveFile(w, f)
}

func (s *Server) handleBackupDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validBackupID(id) {
		writeError(w, http.StatusBadRequest, protocol.CodeInvalidParams, "invalid backup id")
		return
	}
	root, err := s.openBackups()
	if err != nil {
		s.fileFailure(w, "backup delete", err)
		return
	}
	defer func() { _ = root.Close() }()
	fi, err := root.Lstat(id)
	if err != nil {
		s.fileFailure(w, "backup delete", err)
		return
	}
	if !fi.IsDir() {
		writeError(w, http.StatusNotFound, "not_found", "")
		return
	}
	if err := root.RemoveAll(id); err != nil && !errors.Is(err, fs.ErrNotExist) {
		s.fileFailure(w, "backup delete", err)
		return
	}
	s.logf("backup %s deleted", id)
	w.WriteHeader(http.StatusNoContent)
}
