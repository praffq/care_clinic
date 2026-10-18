package stack

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/client"
	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
	"github.com/ohcnetwork/care_desktop/app/internal/service/api"
)

// Service is the part of the host service's control client the runtime uses.
type Service interface {
	Config(ctx context.Context) (api.ServiceConfig, error)
	UpdateConfig(ctx context.Context, u api.ServiceConfigUpdate) (api.ServiceConfig, error)
	Agent(opts ...client.Option) *client.Client
}

// Runtime installs and drives the clinic stack through the service relay.
type Runtime struct {
	Service Service
	// StateDir keeps host-side clinic state (secrets.env, the backup
	// certificate and recovery key); empty means
	// DefaultStateDir.
	StateDir string
	// Log receives progress lines; nil discards them.
	Log func(string)
}

// DefaultStateDir is where the app and ctl keep host-side clinic state.
func DefaultStateDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "care-clinic", "clinic"), nil
}

func (r *Runtime) logf(format string, a ...any) {
	if r.Log != nil {
		r.Log(fmt.Sprintf(format, a...))
	}
}

// frames forwards op log lines to Log.
func (r *Runtime) frames(f protocol.Frame) {
	switch {
	case f.Log != nil:
		r.logf("  %s", f.Log.Line)
	case f.Progress != nil && f.Progress.Message != "":
		r.logf("  %s %s", f.Progress.State, f.Progress.Message)
	}
}

// Secrets returns the clinic secrets, creating them once. The host copy in
// StateDir is the source of truth; when it is missing (new host user, app
// data wiped) but the engine still has a clinic, its copy is adopted, since
// the database volume was initialised with that password.
func (r *Runtime) Secrets(ctx context.Context) (Secrets, error) {
	dir, err := r.stateDir()
	if err != nil {
		return Secrets{}, err
	}
	path := filepath.Join(dir, SecretsEnv)
	if s, ok, err := LoadSecrets(path); ok || err != nil {
		return s, err
	}
	s := NewSecrets()
	rc, err := r.Service.Agent().GetFile(ctx, protocol.FileAreaConfig, SecretsEnv)
	switch {
	case err == nil:
		data, rerr := io.ReadAll(io.LimitReader(rc, 64<<10))
		_ = rc.Close()
		if rerr != nil {
			return s, rerr
		}
		if s, err = ParseSecrets(data); err != nil {
			return s, fmt.Errorf("engine %s: %w", SecretsEnv, err)
		}
		r.logf("Adopted the clinic secrets from the engine")
	case isNotFound(err):
		r.logf("Generated new clinic secrets")
	default:
		return s, err
	}
	return s, SaveSecrets(path, s)
}

func isNotFound(err error) bool {
	return errors.Is(err, client.ErrNotFound)
}

// Configure renders kit for host and uploads it with the clinic secrets. It
// also makes the service send PROXY headers, which the Caddyfile requires.
// Running containers pick the change up on the next Up.
func (r *Runtime) Configure(ctx context.Context, kit fs.FS, host string, origins []string, images []Image) error {
	sec, err := r.Secrets(ctx)
	if err != nil {
		return err
	}
	cert, err := r.BackupCertificate(ctx)
	if err != nil {
		return err
	}
	files, err := Render(kit, Config{
		Host: host, Origins: origins, Images: images, Secrets: sec,
		BackupCert: cert, BackupTZ: PosixTZ(time.Now()),
	})
	if err != nil {
		return err
	}
	r.logf("Uploading the clinic configuration for %s", host)
	ag := r.Service.Agent()
	for _, name := range slices.Sorted(maps.Keys(files)) {
		b := files[name]
		if err := ag.PutFile(ctx, protocol.FileAreaConfig, name, bytes.NewReader(b), int64(len(b))); err != nil {
			return fmt.Errorf("upload %s: %w", name, err)
		}
	}
	on := true
	if _, err := r.Service.UpdateConfig(ctx, api.ServiceConfigUpdate{ProxyProtocol: &on}); err != nil {
		return fmt.Errorf("enable PROXY protocol: %w", err)
	}
	return nil
}

