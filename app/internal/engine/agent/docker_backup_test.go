package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
)

func sumHex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// writeRestoreInput writes a restore-input area for set id.
func writeRestoreInput(t *testing.T, id string, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	m := Manifest{ID: id, CreatedAt: time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)}
	for _, name := range []string{"database.dump.enc", "files.tar.gz.enc"} {
		m.Files = append(m.Files, protocol.BackupFile{Name: name, Size: int64(len(name)), SHA256: sumHex(name)})
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := json.Marshal(m)
	files[ManifestName] = files[ManifestName] + string(b)
	if _, ok := files[protocol.BackupKeyName]; !ok {
		files[protocol.BackupKeyName] = "-----BEGIN RSA PRIVATE KEY-----"
	}
	for name, content := range files {
		if content == "-" {
			_ = os.Remove(filepath.Join(dir, name))
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestVerifyRestoreInput(t *testing.T) {
	const id = "daily-20261018T000000Z"
	if err := VerifyRestoreInput(writeRestoreInput(t, id, map[string]string{}), id); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		id    string
		files map[string]string
	}{
		"other set":       {"manual-20261018T050403Z", map[string]string{}},
		"corrupt file":    {id, map[string]string{"files.tar.gz.enc": "files.tar.gz.enX"}},
		"truncated file":  {id, map[string]string{"database.dump.enc": "data"}},
		"missing file":    {id, map[string]string{"database.dump.enc": "-"}},
		"missing key":     {id, map[string]string{protocol.BackupKeyName: "-"}},
		"empty key":       {id, map[string]string{protocol.BackupKeyName: ""}},
		"broken manifest": {id, map[string]string{ManifestName: "{"}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := VerifyRestoreInput(writeRestoreInput(t, id, tc.files), tc.id); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestDockerRestore(t *testing.T) {
	const id = "daily-20261018T000000Z"
	stop := c("stop", "backend", "celery-worker", "celery-beat", "minio")
	restore := c("run", "--rm", "--no-deps", "-T", "backup", "restore", id)
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		d, r := newDocker(nil)
		d.RestoreInputDir = writeRestoreInput(t, id, map[string]string{})
		if err := d.DBRestore(ctx, protocol.DBRestore{BackupID: id}, &recEmitter{}); err != nil {
			t.Fatal(err)
		}
		expectArgv(t, r, stop, restore, c("run", "--rm", "-T", "migrate"),
			c("up", "--detach", "--remove-orphans", "--wait", "--wait-timeout", "900"))
	})

	t.Run("failure restarts CARE", func(t *testing.T) {
		d, r := newDocker(func(cmd Command) fakeRun {
			if slices.Contains(cmd.Args, "restore") {
				return fakeRun{stderr: "[restore] ERROR: decrypting failed\n", err: errors.New("exit 1")}
			}
			return fakeRun{}
		})
		d.RestoreInputDir = writeRestoreInput(t, id, map[string]string{})
		err := d.DBRestore(ctx, protocol.DBRestore{BackupID: id}, &recEmitter{})
		if err == nil || !strings.Contains(err.Error(), "decrypting failed") {
			t.Fatalf("err = %v", err)
		}
		expectArgv(t, r, stop, restore, c("up", "--detach", "--remove-orphans"))
	})

	t.Run("bad input runs nothing", func(t *testing.T) {
		d, r := newDocker(nil)
		d.RestoreInputDir = writeRestoreInput(t, id, map[string]string{"database.dump.enc": "x"})
		err := d.DBRestore(ctx, protocol.DBRestore{BackupID: id}, &recEmitter{})
		var oe *protocol.OpError
		if !errors.As(err, &oe) || oe.Code != protocol.CodeInvalidParams || len(r.cmds) != 0 {
			t.Fatalf("err = %v, cmds %q", err, r.argv())
		}
	})
}
