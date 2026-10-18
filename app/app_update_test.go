package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/sys/elevate"
	"github.com/ohcnetwork/care_desktop/app/internal/updates"
)

func TestNewerVersion(t *testing.T) {
	for _, tt := range []struct {
		current, candidate string
		want               bool
	}{
		{"0.1.0", "0.1.1", true},
		{"0.1.0", "0.2.0", true},
		{"0.1.0", "1.0.0", true},
		{"0.1.0", "0.1.0", false},
		{"0.2.0", "0.1.9", false},
		{"1.0.0", "0.9.9", false},
		{"0.9.0", "0.10.0", true},
		{"0.10.0", "0.9.0", false},
		{"0.1.0-dev", "0.1.0", true},
		{"0.1.0-dev", "0.0.9", false},
		{"0.1.0", "not-a-version", false},
	} {
		if got := newerVersion(tt.current, tt.candidate); got != tt.want {
			t.Errorf("newerVersion(%q, %q) = %v, want %v", tt.current, tt.candidate, got, tt.want)
		}
	}
}

func TestPlatformAssetPicksOneInstaller(t *testing.T) {
	rel := ghRelease{Assets: []ghAsset{
		{Name: "SHA256SUMS", URL: "https://example.invalid/SHA256SUMS"},
		{Name: "release-manifest.json", URL: "https://example.invalid/manifest"},
		{Name: "CARE-Clinic-1.2.3-macos.dmg", URL: "https://example.invalid/dmg"},
		{Name: "CARE-Clinic-1.2.3-windows-amd64-setup.exe", URL: "https://example.invalid/exe"},
	}}
	asset, ok := platformAsset(rel)
	if !ok {
		t.Skip("no installer is published for this platform")
	}
	if asset.URL == "" || asset.Name == "SHA256SUMS" {
		t.Fatalf("picked %q as the installer", asset.Name)
	}
	if _, ok := findAsset(rel, func(name string) bool { return name == "SHA256SUMS" }); !ok {
		t.Fatal("checksums were not found alongside the installer")
	}
	empty := ghRelease{Assets: []ghAsset{{Name: "CARE-Clinic-1.2.3-macos.dmg"}}}
	if _, ok := platformAsset(empty); ok {
		t.Fatal("an asset with no URL was accepted")
	}
}

func updateCache(t *testing.T) {
	t.Helper()
	tmp := t.TempDir()
	for _, k := range []string{"TMPDIR", "TMP", "TEMP", "HOME", "XDG_CACHE_HOME", "LOCALAPPDATA"} {
		t.Setenv(k, tmp)
	}
}

func TestDownloadReleaseRetriesOnceThenRejectsDamagedInstallers(t *testing.T) {
	good := []byte("CARE Clinic installer")
	digest := sha256.Sum256(good)
	want := hex.EncodeToString(digest[:])
	for _, tc := range []struct {
		name      string
		bodies    []string
		requests  int32
		installed bool
	}{
		{"intact", []string{string(good)}, 1, true},
		{"damaged once", []string{"truncated-bad-data-xx", string(good)}, 2, true},
		{"damaged twice", []string{"truncated-bad-data-xx", "corrupted-bad-data-yy"}, 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			updateCache(t)
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := requests.Add(1)
				_, _ = w.Write([]byte(tc.bodies[n-1]))
			}))
			defer server.Close()
			asset := ghAsset{Name: "CARE-Clinic-1.2.3-setup.exe", URL: server.URL}
			path, err := downloadRelease(context.Background(), asset, want, nil)
			if got := requests.Load(); got != tc.requests {
				t.Fatalf("made %d download attempts, want %d", got, tc.requests)
			}
			if tc.installed {
				data, readErr := os.ReadFile(path)
				if err != nil || readErr != nil || string(data) != string(good) {
					t.Fatalf("verified download missing: %v, %v", err, readErr)
				}
				return
			}
			if !errors.Is(err, updates.ErrChecksum) {
				t.Fatalf("damaged download was not rejected: %v", err)
			}
			if !strings.Contains(downloadFailed("1.2.3", err).Error(), "choose Download again") {
				t.Fatal("the failure does not say how to retry")
			}
			cache, _ := os.UserCacheDir()
			if left, _ := os.ReadDir(filepath.Join(cache, "CARE Clinic", "updates", "downloads")); len(left) != 0 {
				t.Fatalf("damaged download was left behind: %v", left)
			}
		})
	}
}

