package agent

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
)

// fileRef validates the {area}/{name} path values; it writes a 400 and
// returns false when either is invalid.
func fileRef(w http.ResponseWriter, r *http.Request) (protocol.FileArea, string, bool) {
	area, name := protocol.FileArea(r.PathValue("area")), r.PathValue("name")
	var fe []protocol.FieldError
	if !area.Valid() {
		fe = append(fe, protocol.FieldError{Field: "area", Msg: "unknown file area"})
	}
	if !protocol.ValidFileName(name) {
		fe = append(fe, protocol.FieldError{Field: "name", Msg: "must match ^[a-zA-Z0-9._-]{1,64}$ and not start with '.'"})
	}
	if fe != nil {
		writeJSON(w, http.StatusBadRequest, &protocol.ValidationError{Errors: fe})
		return "", "", false
	}
	return area, name, true
}

// openArea opens (creating if needed) the directory of area as an os.Root,
// which also refuses symlinks escaping it.
func (s *Server) openArea(area protocol.FileArea) (*os.Root, error) {
	dir := filepath.Join(s.cfg.FilesRoot, string(area))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return os.OpenRoot(dir)
}

func (s *Server) readAreaFile(area protocol.FileArea, name string) ([]byte, error) {
	root, err := s.openArea(area)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, s.cfg.MaxFileBytes))
}

func (s *Server) areaLimit(area protocol.FileArea) int64 {
	if area == protocol.FileAreaRestoreInput {
		return s.cfg.MaxRestoreInputBytes
	}
	return s.cfg.MaxFileBytes
}

func (s *Server) handleFilePut(w http.ResponseWriter, r *http.Request) {
	area, name, ok := fileRef(w, r)
	if !ok {
		return
	}
	limit := s.areaLimit(area)
	if r.ContentLength > limit {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "file exceeds "+strconv.FormatInt(limit, 10)+" bytes")
		return
	}
	root, err := s.openArea(area)
	if err != nil {
		s.fileFailure(w, "put", err)
		return
	}
	defer func() { _ = root.Close() }()

	var rnd [8]byte
	_, _ = rand.Read(rnd[:])
	tmp := ".upload-" + hex.EncodeToString(rnd[:])
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		s.fileFailure(w, "put", err)
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = f.Close()
			_ = root.Remove(tmp)
		}
	}()
	if _, err := io.Copy(f, http.MaxBytesReader(w, r.Body, limit)); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(w, http.StatusRequestEntityTooLarge, "too_large", "file exceeds "+strconv.FormatInt(limit, 10)+" bytes")
			return
		}
		s.fileFailure(w, "put", err)
		return
	}
	if err := f.Sync(); err != nil {
		s.fileFailure(w, "put", err)
		return
	}
	if err := f.Close(); err != nil {
		s.fileFailure(w, "put", err)
		return
	}
	committed = true
	if err := root.Rename(tmp, name); err != nil {
		_ = root.Remove(tmp)
		s.fileFailure(w, "put", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleFileGet(w http.ResponseWriter, r *http.Request) {
	area, name, ok := fileRef(w, r)
	if !ok {
		return
	}
	root, err := s.openArea(area)
	if err != nil {
		s.fileFailure(w, "get", err)
		return
	}
	defer func() { _ = root.Close() }()
	f, err := root.Open(name)
	if err != nil {
		s.fileFailure(w, "get", err)
		return
	}
	defer func() { _ = f.Close() }()
	serveFile(w, f)
}

func (s *Server) handleFileDelete(w http.ResponseWriter, r *http.Request) {
	area, name, ok := fileRef(w, r)
	if !ok {
		return
	}
	root, err := s.openArea(area)
	if err != nil {
		s.fileFailure(w, "delete", err)
		return
	}
	defer func() { _ = root.Close() }()
	if err := root.Remove(name); err != nil {
		s.fileFailure(w, "delete", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// serveFile streams a regular file with its length.
func serveFile(w http.ResponseWriter, f *os.File) {
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		writeError(w, http.StatusNotFound, "not_found", "")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(fi.Size(), 10))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, f)
}

func (s *Server) fileFailure(w http.ResponseWriter, what string, err error) {
	if errors.Is(err, fs.ErrNotExist) {
		writeError(w, http.StatusNotFound, "not_found", "")
		return
	}
	s.logf("files %s: %v", what, err)
	writeError(w, http.StatusInternalServerError, "io_error", "")
}
