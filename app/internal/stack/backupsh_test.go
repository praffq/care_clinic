//go:build unix

package stack

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeTools stand in for the backup image's PostgreSQL, OpenSSL and
// util-linux tools; backup.sh's own logic (sets, manifests, retention) runs
// for real.
var fakeTools = map[string]string{
	"pg_isready": "exit 0",
	"psql":       "echo 1000",
	"pg_dump":    `while [ $# -gt 0 ]; do [ "$1" = -f ] && echo dump > "$2"; shift; done`,
	"pg_restore": "exit 0",
	"flock":      "exit 0",
	"openssl": `out=; in=
while [ $# -gt 0 ]; do case $1 in -in) in=$2 ;; -out) out=$2 ;; esac; shift; done
cp "$in" "$out"`,
	"sha256sum": `echo "0000000000000000000000000000000000000000000000000000000000000000  $1"`,
	"df":        `echo "Filesystem 1024-blocks Used Available Capacity Mounted"; echo "fake 1 1 ${FAKE_FREE_KB:-99999999} 1% /"`,
}

type backupSh struct {
	root string
	env  []string
}

func newBackupSh(t *testing.T) *backupSh {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	b := &backupSh{root: filepath.Join(dir, "backups")}
	for _, d := range []string{bin, b.root, filepath.Join(dir, "minio")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range fakeTools {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cert := filepath.Join(dir, "cert.pem")
	if err := os.WriteFile(cert, []byte("cert"), 0o600); err != nil {
		t.Fatal(err)
	}
	b.env = append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"POSTGRES_PASSWORD=x",
		"CARE_BACKUP_ROOT="+b.root,
		"CARE_BACKUP_MINIO="+filepath.Join(dir, "minio"),
		"CARE_BACKUP_CERT="+cert,
	)
	return b
}

// once runs "backup.sh once id"; sets made earlier are aged so the order
// does not depend on the clock's resolution.
func (b *backupSh) once(t *testing.T, id string, env ...string) (string, error) {
	t.Helper()
	sets := b.sets(t)
	for i, s := range sets {
		at := time.Now().Add(time.Duration(i-len(sets)-1) * time.Hour)
		if err := os.Chtimes(filepath.Join(b.root, s, "manifest.json"), at, at); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("sh", "../../../deployments/scripts/backup.sh", "once", id)
	cmd.Env = append(slices.Clone(b.env), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// sets lists the complete sets, oldest (by manifest time) first.
func (b *backupSh) sets(t *testing.T) []string {
	t.Helper()
	ms, err := filepath.Glob(filepath.Join(b.root, "*", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	slices.SortFunc(ms, func(x, y string) int {
		a, _ := os.Stat(x)
		c, _ := os.Stat(y)
		return a.ModTime().Compare(c.ModTime())
	})
	var out []string
	for _, m := range ms {
		out = append(out, filepath.Base(filepath.Dir(m)))
	}
	return out
}

func TestBackupShKeepsAtMostTwoSets(t *testing.T) {
	b := newBackupSh(t)
	for _, id := range []string{"daily-a", "daily-b"} {
		if out, err := b.once(t, id); err != nil {
			t.Fatalf("%s: %v\n%s", id, err, out)
		}
	}
	if got := b.sets(t); !slices.Equal(got, []string{"daily-a", "daily-b"}) {
		t.Fatalf("sets = %v", got)
	}
	out, err := b.once(t, "daily-c")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := b.sets(t); !slices.Equal(got, []string{"daily-b", "daily-c"}) {
		t.Fatalf("sets = %v\n%s", got, out)
	}
	if !strings.Contains(out, "deleting the oldest, daily-a") {
		t.Fatalf("no rotation logged:\n%s", out)
	}
}

func TestBackupShMakesRoomButKeepsTheNewestSet(t *testing.T) {
	b := newBackupSh(t)
	for _, id := range []string{"daily-a", "daily-b"} {
		if out, err := b.once(t, id); err != nil {
			t.Fatalf("%s: %v\n%s", id, err, out)
		}
	}
	// No room: the oldest set goes, the newest stays, and the backup fails.
	out, err := b.once(t, "daily-c", "FAKE_FREE_KB=10")
	if err == nil || !strings.Contains(out, "not enough space") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if got := b.sets(t); !slices.Equal(got, []string{"daily-b"}) {
		t.Fatalf("sets = %v\n%s", got, out)
	}
	// Still no room: the only set is never deleted.
	if _, err := b.once(t, "daily-d", "FAKE_FREE_KB=10"); err == nil {
		t.Fatal("no error")
	}
	if got := b.sets(t); !slices.Equal(got, []string{"daily-b"}) {
		t.Fatalf("sets = %v", got)
	}
	// Room again: a new set is made next to it.
	if out, err := b.once(t, "daily-e"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := b.sets(t); !slices.Equal(got, []string{"daily-b", "daily-e"}) {
		t.Fatalf("sets = %v", got)
	}
}
