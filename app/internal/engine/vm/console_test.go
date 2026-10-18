package vm

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestConsoleLogRotates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "console.log")
	if err := os.WriteFile(path, []byte("old boot\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := openConsoleLog(path, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"0123456789", "abcdefghij", "klm"} {
		if _, err := l.Write([]byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Write([]byte("x")); err == nil {
		t.Error("write after close succeeded")
	}

	// "old boot\n" + "0123456789" = 19 bytes; "abcdefghij" would exceed 20.
	assertFile(t, path+".1", "old boot\n0123456789")
	assertFile(t, path, "abcdefghijklm")
}

func TestConsoleLogOversizedWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "console.log")
	l, err := openConsoleLog(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	// A single write larger than the cap goes to a fresh file, never lost.
	for _, s := range []string{"long line", "more"} {
		if _, err := l.Write([]byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	assertFile(t, path+".1", "long line")
	assertFile(t, path, "more")
	// Windows has no POSIX permission bits; files inherit the folder ACL.
	if st, err := os.Stat(path); err != nil || (runtime.GOOS != "windows" && st.Mode().Perm() != 0o600) {
		t.Errorf("console log mode: %v %v", st, err)
	}
}

func TestCopyConsoleIgnoresMirrorErrors(t *testing.T) {
	var log strings.Builder
	copyConsole(strings.NewReader("boot ok\n"), &log, failingWriter{})
	if log.String() != "boot ok\n" {
		t.Fatalf("log = %q", log.String())
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, os.ErrClosed }

func assertFile(t *testing.T, path, want string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != want {
		t.Errorf("%s = %q, want %q", filepath.Base(path), b, want)
	}
}
