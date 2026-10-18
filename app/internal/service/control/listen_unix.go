//go:build unix

package control

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"time"
)

// ListenConfig describes the control socket.
type ListenConfig struct {
	Path string
	// Group, when set, owns the socket and its members may connect. The
	// socket is then 0666 and the peer check is the gate: it also reads
	// membership from the user database, so a user added to the group can
	// connect at once, without logging in again (a 0660 socket would refuse
	// them until their next login). Without it (dev mode) the socket is 0600
	// and only the service's own user (and root) may connect.
	Group string
	Log   *slog.Logger
}

// Listen creates the control socket and returns a listener that drops
// connections from peers the policy rejects. A stale socket is replaced; a
// live one (another service instance) is an error. A missing parent
// directory is created (0755 with a group, else 0700).
func Listen(cfg ListenConfig) (net.Listener, error) {
	pol := Policy{OwnerUID: uint32(os.Getuid())}
	gid := -1
	if cfg.Group != "" {
		g, err := LookupGroup(cfg.Group)
		if err != nil {
			return nil, fmt.Errorf("control socket group: %w", err)
		}
		pol.Group, gid = &g, int(g)
	}
	log := cfg.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	dir := filepath.Dir(cfg.Path)
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		mode := os.FileMode(0o700)
		if gid >= 0 {
			mode = 0o755
		}
		if err := os.MkdirAll(dir, mode); err != nil {
			return nil, err
		}
		if err := os.Chmod(dir, mode); err != nil {
			return nil, err
		}
	}
	if err := removeStale(cfg.Path); err != nil {
		return nil, err
	}
	// Bind under a temporary name and rename once mode and group are set, so
	// the final path never exists with the wrong permissions.
	tmp := cfg.Path + ".new"
	_ = os.Remove(tmp)
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: tmp, Net: "unix"})
	if err != nil {
		return nil, err
	}
	ln.SetUnlinkOnClose(false)
	fail := func(err error) (net.Listener, error) {
		_ = ln.Close()
		_ = os.Remove(tmp)
		return nil, err
	}
	mode := os.FileMode(0o600)
	if gid >= 0 {
		mode = 0o666
		if err := os.Chown(tmp, -1, gid); err != nil {
			return fail(err)
		}
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return fail(err)
	}
	if err := os.Rename(tmp, cfg.Path); err != nil {
		return fail(err)
	}
	return &peerListener{UnixListener: ln, path: cfg.Path, policy: pol, log: log}, nil
}

func removeStale(path string) error {
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if st.Mode().Type() != os.ModeSocket {
		return fmt.Errorf("%s exists and is not a socket", path)
	}
	if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
		_ = c.Close()
		return fmt.Errorf("%s is in use: is another care-clinic-service running?", path)
	}
	return os.Remove(path)
}

type peerListener struct {
	*net.UnixListener
	path   string
	policy Policy
	log    *slog.Logger
}

// Accept returns the next connection from an allowed peer.
func (l *peerListener) Accept() (net.Conn, error) {
	for {
		c, err := l.AcceptUnix()
		if err != nil {
			return nil, err
		}
		p, err := peerOf(c)
		if err == nil && l.policy.Allow(p) {
			return c, nil
		}
		l.log.Warn("rejected control connection", "uid", p.UID, "err", err)
		_ = c.Close()
	}
}

func (l *peerListener) Close() error {
	err := l.UnixListener.Close()
	_ = os.Remove(l.path)
	return err
}
