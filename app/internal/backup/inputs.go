package backup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path"
	"path/filepath"
	"time"
)

// Guest locations the CARE agent owns. Containers only ever bind these: host
// files reach them through explicit uploads, never through host path binds.
const (
	DefaultAgentBackupDir = "/var/lib/care/backups"
	DefaultAgentDir       = "/var/lib/care/clinic"
	restoreInputsDir      = "/var/lib/care/restore-inputs"
)

func (s *Store) agentBackupDir() string {
	if s.AgentBackupDir != "" {
		return s.AgentBackupDir
	}
	return DefaultAgentBackupDir
}

func (s *Store) agentDir() string {
	if s.AgentDir != "" {
		return s.AgentDir
	}
	return DefaultAgentDir
}

// restoreInputs are host files uploaded for one restore step, in a unique
// guest directory that is removed on every outcome.
type restoreInputs struct {
	s       *Store
	dir     string
	key     string
	written []string
}

func (s *Store) newRestoreInputs() (*restoreInputs, error) {
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return nil, err
	}
	return &restoreInputs{s: s, dir: path.Join(restoreInputsDir, hex.EncodeToString(id))}, nil
}

// add uploads local as name inside the input directory.
func (in *restoreInputs) add(local, name string) (string, error) {
	guest := path.Join(in.dir, name)
	if path.Dir(guest) != in.dir {
		return "", fmt.Errorf("invalid restore input name %q", name)
	}
	in.written = append(in.written, guest)
	if err := in.s.run.AgentClient().PutMode(in.s.run.Context(), local, guest, 0o600); err != nil {
		return "", fmt.Errorf("could not send %s to the CARE engine: %w", filepath.Base(local), err)
	}
	return guest, nil
}

// addKey uploads the recovery key under a name of its own, so a backup file
// can never be mistaken for it.
func (in *restoreInputs) addKey(local string) (string, error) {
	guest, err := in.add(local, "recovery-key.pem")
	in.key = guest
	return guest, err
}

// remove deletes every uploaded input, including partial uploads, and then
// the directory. Removal uses a fresh context so a cancelled restore still
// cleans the recovery key out of the engine.
func (in *restoreInputs) remove() {
	client := in.s.run.AgentClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	failed := false
	for _, guest := range in.written {
		if err := client.Remove(ctx, guest); err != nil {
			failed = true
			in.s.logln("warning: could not remove restore input " + path.Base(guest) + " from the CARE engine: " + err.Error())
		}
	}
	if !failed {
		if err := client.Remove(ctx, in.dir); err != nil {
			in.s.logln("warning: could not remove restore input directory: " + err.Error())
		}
	}
}
