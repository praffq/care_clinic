package logfile

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The helper process owns fd 2, so the test re-runs itself as one.
func TestMain(m *testing.M) {
	if path := os.Getenv("LOGFILE_HELPER"); path != "" {
		f, err := Open(path, 100, 2)
		if err != nil {
			fmt.Println("open:", err)
			os.Exit(3)
		}
		for i := range 20 {
			_, _ = fmt.Fprintf(f, "line %02d %s\n", i, strings.Repeat("x", 20))
		}
		fmt.Fprintln(os.Stderr, "direct stderr")
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "service.log")
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "LOGFILE_HELPER="+path)
	if out, err := cmd.CombinedOutput(); err != nil || len(out) != 0 {
		t.Fatalf("helper: %v: %q (stderr must go to the file)", err, out)
	}
	read := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	cur, one, two := read(path), read(filepath.Join(dir, "service.1.log")), read(filepath.Join(dir, "service.2.log"))
	if _, err := os.Stat(filepath.Join(dir, "service.3.log")); !os.IsNotExist(err) {
		t.Fatalf("kept more than 2 rotated files: %v", err)
	}
	for _, s := range []string{cur, one, two} {
		if len(s) > 100 {
			t.Fatalf("file over the limit: %d bytes", len(s))
		}
	}
	if !strings.Contains(cur, "line 19") || !strings.HasSuffix(cur, "direct stderr\n") {
		t.Fatalf("current log = %q", cur)
	}
	if !strings.Contains(one, "line 17") || strings.Contains(one, "line 19") {
		t.Fatalf("service.1.log = %q", one)
	}
}
