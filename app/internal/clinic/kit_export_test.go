package clinic

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/enginetest"
	"github.com/ohcnetwork/care_desktop/app/internal/release"
	"github.com/ohcnetwork/care_desktop/app/internal/storage"
)

func transferFixture(t *testing.T) (*Clinic, *enginetest.Agent) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("PATH", filepath.Join(root, "no-desktop-tools"))
	agent := enginetest.New(t, func(c enginetest.Call) enginetest.Result {
		if c.Operation == "compose" {
			return enginetest.Result{Stdout: "backend\n", Stderr: "guest diagnostic\n"}
		}
		return enginetest.Result{}
	})
	e := &Clinic{InstallDir: root, BackupDir: filepath.Join(root, "export"), Agent: agent.Client, Pins: &release.Pins{}}
	for name, data := range map[string]string{
		"docker-compose.yml": "services: {}\n", "backend.env": "TOKEN='private'\n",
		"frontend.env": "", "secrets.env": "POSTGRES_PASSWORD='private'\n",
		"keys/backup-cert.pem": "public certificate", "scripts/pitr-stage.sh": "#!/bin/sh\n",
		"backend-plugins.Dockerfile": "FROM fixture\n",
	} {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return e, agent
}

func TestRuntimeTransfersWithoutDesktopDocker(t *testing.T) {
	e, agent := transferFixture(t)
	for _, op := range []string{"ps", "logs", "pull", "down", "up"} {
		got, err := e.capture("compose", op)
		if err != nil || got != "backend" {
			t.Fatalf("compose %s stdout/stderr: %q, %v", op, got, err)
		}
	}
	if err := e.Runner().AgentBuild(filepath.Join(e.InstallDir, "backend-plugins.Dockerfile"), t.TempDir(), "-t", "fixture"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.capture("image", "inspect", "fixture"); err != nil {
		t.Fatal(err)
	}
	if err := e.run(nil, "compose", "exec", "-T", "backend", "manage.py", "check"); err != nil {
		t.Fatal(err)
	}
	for name, mode := range map[string]os.FileMode{
		"backend.env": 0o600, "secrets.env": 0o600,
		"keys/backup-cert.pem": 0o644, "scripts/pitr-stage.sh": 0o644,
		"keys": 0o755, "scripts": 0o755, "setup": 0o755, "minio": 0o755,
	} {
		info, err := os.Stat(agent.Path(path.Join(AgentClinicDir, name)))
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("%s: %v, %v; want %o", name, info, err, mode)
		}
	}
	if err := os.WriteFile(filepath.Join(e.InstallDir, "backend.env"), []byte("TOKEN='changed'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.capture("compose", "ps"); err != nil {
		t.Fatal(err)
	}
	if got := string(agent.ReadFile(t, path.Join(AgentClinicDir, "backend.env"))); got != "TOKEN='changed'\n" {
		t.Fatalf("edited generated configuration not transferred: %q", got)
	}
	for _, c := range agent.Calls() {
		if c.Dir != AgentClinicDir {
			t.Fatalf("operation uses desktop working directory: %+v", c)
		}
		for _, arg := range c.Args {
			if strings.Contains(arg, e.InstallDir) {
				t.Fatalf("desktop path leaked into runtime operation: %+v", c)
			}
		}
	}
}

func publishExportSet(t *testing.T, a *enginetest.Agent, stamp string) []byte {
	t.Helper()
	data := []byte("verified-" + stamp)
	name := "care-" + stamp + ".dump.enc"
	sum := sha256.Sum256(data)
	m := storage.Manifest{Version: 1, Set: stamp, CreatedAt: time.Now().Unix(), Checks: map[string]string{},
		Files: []storage.ManifestFileEntry{{Path: name, Bytes: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}}}
	for _, check := range storage.RequiredChecks {
		m.Checks[check] = "ok"
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	a.WriteFile(t, path.Join(AgentBackupDir, name), data)
	a.WriteFile(t, path.Join(AgentBackupDir, storage.ManifestFile), b)
	return b
}

func TestAsynchronousBackupAndWALExportsStayCurrent(t *testing.T) {
	e, agent := transferFixture(t)
	ctx := context.Background()
	publishExportSet(t, agent, "20260101-010101")
	wal := "000000010000000000000001"
	data := []byte("encrypted change segment")
	sum := sha256.Sum256(data)
	agent.WriteFile(t, path.Join(AgentBackupDir, "wal", wal+".gz.enc"), data)
	agent.WriteFile(t, path.Join(AgentBackupDir, "wal", wal+".sha256"), []byte("sha256="+hex.EncodeToString(sum[:])+"\n"))
	agent.WriteFile(t, path.Join(agentStateDir, storage.WALStatusFile), []byte("state=ok\nsaved_through=1767229200\n"))
	if err := e.ExportBackups(ctx); err != nil {
		t.Fatal(err)
	}
	previous := filepath.Join(e.BackupDir, "care-20260101-010101.dump.enc")
	newManifest := publishExportSet(t, agent, "20260102-010101")
	if err := os.Remove(agent.Path(path.Join(AgentBackupDir, "care-20260101-010101.dump.enc"))); err != nil {
		t.Fatal(err)
	}
	agent.WriteFile(t, path.Join(agentStateDir, storage.WALStatusFile), []byte("state=ok\nsaved_through=1767315600\n"))
	if err := e.ExportBackups(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(previous); !os.IsNotExist(err) {
		t.Fatalf("previous set not pruned after complete verified export: %v", err)
	}
	for name, want := range map[string]string{
		storage.ManifestFile: string(newManifest), "wal/" + wal + ".gz.enc": string(data),
	} {
		b, err := os.ReadFile(filepath.Join(e.BackupDir, filepath.FromSlash(name)))
		if err != nil || string(b) != want {
			t.Fatalf("%s: %q, %v", name, b, err)
		}
	}
	state, err := os.ReadFile(filepath.Join(e.InstallDir, storage.StateDirName, storage.WALStatusFile))
	if err != nil || !strings.Contains(string(state), "1767315600") {
		t.Fatalf("same-size asynchronous WAL status update missed: %q, %v", state, err)
	}
}

func TestFailedBackupExportKeepsPreviousVerifiedRecord(t *testing.T) {
	for _, failure := range []string{"missing", "corrupt", "wal"} {
		t.Run(failure, func(t *testing.T) {
			e, a := transferFixture(t)
			previous := publishExportSet(t, a, "20260101-010101")
			if err := e.ExportBackups(context.Background()); err != nil {
				t.Fatal(err)
			}

			publishExportSet(t, a, "20260102-010101")
			next := path.Join(AgentBackupDir, "care-20260102-010101.dump.enc")
			switch failure {
			case "missing":
				if err := os.Remove(a.Path(next)); err != nil {
					t.Fatal(err)
				}
			case "corrupt":
				a.WriteFile(t, next, []byte("damaged!-20260102-010101"))
			case "wal":
				a.WriteFile(t, path.Join(AgentBackupDir, "wal/000000010000000000000001.gz.enc"), []byte("bad"))
				a.WriteFile(t, path.Join(AgentBackupDir, "wal/000000010000000000000001.sha256"), []byte("sha256="+strings.Repeat("a", 64)+"\n"))
			}
			if err := e.ExportBackups(context.Background()); err == nil {
				t.Fatal("incomplete/corrupt guest backup exported successfully")
			}
			got, err := os.ReadFile(filepath.Join(e.BackupDir, storage.ManifestFile))
			if err != nil || string(got) != string(previous) {
				t.Fatalf("previous verified record changed: %q, %v", got, err)
			}
			if _, err := os.Stat(filepath.Join(e.BackupDir, "care-20260101-010101.dump.enc")); err != nil {
				t.Fatalf("previous verified set removed after failed transfer: %v", err)
			}
		})
	}
}

type manifestChangeTransport struct {
	http.RoundTripper
	reads  int
	change func()
}

func (tr *manifestChangeTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method == http.MethodGet && r.URL.Query().Get("path") == path.Join(AgentBackupDir, storage.ManifestFile) {
		tr.reads++
		if tr.reads == 2 {
			tr.change()
		}
	}
	return tr.RoundTripper.RoundTrip(r)
}

func TestBackupRecordRotationCannotCommitPartialExport(t *testing.T) {
	e, a := transferFixture(t)
	previous := publishExportSet(t, a, "20260101-010101")
	if err := e.ExportBackups(context.Background()); err != nil {
		t.Fatal(err)
	}
	publishExportSet(t, a, "20260102-010101")
	base := a.Client.HTTP.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	a.Client.HTTP = &http.Client{Transport: &manifestChangeTransport{RoundTripper: base, change: func() {
		publishExportSet(t, a, "20260103-010101")
	}}}
	if err := e.ExportBackups(context.Background()); err == nil || !strings.Contains(err.Error(), "changed during export") {
		t.Fatalf("mid-export manifest rotation accepted: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(e.BackupDir, storage.ManifestFile))
	if err != nil || string(got) != string(previous) {
		t.Fatalf("previous record was replaced: %q, %v", got, err)
	}
}

func TestExportWaitRespectsCancellation(t *testing.T) {
	e, _ := transferFixture(t)
	exportGate <- struct{}{}
	defer func() { <-exportGate }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := e.ExportBackups(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("export lock ignored cancellation: %v", err)
	}
	// A large backup download must not hold up the small health-state refresh.
	if err := e.ExportBackupState(context.Background()); err != nil {
		t.Fatal(err)
	}
}
