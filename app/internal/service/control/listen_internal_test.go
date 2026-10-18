//go:build darwin || linux

package control

import (
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A peer whose login credentials lack the group is let in once the user
// database lists it as a member, and dropped otherwise.
func TestPeerListenerUsesUserDatabaseMembership(t *testing.T) {
	const group = 4_000_000_001
	for name, member := range map[string]bool{"member": true, "not a member": false} {
		t.Run(name, func(t *testing.T) {
			if os.Getuid() == 0 {
				t.Skip("root is always allowed")
			}
			dir, err := os.MkdirTemp("/tmp", "cc")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			sock := filepath.Join(dir, "s")
			ul, err := net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			g := uint32(group)
			pol := Policy{OwnerUID: uint32(os.Getuid()) + 1, Group: &g, UserGroups: func(uid uint32) []uint32 {
				if member && uid == uint32(os.Getuid()) {
					return []uint32{group}
				}
				return nil
			}}
			ln := &peerListener{UnixListener: ul, path: sock, policy: pol, log: slog.New(slog.DiscardHandler)}
			t.Cleanup(func() { _ = ln.Close() })
			accepted := make(chan net.Conn, 1)
			go func() {
				if c, err := ln.Accept(); err == nil {
					accepted <- c
				}
			}()
			c, err := net.Dial("unix", sock)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = c.Close() }()
			if member {
				select {
				case a := <-accepted:
					_ = a.Close()
				case <-time.After(5 * time.Second):
					t.Fatal("member was not accepted")
				}
				return
			}
			_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
			if _, err := c.Read(make([]byte, 1)); err != io.EOF {
				t.Fatalf("non-member read = %v, want EOF", err)
			}
			select {
			case <-accepted:
				t.Fatal("non-member was accepted")
			default:
			}
		})
	}
}
