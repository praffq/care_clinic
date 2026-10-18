package updates

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
	"github.com/ohcnetwork/care_desktop/app/internal/service/backups"
	"github.com/ohcnetwork/care_desktop/app/internal/service/control"
	"github.com/ohcnetwork/care_desktop/app/internal/stack"
)

// ClientAgent adapts the root service's control client: agent ops go
// through its relay, backups are taken by the engine's backup service and
// exported to the host's backup_dir, and restores upload a host set with the
// recovery key (backups.Restore).
type ClientAgent struct {
	Control *control.Client
	// RecoveryKey is the recovery key PEM; empty means the one saved by
	// ctl clinic install (stack.BackupRecoveryKey in the clinic state dir).
	RecoveryKey string
	Log         func(string)
}

func (a ClientAgent) frame(f protocol.Frame) {
	if a.Log != nil && f.Log != nil {
		a.Log("  " + f.Log.Line)
	}
}

func (a ClientAgent) Status(ctx context.Context) (protocol.Status, error) {
	return a.Control.Agent().Status(ctx)
}

func (a ClientAgent) Op(ctx context.Context, op protocol.Op, result any) error {
	return a.Control.Agent().Op(ctx, op, result, a.frame)
}

// Backup runs backup.run and copies the set to the host, where Restore
// finds it even after the engine prunes its copy.
func (a ClientAgent) Backup(ctx context.Context) (string, error) {
	var res protocol.BackupRunResult
	if err := a.Op(ctx, protocol.BackupRun{}, &res); err != nil {
		return "", err
	}
	if res.BackupID == "" {
		return "", errors.New("backup.run returned no id")
	}
	st, err := a.Control.ExportBackups(ctx)
	if err != nil {
		return "", fmt.Errorf("copy backup %s to the host: %w", res.BackupID, err)
	}
	dir := filepath.Join(st.Dir, res.BackupID)
	if _, err := backups.ReadManifest(dir, res.BackupID); err != nil {
		if st.LastError != "" {
			err = fmt.Errorf("%w (export: %s)", err, st.LastError)
		}
		return "", fmt.Errorf("backup %s is not on the host: %w", res.BackupID, err)
	}
	return res.BackupID, nil
}

func (a ClientAgent) Restore(ctx context.Context, id string) error {
	keyPath := a.RecoveryKey
	if keyPath == "" {
		state, err := stack.DefaultStateDir()
		if err != nil {
			return err
		}
		keyPath = filepath.Join(state, stack.BackupRecoveryKey)
	}
	key, err := stack.ReadRecoveryKey(keyPath)
	if err != nil {
		return err
	}
	cfg, err := a.Control.Config(ctx)
	if err != nil {
		return err
	}
	return backups.Restore(ctx, a.Control.Agent(), filepath.Join(cfg.BackupDir, id), key, a.frame)
}

// RuntimeStack adapts stack.Runtime; Kit, Host and Origins are what the
// env is rendered with.
type RuntimeStack struct {
	Runtime *stack.Runtime
	Kit     fs.FS
	Host    string
	Origins []string
	// Ping checks the clinic over HTTPS; nil means PingLocal.
	Ping func(ctx context.Context) error
}

// PingLocal checks that CARE answers https://localhost/ping/ with 200. The
// certificate is the clinic's own, so it is not verified.
func PingLocal(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://localhost/ping/", nil)
	if err != nil {
		return err
	}
	tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // the clinic's own local certificate
	defer tr.CloseIdleConnections()
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		return fmt.Errorf("nothing answering on :443: %w", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	return nil
}

func (s RuntimeStack) EnsureImages(ctx context.Context, images []stack.Image) error {
	return s.Runtime.Images(ctx, images)
}

func (s RuntimeStack) WriteEnv(ctx context.Context, images []stack.Image) error {
	return s.Runtime.Configure(ctx, s.Kit, s.Host, s.Origins, images)
}

// healthServices must be running and not unhealthy for the stack to count
// as healthy.
var healthServices = []protocol.Service{
	protocol.ServiceDB, protocol.ServiceRedis, protocol.ServiceBackend, protocol.ServiceCeleryWorker,
	protocol.ServiceCeleryBeat, protocol.ServiceFrontend, protocol.ServiceCaddy,
}

func (s RuntimeStack) Health(ctx context.Context) error {
	st, err := s.Runtime.Status(ctx)
	if err != nil {
		return fmt.Errorf("stack status: %w", err)
	}
	byName := map[protocol.Service]protocol.ServiceState{}
	for _, svc := range st.Services {
		byName[svc.Name] = svc
	}
	var bad []string
	for _, name := range healthServices {
		svc, ok := byName[name]
		switch {
		case !ok:
			bad = append(bad, string(name)+" missing")
		case svc.State != "running":
			bad = append(bad, string(name)+" "+svc.State)
		case svc.Health == "unhealthy" || svc.Health == "starting":
			bad = append(bad, string(name)+" "+svc.Health)
		}
	}
	if len(bad) > 0 {
		return errors.New("services not healthy: " + strings.Join(bad, ", "))
	}
	ping := s.Ping
	if ping == nil {
		ping = PingLocal
	}
	if err := ping(ctx); err != nil {
		return fmt.Errorf("CARE is not answering: %w", err)
	}
	return nil
}

// DBSize estimates the database from the newest complete backup set (its
// compressed dump understates the live database, hence the reserve in
// preflight); 0 when there is none.
func (s RuntimeStack) DBSize(ctx context.Context) (int64, error) {
	sets, err := s.Runtime.Service.Agent().ListBackups(ctx)
	if err != nil {
		return 0, err
	}
	var best *protocol.BackupSet
	for i := range sets {
		if b := &sets[i]; b.Complete && (best == nil || b.CreatedAt.After(best.CreatedAt)) {
			best = b
		}
	}
	if best == nil {
		return 0, nil
	}
	var n int64
	for _, f := range best.Files {
		n += f.Size
	}
	return n, nil
}
