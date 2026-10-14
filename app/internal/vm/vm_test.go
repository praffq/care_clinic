package vm

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestCommandLine(t *testing.T) {
	m := Manifest{System: "/nix/store/abc-system", Cmdline: "console=hvc0 console=ttyS0 reboot=t panic=1 loglevel=4"}
	if got := m.commandLine("ttyS0"); got != "console=ttyS0 reboot=t panic=1 loglevel=4 init=/nix/store/abc-system/init" {
		t.Fatal(got)
	}
}

func TestConfigDefaults(t *testing.T) {
	c := Config{}.withDefaults(8)
	if c.CPUs != 4 || c.MemoryMB != 4096 || c.DiskGB != 64 {
		t.Fatalf("%+v", c)
	}
	if c := (Config{CPUs: 16, MemoryMB: 512}).withDefaults(2); c.CPUs != 2 || c.MemoryMB != minMemoryMB {
		t.Fatalf("%+v", c)
	}
}

func TestDarwinBundleMinimumSupportsVirtualizationDataDiskID(t *testing.T) {
	data, err := os.ReadFile("../../build/darwin/Info.plist")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "<string>12.3.0</string>") {
		t.Fatal("macOS bundle minimum is below the VM data disk identifier requirement")
	}
}

func writeTarGz(t *testing.T, p string, files map[string]string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		_ = tw.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: int64(len(files[n])), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(files[n]))
	}
	_ = tw.Close()
	_ = gz.Close()
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func sum(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

func TestStagedEngineChecksAndExtracts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("LOCALAPPDATA", home)
	files := engineFiles("0.1.0", "k")
	files["../evil"] = "x"
	arc := filepath.Join(t.TempDir(), "engine.tar.gz")
	writeTarGz(t, arc, files)
	installEngineForTest(t, arc)
	m, err := Installed()
	if err != nil || m.Version != "0.1.0" {
		t.Fatalf("%+v %v", m, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(engineDir()), "evil")); !os.IsNotExist(err) {
		t.Fatal("path traversal entry was extracted")
	}
	if err := prepareDataDisk(context.Background(), m, 1); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(dataDisk()); fi.Size() != 1<<30 {
		t.Fatalf("data disk size %d", fi.Size())
	}
	files["kernel"] = "tampered"
	writeTarGz(t, arc, files)
	if _, err := StageEngineUpdate(toolFiles(t, arc, "v2")); err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("tampered bundle accepted: %v", err)
	}
}

func TestHelperPublishesStartupFailureBeforeVMIsReady(t *testing.T) {
	h := &host{status: HostStatus{Running: true, Error: "data disk: unavailable"}}
	response := httptest.NewRecorder()
	h.startupRoutes().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/status", nil))
	var status HostStatus
	if err := json.NewDecoder(response.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || status.Error != "data disk: unavailable" {
		t.Fatalf("startup status = %d %+v", response.Code, status)
	}
	if ready, err := statusOutcome(status); ready || err == nil || !strings.Contains(err.Error(), status.Error) {
		t.Fatalf("startup error outcome = %t %v", ready, err)
	}
}

func TestEnsureStatusDoesNotAcceptDockerBeforeAttachError(t *testing.T) {
	status := HostStatus{Running: true, Docker: true, Error: "attach failed"}
	if ready, err := statusOutcome(status); ready || err == nil || !strings.Contains(err.Error(), "attach failed") {
		t.Fatalf("attach failure status = %t %v", ready, err)
	}
}
