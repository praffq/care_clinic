package clinic

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/compose"
	"github.com/ohcnetwork/care_desktop/app/internal/lines"
	"github.com/ohcnetwork/care_desktop/app/internal/release"
)

func TestUpgradeLineDropsOldPendingMetadataBeforeFailedNewStage(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX Docker fixture, never a real daemon")
	}
	dir := t.TempDir()
	trace := filepath.Join(dir, "trace")
	pins := &release.Pins{AppVersion: "0.1.16", DefaultLine: "stable", LinesURL: filepath.Join(dir, "lines.json"),
		BackendImage: "fixture-backend", FrontendImage: "fixture-frontend"}
	image := func(name string, c string) lines.Image {
		return lines.Image{Commit: strings.Repeat(c, 40), Image: "ghcr.io/fixture/" + name + "@sha256:" + strings.Repeat(c, 64),
			Builder:     "ghcr.io/fixture/" + name + "-builder@sha256:" + strings.Repeat(c, 64),
			RuntimeKeys: []string{"REACT_CARE_API_URL"}}
	}
	stable := lines.Line{ID: "stable", Backend: image("backend", "a"), Frontend: image("frontend", "a")}
	next := lines.Line{ID: "next", Backend: image("backend", "c"), Frontend: image("frontend", "c")}
	oldFrontend := image("frontend", "b")
	old := compose.Lock{Line: "stable", Backend: compose.Channel{Current: stable.Backend},
		Frontend: compose.Channel{Current: stable.Frontend, Next: &oldFrontend}}
	if err := compose.WriteLock(dir, old); err != nil {
		t.Fatal(err)
	}
	manifest, err := json.Marshal(lines.Manifest{Schema: lines.Schema, Lines: []lines.Line{stable, next}})
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"lines.json": manifest, "frontend.env": {}, "backend.env": {},
	} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "keys"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "keys", "backup-cert.pem"), []byte("fixture stat-only encryption gate"), 0o600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + trace + "'\n" +
		"case \"$1 $2\" in 'image ls') exit 0;; 'image inspect') exit 1;; esac\n" +
		"case \"$1\" in pull) [ \"$2\" != '" + next.Backend.Image + "' ]; exit $?;; build) exit 0;; esac\nexit 99\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", t.TempDir())
	e := &Clinic{InstallDir: dir, BackupDir: filepath.Join(dir, "backups"), Pins: pins}
	if err := e.UpgradeLine("next"); err == nil || !strings.Contains(err.Error(), "exit status") {
		t.Fatalf("new stage did not reach its intended backend failure: %v", err)
	}
	lock := compose.ReadLock(dir)
	if lock.NextLine != "" || lock.Backend.Next != nil || lock.Frontend.Next != nil {
		t.Fatalf("old pending metadata survived failed line upgrade: %+v", lock)
	}
	calls, err := os.ReadFile(trace)
	if err != nil || !strings.Contains(string(calls), "fixture-frontend-next") || !strings.Contains(string(calls), next.Backend.Image) {
		t.Fatalf("fixture did not stage frontend before failed backend: %s, %v", calls, err)
	}
	if applied, err := e.Builder().ApplyPending(); err != nil || applied.Any() {
		t.Fatalf("orphaned next image was applied using old metadata: %+v %v", applied, err)
	}
	if err := compose.WriteLock(dir, old); err != nil {
		t.Fatal(err)
	}
	if err := e.DeclineUpdate(); err != nil {
		t.Fatal(err)
	}
	declined := compose.ReadLock(dir).Frontend
	if declined.Next == nil || declined.Next.Commit != oldFrontend.Commit || declined.Declined != oldFrontend.Commit {
		t.Fatalf("ordinary not-now no longer preserves next-start behavior: %+v", declined)
	}
}
