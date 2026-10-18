package backups

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/client"
	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
)

// RestoreAgent is the part of the engine client Restore uses.
type RestoreAgent interface {
	PutFile(ctx context.Context, area protocol.FileArea, name string, r io.Reader, size int64) error
	DeleteFile(ctx context.Context, area protocol.FileArea, name string) error
	Op(ctx context.Context, op protocol.Op, result any, onFrame client.FrameFunc) error
}

// Restore verifies the exported set in setDir, uploads it with the recovery
// key to the engine's restore-input area, runs db.restore and removes the
// uploaded inputs again, whatever the outcome. The clinic is down while the
// restore runs and is replaced by the set's database and files.
func Restore(ctx context.Context, ag RestoreAgent, setDir string, recoveryKey []byte, onFrame client.FrameFunc) error {
	id := filepath.Base(setDir)
	m, err := ReadManifest(setDir, id)
	if err != nil {
		return err
	}
	if err := VerifySet(setDir, m); err != nil {
		return fmt.Errorf("backup %s is damaged: %w", id, err)
	}
	var uploaded []string
	defer func() {
		// Best effort even when ctx was cancelled: the key must not stay in
		// the engine.
		for _, name := range uploaded {
			_ = ag.DeleteFile(context.WithoutCancel(ctx), protocol.FileAreaRestoreInput, name)
		}
	}()
	put := func(name string, r io.Reader, size int64) error {
		uploaded = append(uploaded, name)
		if err := ag.PutFile(ctx, protocol.FileAreaRestoreInput, name, r, size); err != nil {
			return fmt.Errorf("upload %s: %w", name, err)
		}
		return nil
	}
	for _, bf := range m.Files {
		f, err := os.Open(filepath.Join(setDir, bf.Name))
		if err != nil {
			return err
		}
		err = put(bf.Name, f, bf.Size)
		_ = f.Close()
		if err != nil {
			return err
		}
	}
	mb, err := os.ReadFile(filepath.Join(setDir, protocol.BackupManifestName))
	if err != nil {
		return err
	}
	if err := put(protocol.BackupManifestName, bytes.NewReader(mb), int64(len(mb))); err != nil {
		return err
	}
	if err := put(protocol.BackupKeyName, bytes.NewReader(recoveryKey), int64(len(recoveryKey))); err != nil {
		return err
	}
	return ag.Op(ctx, protocol.DBRestore{BackupID: id}, nil, onFrame)
}

// FindSet resolves what the user picked to restore from: a set folder
// itself (it holds manifest.json), or a backup folder (the host backup
// folder, the extra copy, or either copied to a new computer) and an id in
// it, or its newest set when id is "".
func FindSet(path, id string) (string, error) {
	if _, err := os.Stat(filepath.Join(path, protocol.BackupManifestName)); err == nil {
		if id != "" && id != filepath.Base(path) {
			return "", fmt.Errorf("%s is backup %s, not %s", path, filepath.Base(path), id)
		}
		return path, nil
	}
	if id != "" {
		if err := (protocol.DBRestore{BackupID: id}).Validate(); err != nil {
			return "", err
		}
		return filepath.Join(path, id), nil
	}
	sets, err := HostSets(path)
	if err != nil {
		return "", err
	}
	if len(sets) == 0 {
		return "", fmt.Errorf("no backup in %s", path)
	}
	return sets[0].Path, nil
}
