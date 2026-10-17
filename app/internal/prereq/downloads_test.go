package prereq

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/release"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/proc"
)

func TestDownloadErrorClassifiesConnectionsWithoutHidingOtherFailures(t *testing.T) {
	for _, tc := range []struct {
		name        string
		err         error
		interrupted bool
	}{
		{"closed connection", io.EOF, true},
		{"truncated body", io.ErrUnexpectedEOF, true},
		{"request timeout", context.DeadlineExceeded, true},
		{"stall cancellation", context.Canceled, true},
		{"reset connection", &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}, true},
		{"disconnected network", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ENETUNREACH}, true},
		{"DNS failure", &net.DNSError{Err: "no such host", Name: "example.invalid"}, true},
		{"wrapped request", &url.Error{Op: "Get", URL: "https://example.invalid", Err: io.ErrUnexpectedEOF}, true},
		{"certificate", &url.Error{Op: "Head", URL: "https://example.invalid", Err: x509.UnknownAuthorityError{}}, false},
		{"disk full", &os.PathError{Op: "write", Path: "/temporary-installer", Err: syscall.ENOSPC}, false},
		{"local permission", &os.PathError{Op: "open", Path: "/temporary-installer", Err: os.ErrPermission}, false},
		{"checksum", errors.New("SHA-256 mismatch"), false},
		{"server rejection", errors.New("the server said 403 Forbidden"), false},
		{"installer cancelled", errors.New("User cancelled the authorization dialog (-128)"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := downloadError(tc.err)
			if !errors.Is(got, tc.err) || strings.Contains(got.Error(), "download connection interrupted:") != tc.interrupted {
				t.Fatalf("incorrect download error classification or lost cause: %v", got)
			}
			if !tc.interrupted && got != tc.err {
				t.Fatalf("a non-network error was changed: %v", got)
			}
		})
	}
}

func downloadTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(name, dir)
	}
	return dir
}

func TestInterruptedDownloadDiscardsPartialFileAndRetries(t *testing.T) {
	dir := downloadTempDir(t)
	body := strings.Repeat("synthetic installer", 8192)
	sum := sha256.Sum256([]byte(body))
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		if requests.Add(1) == 1 {
			_, _ = io.WriteString(w, body[:1024])
			return
		}
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()
	var events []DownloadProgress
	pr := &Provisioner{Progress: func(p DownloadProgress) { events = append(events, p) }}
	d := Download{Name: "engine-test.zip", URL: server.URL, SHA256: hex.EncodeToString(sum[:])}
	path, err := pr.download(d)
	if path != "" || !errors.Is(err, io.ErrUnexpectedEOF) || !strings.Contains(err.Error(), "download connection interrupted:") {
		t.Fatalf("a broken connection lost its cause or interruption classification: path=%q, err=%v", path, err)
	}
	if files, err := os.ReadDir(dir); err != nil || len(files) != 0 {
		t.Fatalf("an incomplete installer was kept: %v, %v", files, err)
	}
	for _, event := range events {
		if event.Phase == "verifying" || event.Phase == "complete" {
			t.Fatalf("an interrupted installer reached verification or completion: %+v", event)
		}
	}
	last := events[len(events)-1]
	if last.Phase != "failed" || last.Done != 1024 || last.Total != int64(len(body)) {
		t.Fatalf("lost the failed download's progress: %+v", last)
	}
	events = nil
	path, err = pr.download(d)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != body {
		t.Fatalf("retry did not retrieve the complete installer: %v", err)
	}
	if requests.Load() != 2 || events[0].Phase != "connecting" || events[0].Done != 0 || events[len(events)-1].Phase != "complete" {
		t.Fatalf("retry did not restart and verify the download: %+v", events)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

func TestDownloadConnectionFailureBeforeHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	server.Close()
	d := Download{Name: "engine-test.zip", URL: server.URL}
	if _, err := inspectDownload(d); err == nil || !strings.Contains(err.Error(), "download connection interrupted:") {
		t.Fatalf("preview connection failure wasn't classified: %v", err)
	}
	var events []DownloadProgress
	pr := &Provisioner{Progress: func(p DownloadProgress) { events = append(events, p) }}
	if path, err := pr.download(d); path != "" || err == nil || !strings.Contains(err.Error(), "download connection interrupted:") {
		t.Fatalf("download connection failure wasn't classified: path=%q, err=%v", path, err)
	}
	if len(events) != 2 || events[0].Phase != "connecting" || events[1].Phase != "failed" || events[1].Done != 0 {
		t.Fatalf("failed connection reported download progress: %+v", events)
	}
}

func TestStalledDownloadIsAnInterruptionNotUserCancellation(t *testing.T) {
	dir := downloadTempDir(t)
	previous := downloadStallTimeout
	downloadStallTimeout = 50 * time.Millisecond
	defer func() { downloadStallTimeout = previous }()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000000")
		_, _ = io.WriteString(w, "partial")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	pr := &Provisioner{}
	path, err := pr.download(Download{Name: "engine-test.zip", URL: server.URL})
	if path != "" || !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "download connection interrupted:") {
		t.Fatalf("a stalled download looked like an unrelated cancellation: path=%q, err=%v", path, err)
	}
	if files, err := os.ReadDir(dir); err != nil || len(files) != 0 {
		t.Fatalf("a stalled installer was kept: %v, %v", files, err)
	}
}

