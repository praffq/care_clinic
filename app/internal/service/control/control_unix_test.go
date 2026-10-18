//go:build unix

package control_test

import (
	"errors"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/service/control"
)

// shortDir returns a temp dir whose paths fit in a sockaddr_un.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ccs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func testSocket(_ *testing.T, dir string) string { return filepath.Join(dir, "run", "control.sock") }

func TestSocketMode(t *testing.T) {
	e := newEnv(t, nil)
	st, err := os.Stat(e.sock)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Type() != os.ModeSocket || st.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode %v", st.Mode())
	}
	if d, _ := os.Stat(filepath.Dir(e.sock)); d.Mode().Perm() != 0o700 {
		t.Fatalf("socket dir mode %v", d.Mode())
	}
	if _, err := os.Stat(e.sock + ".new"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary socket left behind: %v", err)
	}
	if _, err := control.Listen(control.ListenConfig{Path: e.sock}); err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("second Listen: %v", err)
	}
}

// With a group the socket is world-connectable (0666) and the peer check is
// the gate, so a newly added member needs no fresh login session.
func TestSocketModeWithGroup(t *testing.T) {
	g, err := user.LookupGroupId(strconv.Itoa(os.Getgid()))
	if err != nil {
		t.Skip(err)
	}
	dir := filepath.Join(shortDir(t), "run")
	sock := filepath.Join(dir, "c.sock")
	ln, err := control.Listen(control.ListenConfig{Path: sock, Group: g.Name})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	st, err := os.Stat(sock)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o666 {
		t.Fatalf("socket mode %v, want 0666", st.Mode())
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); ok && int(sys.Gid) != os.Getgid() {
		t.Fatalf("socket gid %d, want %d", sys.Gid, os.Getgid())
	}
	if d, _ := os.Stat(dir); d.Mode().Perm() != 0o755 {
		t.Fatalf("socket dir mode %v", d.Mode())
	}
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
}

func TestListenReplacesStaleSocketAndRejectsFiles(t *testing.T) {
	dir := shortDir(t)
	sock := filepath.Join(dir, "c.sock")
	ln, err := control.Listen(control.ListenConfig{Path: sock})
	if err != nil {
		t.Fatal(err)
	}
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sock); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Close left the socket: %v", err)
	}

	// A crashed service leaves its socket behind.
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	_ = stale.Close()
	ln, err = control.Listen(control.ListenConfig{Path: sock})
	if err != nil {
		t.Fatalf("stale socket: %v", err)
	}
	_ = ln.Close()

	file := filepath.Join(dir, "plain")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Listen(control.ListenConfig{Path: file}); err == nil {
		t.Fatal("Listen replaced a regular file")
	}
}

func TestPolicy(t *testing.T) {
	staff := uint32(500)
	pol := control.Policy{OwnerUID: 0, Group: &staff, UserGroups: func(uid uint32) []uint32 {
		if uid == 503 {
			return []uint32{20, 500}
		}
		return []uint32{20}
	}}
	for _, tc := range []struct {
		peer control.Peer
		want bool
	}{
		{control.Peer{UID: 0}, true},
		{control.Peer{UID: 501, GIDs: []uint32{20, 500}}, true},
		{control.Peer{UID: 502, GIDs: []uint32{20}}, false},
		{control.Peer{UID: 503, GIDs: []uint32{20}}, true}, // member per the user database
	} {
		if got := pol.Allow(tc.peer); got != tc.want {
			t.Errorf("Allow(%+v) = %v", tc.peer, got)
		}
	}
	dev := control.Policy{OwnerUID: 501}
	if !dev.Allow(control.Peer{UID: 501}) || !dev.Allow(control.Peer{UID: 0}) || dev.Allow(control.Peer{UID: 502, GIDs: []uint32{500}}) {
		t.Error("dev policy must admit only the owner and root")
	}
}