func TestDownloadReleaseResumesAPartialDownload(t *testing.T) {
	updateCache(t)
	good := []byte(strings.Repeat("CARE Clinic installer ", 100))
	digest := sha256.Sum256(good)
	want := hex.EncodeToString(digest[:])
	var ranges []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ranges = append(ranges, r.Header.Get("Range"))
		http.ServeContent(w, r, "setup.exe", time.Time{}, bytes.NewReader(good))
	}))
	defer server.Close()
	asset := ghAsset{Name: "CARE-Clinic-1.2.3-setup.exe", URL: server.URL, Size: int64(len(good))}
	cache, _ := os.UserCacheDir()
	dir := filepath.Join(cache, "CARE Clinic", "updates", "downloads")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, want+"-"+asset.Name+".part"), good[:500], 0o600); err != nil {
		t.Fatal(err)
	}
	var last int64
	path, err := downloadRelease(context.Background(), asset, want, func(done, _ int64) { last = done })
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); !bytes.Equal(data, good) {
		t.Fatal("the resumed download is not the installer")
	}
	if len(ranges) != 1 || ranges[0] != "bytes=500-" || last != int64(len(good)) {
		t.Fatalf("ranges %q, last progress %d", ranges, last)
	}
	// A verified download is reused without another request.
	if _, err := downloadRelease(context.Background(), asset, want, nil); err != nil || len(ranges) != 1 {
		t.Fatalf("the verified download was fetched again: %v %q", err, ranges)
	}
}

func TestDownloadRejectsUnsafeAssetAndChecksum(t *testing.T) {
	sum := strings.Repeat("a", 64)
	for _, name := range []string{"../installer.exe", `..\installer.exe`, "", "."} {
		if _, err := downloadRelease(context.Background(), ghAsset{Name: name}, sum, nil); err == nil {
			t.Errorf("unsafe filename %q accepted", name)
		}
	}
	if _, err := downloadRelease(context.Background(), ghAsset{Name: "installer.exe"}, "invalid", nil); err == nil {
		t.Fatal("invalid checksum accepted")
	}
}

func TestMacUpdateTrust(t *testing.T) {
	for _, tc := range []struct {
		name          string
		old, new      string
		allowUnsigned bool
		ok            bool
	}{
		{"same team", "TEAM1", "TEAM1", false, true},
		{"same team, unsigned feed allowed", "TEAM1", "TEAM1", true, true},
		{"other team", "TEAM1", "TEAM2", true, false},
		{"signed to ad-hoc is a downgrade", "TEAM1", "", true, false},
		{"ad-hoc to signed", "", "TEAM1", true, false},
		{"ad-hoc to ad-hoc needs an unsigned feed", "", "", false, false},
		{"ad-hoc to ad-hoc on an unsigned feed", "", "", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := macUpdateTrust(tc.old, tc.new, tc.allowUnsigned); (err == nil) != tc.ok {
				t.Fatalf("macUpdateTrust(%q, %q, %v) = %v", tc.old, tc.new, tc.allowUnsigned, err)
			}
		})
	}
}

func TestSwapScriptReplacesBundleOrKeepsTheOldOne(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the in-place app swap is macOS only")
	}
	bundle := func(dir, marker string) string {
		app := filepath.Join(dir, "CARE Clinic.app")
		if err := os.MkdirAll(filepath.Join(app, "Contents"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(app, "Contents", "marker"), []byte(marker), 0o644); err != nil {
			t.Fatal(err)
		}
		return app
	}
	marker := func(app string) string {
		data, _ := os.ReadFile(filepath.Join(app, "Contents", "marker"))
		return string(data)
	}
	tmp := t.TempDir()
	apps := filepath.Join(tmp, "Applications")
	target := bundle(apps, "old")
	staged := bundle(filepath.Join(tmp, "stage"), "new")

	if err := elevate.Run(swapScript(target, filepath.Join(tmp, "missing.app")), false); err == nil {
		t.Fatal("a swap from a missing bundle reported success")
	}
	if got := marker(target); got != "old" {
		t.Fatalf("a failed swap left %q installed, want the old bundle back", got)
	}

	brokenRename := strings.Replace(swapScript(target, staged),
		`/bin/mv "$slot/new.app"`, `/usr/bin/false`, 1)
	if err := elevate.Run(brokenRename, false); err == nil {
		t.Fatal("failed final rename reported success")
	}
	if got := marker(target); got != "old" {
		t.Fatalf("failed final rename did not restore old bundle: %q", got)
	}
	// Failed transactions retain their private directory for diagnosis.
	for _, entry := range []string{".care-update-" + filepath.Base(tmp)} {
		if err := os.RemoveAll(filepath.Join(apps, entry)); err != nil {
			t.Fatal(err)
		}
	}

	if err := elevate.Run(swapScript(target, staged), false); err != nil {
		t.Fatal(err)
	}
	if got := marker(target); got != "new" {
		t.Fatalf("installed bundle is %q, want new", got)
	}
	if left, _ := os.ReadDir(apps); len(left) != 1 {
		t.Fatalf("the swap left extra entries behind: %v", left)
	}
}

func TestReleaseVersionFromTag(t *testing.T) {
	for tag, want := range map[string]string{"desktop-v0.2.0": "0.2.0", "v1.2.3": "1.2.3", " desktop-v1.0.0 ": "1.0.0"} {
		if got, err := releaseVersion(tag); err != nil || got != want {
			t.Errorf("releaseVersion(%q) = %q, %v; want %q", tag, got, err, want)
		}
	}
	for _, tag := range []string{"engine-v0.1.2", "desktop-v", "", "care-engine-v0.1.3"} {
		if v, err := releaseVersion(tag); err == nil {
			t.Errorf("releaseVersion(%q) = %q, want an error", tag, v)
		}
	}
}