func TestDownloadPreviewOnlyRequestsHeaders(t *testing.T) {
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/installer", http.StatusFound)
			return
		}
		w.Header().Set("Content-Length", "734003200")
	}))
	defer server.Close()
	info, err := inspectDownload(Download{Name: "engine.zip", URL: server.URL + "/redirect"})
	if err != nil || info.Name != "engine.zip" || info.Size != 734003200 {
		t.Fatalf("incorrect preview: %+v, %v", info, err)
	}
	if strings.Join(methods, ",") != "HEAD,HEAD" {
		t.Fatalf("preview requested installer content: %v", methods)
	}
}

func TestDownloadPreviewRejectsUnavailableSize(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusNotFound, http.StatusMethodNotAllowed} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
			}))
			defer server.Close()
			if _, err := inspectDownload(Download{Name: "installer", URL: server.URL}); err == nil || strings.Contains(err.Error(), "download connection interrupted:") {
				t.Fatalf("missing size or rejected request was accepted or called a connection interruption: %v", err)
			}
		})
	}
}

func TestDownloadReportsBytesAndVerification(t *testing.T) {
	body := strings.Repeat("synthetic installer", 8192)
	hash := sha256.Sum256([]byte(body))
	for _, test := range []struct {
		name  string
		known bool
		valid bool
	}{
		{"known size", true, true},
		{"unknown size", false, true},
		{"checksum failure", true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if test.known {
					w.Header().Set("Content-Length", fmt.Sprint(len(body)))
				} else {
					w.(http.Flusher).Flush()
				}
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			var events []DownloadProgress
			pr := &Provisioner{Progress: func(p DownloadProgress) { events = append(events, p) }}
			sum := hex.EncodeToString(hash[:])
			if !test.valid {
				sum = strings.Repeat("0", 64)
			}
			path, err := pr.download(Download{Name: "fixture-installer", URL: server.URL, SHA256: sum})
			if test.valid {
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := os.Remove(path); err != nil {
						t.Errorf("remove downloaded fixture: %v", err)
					}
				})
				data, err := os.ReadFile(path)
				if err != nil || string(data) != body {
					t.Fatal("download contents changed")
				}
			} else if err == nil || path != "" || strings.Contains(err.Error(), "download connection interrupted:") {
				t.Fatalf("checksum mismatch did not fail distinctly from a connection interruption: %v", err)
			}
			if len(events) < 4 || events[0].Phase != "connecting" || events[1].Phase != "downloading" {
				t.Fatalf("missing download lifecycle: %+v", events)
			}
			last := events[len(events)-1]
			wantPhase := "complete"
			if !test.valid {
				wantPhase = "failed"
			}
			total := int64(0)
			if test.known {
				total = int64(len(body))
			}
			if last.Phase != wantPhase || last.Done != int64(len(body)) || last.Total != total {
				t.Fatalf("incorrect terminal progress: %+v", last)
			}
			var previous int64
			sawBytes := false
			for _, event := range events {
				if event.Name != "fixture-installer" || event.Done < previous {
					t.Fatalf("invalid progress: %+v", event)
				}
				if event.Phase == "complete" && !test.valid {
					t.Fatal("verification failure reported completion")
				}
				previous = event.Done
				if event.Phase == "downloading" && event.Done > 0 {
					sawBytes = true
				}
			}
			if !sawBytes {
				t.Fatal("missing byte progress while downloading")
			}
		})
	}
}