// Images loads (tarball) or pulls each image; the agent verifies digests.
func (r *Runtime) Images(ctx context.Context, images []Image) error {
	ag := r.Service.Agent()
	for _, img := range images {
		if img.Tarball == "" {
			r.logf("Pulling %s", img.Ref())
			if err := ag.Op(ctx, protocol.ImagePull{Role: img.Role, Ref: img.Repo, Digest: img.Digest}, nil, r.frames); err != nil {
				return err
			}
			continue
		}
		r.logf("Loading %s from %s", img.Ref(), filepath.Base(img.Tarball))
		f, err := os.Open(img.Tarball)
		if err != nil {
			return err
		}
		err = ag.LoadImage(ctx, protocol.ImageLoad{Role: img.Role, Digest: img.Digest}, f, nil)
		_ = f.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// Install loads the images, configures the clinic for host and brings the
// stack up migrated. It is safe to run again: secrets are kept and only
// changed containers are recreated.
func (r *Runtime) Install(ctx context.Context, kit fs.FS, host string, origins []string, images []Image) error {
	if err := r.Images(ctx, images); err != nil {
		return err
	}
	if err := r.Configure(ctx, kit, host, origins, images); err != nil {
		return err
	}
	if err := r.Migrate(ctx); err != nil {
		return err
	}
	return r.Up(ctx)
}

// Migrate runs the one-shot migrate service.
func (r *Runtime) Migrate(ctx context.Context) error {
	r.logf("Applying database migrations")
	if err := r.Service.Agent().Op(ctx, protocol.RunMigrations{}, nil, r.frames); err != nil {
		return fmt.Errorf("database migrations failed: %w", err)
	}
	return nil
}

// Up starts the stack and waits until it is healthy.
func (r *Runtime) Up(ctx context.Context) error {
	r.logf("Starting CARE")
	return r.Service.Agent().Op(ctx, protocol.StackUp{Wait: true, TimeoutSec: 600}, nil, r.frames)
}

// Stop stops the stack, keeping containers and data.
func (r *Runtime) Stop(ctx context.Context) error {
	r.logf("Stopping CARE (data kept)")
	return r.Service.Agent().Op(ctx, protocol.StackStop{}, nil, r.frames)
}

// Status reports per-service state.
func (r *Runtime) Status(ctx context.Context) (protocol.StackStatusResult, error) {
	var st protocol.StackStatusResult
	err := r.Service.Agent().Op(ctx, protocol.StackStatus{}, &st, nil)
	return st, err
}

// Logs writes the last tail lines of svc to w.
func (r *Runtime) Logs(ctx context.Context, svc protocol.Service, tail int, w io.Writer) error {
	return r.Service.Agent().Op(ctx, protocol.StackLogs{Service: svc, TailLines: tail}, nil, func(f protocol.Frame) {
		if f.Log != nil {
			_, _ = fmt.Fprintln(w, f.Log.Line)
		}
	})
}

// ErrUserExists is returned by CreateSuperuser when the username is taken.
var ErrUserExists = errors.New("user already exists")

// CreateSuperuser creates a CARE superuser. The password travels as a
// short-lived file in the config area, never in op parameters.
func (r *Runtime) CreateSuperuser(ctx context.Context, username, password string) error {
	ag := r.Service.Agent()
	name := "superuser-" + randomHex(8)
	if err := ag.PutFile(ctx, protocol.FileAreaConfig, name, strings.NewReader(password), int64(len(password))); err != nil {
		return err
	}
	defer func() {
		// Best effort even when ctx was cancelled.
		if err := ag.DeleteFile(context.WithoutCancel(ctx), protocol.FileAreaConfig, name); err != nil {
			r.logf("Warning: could not delete the password file %s: %v", name, err)
		}
	}()
	err := ag.Op(ctx, protocol.ManageCmd{Command: protocol.ManageCreateSuperuser, Username: username, PasswordFile: name}, nil, nil)
	if err != nil && strings.Contains(err.Error(), "already taken") {
		return ErrUserExists
	}
	return err
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
