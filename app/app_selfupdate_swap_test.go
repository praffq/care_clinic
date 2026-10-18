package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/feed"
)

// TestMacSelfUpdateSwap builds two ad-hoc signed app versions in a temp dir
// and runs the real verification and update helper between them: the new
// version replaces the old one after the "app" exits, and a tampered bundle
// leaves the old one in place. /usr/bin/open and the failure alert are
// stubbed so nothing is launched. scripts/test-self-update-swap.sh runs it.
func TestMacSelfUpdateSwap(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the in-place app swap is macOS only")
	}
	if !feed.UnsignedFeedAllowed() {
		t.Skip("this build only accepts signed updates")
	}
	for _, tc := range []struct {
		name   string
		tamper bool
	}{{"swap", false}, {"tampered", true}} {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			target := fakeAppBundle(t, filepath.Join(tmp, "Applications"), "0.1.0")
			staged := fakeAppBundle(t, filepath.Join(tmp, "update-1", "stage"), "0.2.0")
			work := filepath.Join(tmp, "update-1")

			unsigned, err := verifyMacBundle(staged, target, "0.2.0")
			if err != nil || !unsigned {
				t.Fatalf("verifyMacBundle = %v, %v; want an accepted ad-hoc update", unsigned, err)
			}
			if _, err := verifyMacBundle(staged, target, "0.3.0"); err == nil {
				t.Fatal("a bundle with the wrong version was accepted")
			}
			verification, err := macCopiedBundleVerification(staged)
			if err != nil {
				t.Fatal(err)
			}
			if tc.tamper {
				// Changed after verification: the helper must refuse it.
				if err := os.WriteFile(filepath.Join(staged, "Contents", "MacOS", "care"), []byte("#!/bin/sh\necho evil\n"), 0o755); err != nil {
					t.Fatal(err)
				}
			}

			running := exec.Command("/bin/sleep", "30")
			if err := running.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = running.Process.Kill() }()
			opened := filepath.Join(tmp, "opened")
			script := macUpdateHelper(target, staged, work, running.Process.Pid, false, verification)
			script = strings.ReplaceAll(script, "/usr/bin/open ", "echo >>"+opened+" ")
			script = strings.ReplaceAll(script, "/usr/bin/osascript ", "echo alert ")
			name := filepath.Join(tmp, "install.sh")
			if err := os.WriteFile(name, []byte(script), 0o600); err != nil {
				t.Fatal(err)
			}
			helper := exec.Command("/bin/sh", name)
			if err := helper.Start(); err != nil {
				t.Fatal(err)
			}
			waitFile(t, filepath.Join(work, "ready"))
			time.Sleep(600 * time.Millisecond)
			if v := bundleVersion(t, target); v != "0.1.0" {
				t.Fatalf("replaced before the handoff was committed: %s", v)
			}
			if err := os.WriteFile(filepath.Join(work, "continue"), []byte("continue"), 0o600); err != nil {
				t.Fatal(err)
			}
			time.Sleep(600 * time.Millisecond)
			if v := bundleVersion(t, target); v != "0.1.0" {
				t.Fatalf("replaced while the app was still running: %s", v)
			}
			_ = running.Process.Kill()
			_ = running.Wait()
			err = helper.Wait()

			want := "0.2.0"
			if tc.tamper {
				want = "0.1.0"
				if err == nil {
					t.Fatal("the helper accepted a bundle changed after verification")
				}
			} else if err != nil {
				log, _ := os.ReadFile(filepath.Join(work, "helper.log"))
				t.Fatalf("helper failed: %v\n%s", err, log)
			}
			if v := bundleVersion(t, target); v != want {
				t.Fatalf("installed version is %s, want %s", v, want)
			}
			if out, _ := os.ReadFile(opened); strings.TrimSpace(string(out)) != target {
				t.Fatalf("relaunched %q, want %q", out, target)
			}
			if err := exec.Command("/usr/bin/codesign", "--verify", "--deep", "--strict", target).Run(); err != nil {
				t.Fatalf("the installed app's signature is broken: %v", err)
			}
			if left, _ := filepath.Glob(filepath.Join(filepath.Dir(target), ".care-update-*")); !tc.tamper && len(left) != 0 {
				t.Fatalf("the swap left %v behind", left)
			}
		})
	}
}

func fakeAppBundle(t *testing.T, dir, version string) string {
	t.Helper()
	app := filepath.Join(dir, "CARE Clinic.app")
	if err := os.MkdirAll(filepath.Join(app, "Contents", "MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>org.ohc.care-clinic.swaptest</string>
<key>CFBundleExecutable</key><string>care</string>
<key>CFBundleShortVersionString</key><string>` + version + `</string>
<key>CFBundlePackageType</key><string>APPL</string>
</dict></plist>
`
	if err := os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app, "Contents", "MacOS", "care"), []byte("#!/bin/sh\necho "+version+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("/usr/bin/codesign", "--force", "--sign", "-", app).CombinedOutput(); err != nil {
		t.Fatalf("ad-hoc signing failed: %v: %s", err, out)
	}
	return app
}

func bundleVersion(t *testing.T, app string) string {
	t.Helper()
	v, err := plistValue(app, "CFBundleShortVersionString")
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func waitFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never appeared", path)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
