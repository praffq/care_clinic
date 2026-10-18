package control_test

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/service/control"
)

func shortDir(t *testing.T) string { return t.TempDir() }

func testSocket(t *testing.T, _ string) string {
	t.Helper()
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return `\\.\pipe\care-clinic-test-` + hex.EncodeToString(b)
}

func TestPipeInUse(t *testing.T) {
	e := newEnv(t, nil)
	if _, err := control.Listen(control.ListenConfig{Path: e.sock}); err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("second listener: %v", err)
	}
}

// The hosted runner's user is an administrator, so a pipe for the
// Administrators group (with its members listed) admits it.
func TestPipeWithGroup(t *testing.T) {
	sock := testSocket(t, "")
	ln, err := control.Listen(control.ListenConfig{Path: sock, Group: "Administrators"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			_ = c.Close()
		}
	}()
	c, err := control.NewClient(sock).Dial(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	if _, err := control.Listen(control.ListenConfig{Path: testSocket(t, ""), Group: "no-such-group-care"}); err == nil {
		t.Fatal("unknown group accepted")
	}
}
