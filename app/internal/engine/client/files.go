package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
)

// backupSHA256Header carries the manifest checksum of an exported backup file.
const backupSHA256Header = "X-Care-SHA256"

func fileRef(area protocol.FileArea, name string) (string, error) {
	var fe []protocol.FieldError
	if !area.Valid() {
		fe = append(fe, protocol.FieldError{Field: "area", Msg: "unknown file area"})
	}
	if !protocol.ValidFileName(name) {
		fe = append(fe, protocol.FieldError{Field: "name", Msg: "must match ^[a-zA-Z0-9._-]{1,64}$ and not start with '.'"})
	}
	if fe != nil {
		return "", &protocol.ValidationError{Errors: fe}
	}
	return "/v1/files" + pathEscape(string(area), name), nil
}

// PutFile atomically uploads r as area/name, replacing any existing file.
// size is the exact length of r, or -1 when unknown; a known size lets the
// agent reject an oversized upload (ErrTooLarge) before any data is sent.
func (c *Client) PutFile(ctx context.Context, area protocol.FileArea, name string, r io.Reader, size int64) error {
	path, err := fileRef(area, name)
	if err != nil {
		return err
	}
	resp, err := c.do(ctx, http.MethodPut, path, r, size, "application/octet-stream", http.StatusNoContent)
	if err != nil {
		return err
	}
	closeBody(resp)
	return nil
}

// GetFile opens area/name for reading; the caller closes it.
func (c *Client) GetFile(ctx context.Context, area protocol.FileArea, name string) (io.ReadCloser, error) {
	path, err := fileRef(area, name)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(ctx, http.MethodGet, path, nil, 0, "", http.StatusOK)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// DeleteFile removes area/name; it returns ErrNotFound when it does not exist.
func (c *Client) DeleteFile(ctx context.Context, area protocol.FileArea, name string) error {
	path, err := fileRef(area, name)
	if err != nil {
		return err
	}
	return c.doJSON(ctx, http.MethodDelete, path, nil, nil, http.StatusNoContent)
}

// ListBackups returns the backup sets in the VM, newest first.
func (c *Client) ListBackups(ctx context.Context) ([]protocol.BackupSet, error) {
	var sets []protocol.BackupSet
	err := c.doJSON(ctx, http.MethodGet, "/v1/backups", nil, &sets, http.StatusOK)
	return sets, err
}

func validBackupID(id string) error {
	if err := (protocol.DBRestore{BackupID: id}).Validate(); err != nil || id == "." || id == ".." {
		return &protocol.ValidationError{Errors: []protocol.FieldError{{Field: "backup_id", Msg: "invalid backup id"}}}
	}
	return nil
}

// ExportBackupFile copies file of backup set id to w and verifies its length
// and the SHA-256 recorded in the set's manifest. On ErrChecksumMismatch (or
// any other error) w may hold partial or corrupt data and must be discarded.
// It returns the number of bytes written.
func (c *Client) ExportBackupFile(ctx context.Context, id, file string, w io.Writer) (int64, error) {
	if err := validBackupID(id); err != nil {
		return 0, err
	}
	if !protocol.ValidFileName(file) {
		return 0, &protocol.ValidationError{Errors: []protocol.FieldError{{Field: "file", Msg: "invalid file name"}}}
	}
	resp, err := c.do(ctx, http.MethodGet, "/v1/backups"+pathEscape(id, file), nil, 0, "", http.StatusOK)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	want := resp.Header.Get(backupSHA256Header)
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, h), resp.Body)
	if err != nil {
		if ctx.Err() != nil {
			return n, ctx.Err()
		}
		return n, fmt.Errorf("export backup %s/%s: %w", id, file, err)
	}
	if resp.ContentLength >= 0 && n != resp.ContentLength {
		return n, fmt.Errorf("export backup %s/%s: got %d of %d bytes", id, file, n, resp.ContentLength)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return n, fmt.Errorf("export backup %s/%s: %w: manifest %q, got %q", id, file, ErrChecksumMismatch, want, got)
	}
	return n, nil
}

// DeleteBackup removes backup set id.
func (c *Client) DeleteBackup(ctx context.Context, id string) error {
	if err := validBackupID(id); err != nil {
		return err
	}
	return c.doJSON(ctx, http.MethodDelete, "/v1/backups"+pathEscape(id), nil, nil, http.StatusNoContent)
}
