package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
)

// DefaultRestoreInputDir is the restore-input file area; compose mounts it
// read-only into the backup service at /restore-input.
const DefaultRestoreInputDir = DefaultFilesRoot + "/" + string(protocol.FileAreaRestoreInput)

// restoreStopped are stopped while a restore replaces the database and the
// uploaded files, so nothing writes to either.
var restoreStopped = []protocol.Service{
	protocol.ServiceBackend, protocol.ServiceCeleryWorker, protocol.ServiceCeleryBeat, protocol.ServiceMinio,
}

func (d *DockerExecutor) restoreInputDir() string {
	if d.RestoreInputDir != "" {
		return d.RestoreInputDir
	}
	return DefaultRestoreInputDir
}

// BackupRun asks the running backup service for a set named
// manual-<UTC timestamp> and returns its id once the set is complete.
func (d *DockerExecutor) BackupRun(ctx context.Context, em Emitter) (protocol.BackupRunResult, error) {
	if d.noStack() {
		return protocol.BackupRunResult{}, errNotInstalled
	}
	id := "manual-" + d.now().UTC().Format("20060102T150405Z")
	em.Progress(protocol.Progress{Service: protocol.ServiceBackup, State: "running", Message: id})
	_, err := d.stream(ctx, em, "backup", protocol.ServiceBackup, Command{
		Args: compose("exec", "-T", string(protocol.ServiceBackup), "/bin/sh", "/backup.sh", "once", id),
		Dir:  ComposeDir,
	})
	if err != nil {
		return protocol.BackupRunResult{}, err
	}
	em.Progress(protocol.Progress{Service: protocol.ServiceBackup, State: "done", Message: id})
	return protocol.BackupRunResult{BackupID: id}, nil
}

// DBRestore checks the uploaded set against its manifest, stops everything
// that writes to the database or the uploaded files, has the backup service
// decrypt, validate and swap in the set, then migrates and starts CARE. When
// the restore fails before the swap, CARE is started again on its old data.
func (d *DockerExecutor) DBRestore(ctx context.Context, op protocol.DBRestore, em Emitter) error {
	if d.noStack() {
		return errNotInstalled
	}
	em.Progress(protocol.Progress{State: "verifying", Message: op.BackupID})
	if err := VerifyRestoreInput(d.restoreInputDir(), op.BackupID); err != nil {
		return &protocol.OpError{Code: protocol.CodeInvalidParams, Message: "restore input: " + err.Error()}
	}
	em.Progress(protocol.Progress{State: "stopping", Message: "stopping CARE"})
	if err := d.StackStop(ctx, protocol.StackStop{Services: restoreStopped}, em); err != nil {
		return err
	}
	em.Progress(protocol.Progress{Service: protocol.ServiceBackup, State: "restoring", Message: op.BackupID})
	_, err := d.stream(ctx, em, "restore", protocol.ServiceBackup, Command{
		Args: compose("run", "--rm", "--no-deps", "-T", string(protocol.ServiceBackup), "restore", op.BackupID),
		Dir:  ComposeDir,
	})
	if err != nil {
		// Bring CARE back on whatever data it has; the script logged which
		// step failed and whether anything was replaced.
		if upErr := d.StackUp(context.WithoutCancel(ctx), protocol.StackUp{}, em); upErr != nil {
			em.Log(protocol.LogLine{Stream: "stderr", Line: "restarting CARE after the failed restore: " + upErr.Error()})
		}
		return err
	}
	if err := d.RunMigrations(ctx, em); err != nil {
		return err
	}
	return d.StackUp(ctx, protocol.StackUp{Wait: true, TimeoutSec: protocol.MaxTimeoutSec}, em)
}

// VerifyRestoreInput checks that dir holds the manifest of set id, every file
// it lists with the listed size and SHA-256, and the recovery key.
func VerifyRestoreInput(dir, id string) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	f, err := root.Open(ManifestName)
	if err != nil {
		return fmt.Errorf("%s: %w", ManifestName, err)
	}
	m, err := decodeManifest(f, id)
	_ = f.Close()
	if err != nil {
		return fmt.Errorf("%s: %w", ManifestName, err)
	}
	if m.ID == "" {
		return errors.New(ManifestName + " does not name its backup")
	}
	for _, bf := range m.Files {
		if err := verifyFile(root, bf); err != nil {
			return err
		}
	}
	fi, err := root.Lstat(protocol.BackupKeyName)
	if err != nil {
		return fmt.Errorf("%s: %w", protocol.BackupKeyName, err)
	}
	if !fi.Mode().IsRegular() || fi.Size() == 0 {
		return errors.New(protocol.BackupKeyName + " is empty or not a file")
	}
	return nil
}

func verifyFile(root *os.Root, bf protocol.BackupFile) error {
	f, err := root.Open(filepath.Clean(bf.Name))
	if err != nil {
		return fmt.Errorf("%s: %w", bf.Name, err)
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() || fi.Size() != bf.Size {
		return fmt.Errorf("%s: size %d, manifest says %d", bf.Name, fi.Size(), bf.Size)
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("%s: %w", bf.Name, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != bf.SHA256 {
		return fmt.Errorf("%s: sha256 %s, manifest says %s", bf.Name, got, bf.SHA256)
	}
	return nil
}
