package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/release"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/elevate"
	"github.com/ohcnetwork/care_desktop/app/internal/updates"
)

func TestAppUpdateCheckStagesWithoutEmittingInstallProgress(t *testing.T) {
	if desktopPlatformKey() == "" {
		t.Skip("desktop installers are only published for macOS and Windows")
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	oldKeys, oldHTTP := release.PublicKeys, http.DefaultClient
	t.Cleanup(func() { release.PublicKeys, http.DefaultClient = oldKeys, oldHTTP })
	release.PublicKeys = []string{base64.StdEncoding.EncodeToString(pub)}
	data, err := os.ReadFile("internal/release/testdata/published-revision1.json")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := release.DecodeManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	manifest.CanonicalizeImageReferences()
	manifest.Desktop.Version = "0.0.2"
	payload := []byte("verified desktop installer fixture")
	sum := sha256.Sum256(payload)
	assetRequests := 0
	var encoded, signature []byte
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/updates.json":
			_, _ = w.Write(encoded)
		case "/updates.json.sig":
			_, _ = w.Write(signature)
		case "/installer":
			assetRequests++
			_, _ = w.Write(payload)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	http.DefaultClient = server.Client()
	manifest.Desktop.Assets[desktopPlatformKey()] = release.Asset{
		URL: server.URL + "/installer", Size: int64(len(payload)), SHA256: hex.EncodeToString(sum[:]),
	}
	encoded, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	signature = ed25519.Sign(key, encoded)
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("USERPROFILE", root)
	t.Setenv("LOCALAPPDATA", root)
	t.Setenv("XDG_CACHE_HOME", root)
	app := &App{
		configFile:       filepath.Join(root, "config.json"),
		pins:             &release.Pins{AppVersion: "0.0.1", UpdatesURL: server.URL + "/updates.json"},
		stagedAppUpdates: make(map[string]string),
	}
	app.updateCoordinator, err = updates.New(filepath.Join(app.installDir(), "updates"))
	if err != nil {
		t.Fatal(err)
	}
	var progress []AppUpdateProgress
	app.emitEvent = func(event string, args ...any) {
		if event != "app-update-check-progress" {
			t.Errorf("checking must not emit install lifecycle event %q", event)
			return
		}
		progress = append(progress, args[0].(AppUpdateProgress))
	}
	for check := 0; check < 2; check++ {
		result, err := app.CheckAppUpdate()
		if err != nil || !result.Available || result.Version != "0.0.2" {
			t.Fatalf("check %d: result=%+v error=%v", check, result, err)
		}
	}
	if assetRequests != 1 || len(progress) == 0 || progress[len(progress)-1].Done != int64(len(payload)) {
		t.Fatalf("staging/cache behavior: requests=%d progress=%+v", assetRequests, progress)
	}
	staged, err := os.ReadFile(app.stagedAppUpdates["0.0.2"])
	if err != nil || string(staged) != string(payload) {
		t.Fatalf("installer was not staged intact: %v", err)
	}
}

func TestNewerVersion(t *testing.T) {
	for _, tt := range []struct {
		current, candidate string
		want               bool
	}{
		{"0.1.0", "0.1.1", true},
		{"0.1.11", "0.1.12", true},
		{"0.1.12", "0.2.0", true},
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

func TestDownloadVerifiedRetriesThenRejectsDamagedInstallers(t *testing.T) {
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
		{"damaged once", []string{"truncated", string(good)}, 2, true},
		{"damaged twice", []string{"truncated", "corrupted"}, 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			t.Setenv("TMPDIR", tmp)
			t.Setenv("TMP", tmp)
			t.Setenv("TEMP", tmp)
			t.Setenv("HOME", tmp)
			t.Setenv("XDG_CACHE_HOME", tmp)
			t.Setenv("LOCALAPPDATA", tmp)
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := requests.Add(1)
				_, _ = w.Write([]byte(tc.bodies[n-1]))
			}))
			defer server.Close()
			var logs []string
			asset := ghAsset{Name: "CARE-Clinic-1.2.3-setup.exe", URL: server.URL}
			path, err := downloadVerified(asset, want, "1.2.3", func(line string) { logs = append(logs, line) }, nil)
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
			if err == nil || !strings.Contains(err.Error(), "didn't download properly") ||
				!strings.Contains(err.Error(), "choose Update again") {
				t.Fatalf("damaged download was not rejected clearly: %v", err)
			}
			if !strings.Contains(strings.Join(logs, "\n"), "Trying once more") {
				t.Fatalf("retry was not logged: %q", logs)
			}
			cache, _ := os.UserCacheDir()
			if left, _ := os.ReadDir(filepath.Join(cache, "CARE Clinic", "updates")); len(left) != 0 {
				t.Fatalf("damaged download was left behind: %v", left)
			}
		})
	}
}

func TestDownloadRejectsUnsafeAssetAndChecksum(t *testing.T) {
	sum := strings.Repeat("a", 64)
	for _, name := range []string{"../installer.exe", `..\installer.exe`, "", "."} {
		if _, err := downloadVerified(ghAsset{Name: name}, sum, "1.2.3", nil, nil); err == nil {
			t.Errorf("unsafe filename %q accepted", name)
		}
	}
	if _, err := downloadVerified(ghAsset{Name: "installer.exe"}, "invalid", "1.2.3", nil, nil); err == nil {
		t.Fatal("invalid checksum accepted")
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
	left, err := os.ReadDir(apps)
	if err != nil || len(left) != 2 {
		t.Fatalf("the swap did not retain its previous application: %v, %v", left, err)
	}
	previous := filepath.Join(apps, ".care-update-"+filepath.Base(tmp), "previous.app")
	if got := marker(previous); got != "old" {
		t.Fatalf("previous app slot contains %q, want old app", got)
	}
}