func deploymentPins(t *testing.T) *release.Pins {
	t.Helper()
	data, err := os.ReadFile("../../../deployments/.env")
	if err != nil {
		t.Fatal(err)
	}
	config, err := os.ReadFile("../../../versions.toml")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"care_engine_aarch64", "care_engine_x86_64"} {
		section := "[tools." + name + "]"
		config = []byte(strings.Replace(string(config), section, section+"\nsha256 = \""+strings.Repeat("a", 64)+"\"", 1))
	}
	pins, err := release.LoadSource(config, data)
	if err != nil {
		t.Fatal(err)
	}
	return pins
}

func pinnedDownloads(t *testing.T, p *release.Pins) []Download {
	t.Helper()
	var all []Download
	seen := map[string]bool{}
	for _, target := range [][2]string{{"darwin", "arm64"}, {"darwin", "amd64"}, {"windows", "amd64"}} {
		downloads, err := engineDownloads(p, target[0], target[1])
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range downloads {
			if !seen[d.URL] {
				seen[d.URL] = true
				all = append(all, d)
			}
		}
	}
	for _, goarch := range []string{"amd64", "arm64"} {
		engine, err := dockerLinuxDownloads(p, goarch)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, engine)
	}
	return all
}

func TestWindowsQEMUUsesDeploymentPins(t *testing.T) {
	p := deploymentPins(t)
	downloads, err := engineDownloads(p, "windows", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	want := Download{
		Name: "qemu-w64-setup-20250826.exe", URL: p.QEMUWindowsURL,
		SHA256: p.QEMUWindowsSHA256,
	}
	if len(downloads) != 4 || downloads[1] != want ||
		downloads[2].URL != p.SevenZipBootstrapURL || downloads[2].SHA256 != p.SevenZipBootstrapSHA256 ||
		downloads[3].URL != p.SevenZipWindowsURL || downloads[3].SHA256 != p.SevenZipWindowsSHA256 {
		t.Fatalf("Windows provisioning must automatically include pinned QEMU: %+v", downloads)
	}
	for _, d := range downloads[1:] {
		if strings.Contains(d.URL, p.CareVMBaseURL) {
			t.Fatalf("third-party software must download directly from upstream: %+v", d)
		}
	}
	if want.SHA256 == strings.Repeat("0", 64) {
		t.Fatal("QEMU must have a real bundle checksum")
	}
}

func TestEngineDownloadsMatchPlatform(t *testing.T) {
	p := &release.Pins{
		CareVMVersion: "0.1.0", CareVMBaseURL: "https://example.test/engine/",
		CareVMAarch64SHA256: "vm-arm", CareVMX8664SHA256: "vm-intel",
		QEMUWindowsVersion: "10.1.0", QEMUWindowsSHA256: "qemu", QEMUWindowsURL: "https://qemu.example.test/setup.exe",
		SevenZipBootstrapURL: "https://7zip.example.test/7zr.exe", SevenZipBootstrapSHA256: "bootstrap",
		SevenZipWindowsURL: "https://7zip.example.test/7zip.exe", SevenZipWindowsSHA256: "extractor",
	}

	for _, tc := range []struct {
		goos, goarch string
		want         []Download
	}{
		{"darwin", "arm64", []Download{
			{"care-engine-0.1.0-linux-arm64.tar.gz", "https://example.test/engine/care-engine-0.1.0-linux-arm64.tar.gz", "vm-arm"},
		}},
		{"darwin", "amd64", []Download{
			{"care-engine-0.1.0-linux-amd64.tar.gz", "https://example.test/engine/care-engine-0.1.0-linux-amd64.tar.gz", "vm-intel"},
		}},
		{"windows", "amd64", []Download{
			{"care-engine-0.1.0-linux-amd64.tar.gz", "https://example.test/engine/care-engine-0.1.0-linux-amd64.tar.gz", "vm-intel"},
			{"setup.exe", "https://qemu.example.test/setup.exe", "qemu"},
			{"7zr.exe", "https://7zip.example.test/7zr.exe", "bootstrap"},
			{"7zip.exe", "https://7zip.example.test/7zip.exe", "extractor"},
		}},
	} {
		got, err := engineDownloads(p, tc.goos, tc.goarch)
		if err != nil || fmt.Sprint(got) != fmt.Sprint(tc.want) {
			t.Errorf("%s/%s: got %+v, %v", tc.goos, tc.goarch, got, err)
		}
	}
	for _, target := range [][2]string{{"windows", "arm64"}, {"linux", "amd64"}} {
		if _, err := engineDownloads(p, target[0], target[1]); err == nil {
			t.Errorf("%s/%s: expected no engine download", target[0], target[1])
		}
	}
}

func TestWindowsUpstreamDownloadsRejectInvalidURLs(t *testing.T) {
	for _, raw := range []string{"", "http://example.test/qemu.exe", "https://user:password@example.test/qemu.exe",
		"https://example.test/qemu.exe#fragment", "https://example.test/"} {
		t.Run(raw, func(t *testing.T) {
			p := *deploymentPins(t)
			p.QEMUWindowsURL = raw
			if _, err := engineDownloads(&p, "windows", "amd64"); err == nil {
				t.Fatal("invalid upstream URL accepted")
			}
		})
	}
}

func TestIncompleteEngineDownloadsFailBeforeDownloading(t *testing.T) {
	pr := &Provisioner{}
	for _, count := range []int{0, 2, 3, 5, 6, 7} {
		if err := pr.installEngineDownloads(make([]Download, count), nil); err == nil {
			t.Fatalf("incomplete download configuration accepted: %d", count)
		}
	}
}

func TestDockerLinuxDownloadsMatchArchitecture(t *testing.T) {
	p := &release.Pins{
		DockerLinuxVersion: "29.8.1", DockerLinuxX8664SHA256: "engine-intel", DockerLinuxAarch64SHA256: "engine-arm",
	}
	for _, tc := range []struct{ goarch, arch, engineSum string }{
		{"amd64", "x86_64", "engine-intel"},
		{"arm64", "aarch64", "engine-arm"},
	} {
		engine, err := dockerLinuxDownloads(p, tc.goarch)
		if err != nil {
			t.Fatal(err)
		}
		wantEngine := Download{
			Name:   "docker-29.8.1.tgz",
			URL:    "https://download.docker.com/linux/static/stable/" + tc.arch + "/docker-29.8.1.tgz",
			SHA256: tc.engineSum,
		}
		if engine != wantEngine {
			t.Errorf("%s: got %+v", tc.goarch, engine)
		}
	}
	if _, err := dockerLinuxDownloads(p, "riscv64"); err == nil {
		t.Error("riscv64: expected no Docker download")
	}
}

func TestLinuxAgentDownloadsMatchSignedAssets(t *testing.T) {
	base := "https://example.test/care-engine-v0.1.3"
	p := release.EngineUpdate{Version: "0.1.3", Assets: map[string]release.Asset{}}
	for _, tc := range []struct {
		goarch, name, sum string
	}{
		{"amd64", "care-engine-agent-linux-amd64", strings.Repeat("a", 64)},
		{"arm64", "care-engine-agent-linux-arm64", strings.Repeat("b", 64)},
	} {
		p.Assets["native-linux-"+tc.goarch] = release.Asset{URL: base + "/" + tc.name, SHA256: tc.sum, Size: 42}
		got, err := linuxAgentDownload(p, tc.goarch)
		want := Download{Name: tc.name, URL: base + "/" + tc.name, SHA256: tc.sum}
		if err != nil || got != want {
			t.Errorf("%s: got %+v, %v; want %+v", tc.goarch, got, err, want)
		}
	}
	if _, err := linuxAgentDownload(p, "riscv64"); err == nil {
		t.Error("riscv64: expected no native agent download")
	}
}

// CI verifies every [tools] entry of versions.toml; the app must download
// exactly those pinned URLs and checksums.
func TestVersionsTOMLPinsTheDownloadsTheAppUses(t *testing.T) {
	data, err := os.ReadFile("../../../" + release.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	config, err := release.ReadConfig(data)
	if err != nil {
		t.Fatal(err)
	}
	pinned := map[string]string{}
	for _, tool := range config.Tools {
		sum := tool.SHA256
		if sum == "" {
			sum = strings.Repeat("a", 64) // Resolved engine fixture, not a published checksum.
		}
		pinned[tool.URL] = sum
	}
	used := map[string]bool{}
	for _, d := range pinnedDownloads(t, deploymentPins(t)) {
		if sum, ok := pinned[d.URL]; !ok || sum != d.SHA256 {
			t.Errorf("%s (%s) is not pinned in %s", d.URL, d.SHA256, release.ConfigFile)
		}
		used[d.URL] = true
	}
	for url := range pinned {
		if !used[url] {
			t.Errorf("%s pins %s, which the app never downloads", release.ConfigFile, url)
		}
	}
}

func TestResolvedEngineBootstrapIsDownloadableOnEveryDesktopTarget(t *testing.T) {
	pins := deploymentPins(t)
	if err := engineBootstrapPublished(pins); err != nil {
		t.Fatal(err)
	}
	for _, target := range [][2]string{{"darwin", "arm64"}, {"darwin", "amd64"}, {"windows", "amd64"}} {
		downloads, err := engineDownloads(pins, target[0], target[1])
		if err != nil {
			t.Fatal(err)
		}
		if err := engineDownloadsPublished(downloads); err != nil {
			t.Fatalf("%s/%s bootstrap blocked: %v", target[0], target[1], err)
		}
		want := strings.Repeat("a", 64)
		if downloads[0].SHA256 != want || !strings.HasPrefix(downloads[0].URL,
			"https://github.com/praffq/care_clinic/releases/download/care-engine-v0.1.3/") {
			t.Fatalf("bootstrap differs from resolved engine fixture: %+v", downloads[0])
		}
	}
}

func TestEngineUpdateLeavesClinicAloneWhenDownloadFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "missing", http.StatusNotFound)
	}))
	defer server.Close()
	pr := NewProvisioner(proc.Runner{}, nil, nil)
	stopped := false
	err := pr.installEngineDownloads([]Download{
		{Name: "care-engine-test.tar.gz", URL: server.URL, SHA256: strings.Repeat("0", 64)},
	}, func() error { stopped = true; return nil })
	if err == nil {
		t.Fatal("update must fail when the engine cannot be downloaded")
	}
	if stopped {
		t.Fatal("the clinic was stopped before the new engine was downloaded")
	}
}
