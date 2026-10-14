package clinic

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/ohcnetwork/care_desktop/app/internal/engine"
)

// Guest folders the agent keeps on the engine's own disk. The desktop
// transfers inputs into them explicitly; nothing on this computer is shared.
const (
	AgentClinicDir = "/var/lib/care/clinic"
	AgentBackupDir = "/var/lib/care/backups"
	agentStateDir  = AgentClinicDir + "/backup-state"
)

// kitFiles and kitDirs are every bind and env_file input of
// deployments/docker-compose.yml, relative to the install folder. backup-state
// is guest-owned and exported, never uploaded.
var (
	kitFiles = []string{"docker-compose.yml", ".env", "backend.env", "frontend.env", "secrets.env", "Caddyfile", "keys/backup-cert.pem"}
	kitDirs  = []string{"scripts", "setup", "minio"}
)

// publicKit reports kit files read by unprivileged container users (postgres
// reads /keys and /scripts for WAL archiving), so the guest copy is
// world-readable even when the host copy is private. They hold no secret; the
// agent creates their leaf folders 0755 inside the private clinic folder.
func publicKit(rel string) bool {
	return rel == "keys/backup-cert.pem" || strings.HasPrefix(rel, "scripts/")
}

type kitKey struct {
	client *engine.Client
	dir    string
}

type kitUpload struct {
	sum   string
	stamp int64
}

var (
	kitGate     = make(chan struct{}, 1)
	kitUploaded = map[kitKey]map[string]kitUpload{}
)

func (e *Clinic) agentClient() *engine.Client {
	if e.Agent != nil {
		return e.Agent
	}
	return engine.Default
}

// localKit lists the clinic kit files present in the install folder.
func (e *Clinic) localKit() ([]string, error) {
	var rels []string
	for _, rel := range kitFiles {
		info, err := os.Lstat(filepath.Join(e.InstallDir, filepath.FromSlash(rel)))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("clinic file %s is not a regular file", rel)
		}
		rels = append(rels, rel)
	}
	for _, dir := range kitDirs {
		root := filepath.Join(e.InstallDir, dir)
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, os.ErrNotExist) && p == root {
					return fs.SkipAll
				}
				return err
			}
			if d.IsDir() {
				return nil
			}
			if !d.Type().IsRegular() {
				return fmt.Errorf("clinic file %s is not a regular file", p)
			}
			rel, err := filepath.Rel(e.InstallDir, p)
			if err != nil {
				return err
			}
			rels = append(rels, filepath.ToSlash(rel))
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return rels, nil
}

func fileSum(p string) (string, int64, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, err
}

// uploadKit makes the agent's clinic folder an exact copy of this install's
// compose inputs before any engine operation: changed files are replaced
// atomically by the agent, and files the kit no longer has are removed so an
// old setting can never linger. Guest-owned backup state is left alone.
func (e *Clinic) uploadKit(ctx context.Context) error {
	if _, err := os.Stat(filepath.Join(e.InstallDir, "docker-compose.yml")); err != nil {
		// Nothing installed yet (or it was removed): there is no kit to send.
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	client := e.agentClient()
	select {
	case kitGate <- struct{}{}:
		defer func() { <-kitGate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	rels, err := e.localKit()
	if err != nil {
		return err
	}
	for dir, mode := range map[string]os.FileMode{
		AgentClinicDir: 0o700, AgentBackupDir: 0o755,
		path.Join(AgentClinicDir, "scripts"): 0o755,
		path.Join(AgentClinicDir, "keys"):    0o755,
		path.Join(AgentClinicDir, "setup"):   0o755,
		path.Join(AgentClinicDir, "minio"):   0o755,
		agentStateDir:                        0o755,
	} {
		if err := client.Mkdir(ctx, dir, mode); err != nil {
			return fmt.Errorf("prepare engine directory %s: %w", dir, err)
		}
	}
	guest, err := client.List(ctx, AgentClinicDir)
	if err != nil {
		return fmt.Errorf("list the clinic files in the CARE engine: %w", err)
	}
	remote := make(map[string]engine.Entry, len(guest))
	for _, en := range guest {
		remote[en.Path] = en
	}
	key := kitKey{client, e.InstallDir}
	sent := kitUploaded[key]
	if sent == nil {
		sent = map[string]kitUpload{}
		kitUploaded[key] = sent
	}
	want := make(map[string]bool, len(rels))
	for _, rel := range rels {
		want[rel] = true
		local := filepath.Join(e.InstallDir, filepath.FromSlash(rel))
		sum, size, err := fileSum(local)
		if err != nil {
			return err
		}
		have, ok := remote[rel]
		if prev, done := sent[rel]; ok && done && prev.sum == sum && have.Size == size && prev.stamp == have.MtimeNs {
			continue
		}
		if err := putKitFile(ctx, client, local, path.Join(AgentClinicDir, rel), publicKit(rel)); err != nil {
			delete(sent, rel)
			return fmt.Errorf("copy %s into the CARE engine: %w", rel, err)
		}
		sent[rel] = kitUpload{sum: sum}
	}
	for rel := range remote {
		if want[rel] || rel == "backup-state" || strings.HasPrefix(rel, "backup-state/") {
			continue
		}
		if err := client.Remove(ctx, path.Join(AgentClinicDir, rel)); err != nil {
			return fmt.Errorf("remove the old %s from the CARE engine: %w", rel, err)
		}
		delete(sent, rel)
	}
	current, err := client.List(ctx, AgentClinicDir)
	if err != nil {
		return fmt.Errorf("confirm transferred clinic files: %w", err)
	}
	for _, en := range current {
		if cached, ok := sent[en.Path]; ok {
			cached.stamp = en.MtimeNs
			sent[en.Path] = cached
		}
	}
	return nil
}

// putKitFile changes public input modes only on the engine.
func putKitFile(ctx context.Context, client *engine.Client, local, remote string, public bool) error {
	if !public {
		return client.Put(ctx, local, remote)
	}
	return client.PutMode(ctx, local, remote, 0o644)
}
