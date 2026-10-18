//go:build darwin && cgo

package vm

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewRejectsInvalidConfig(t *testing.T) {
	if _, err := New(Config{}); err == nil || !strings.Contains(err.Error(), "bundle directory") {
		t.Fatalf("New: %v", err)
	}
}

// Start fails before touching Virtualization.framework, so this needs no
// entitlement.
func TestStartInvalidBundle(t *testing.T) {
	dir := t.TempDir()
	m, err := New(Config{BundleDir: dir, DataDisk: filepath.Join(dir, "data.img"), ConsoleLog: filepath.Join(dir, "console.log")})
	if err != nil {
		t.Fatal(err)
	}
	err = m.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "missing vmlinuz") {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-m.Done():
	default:
		t.Fatal("Done not closed after a failed Start")
	}
	if m.State() != StateError || !errors.Is(m.Err(), err) {
		t.Fatalf("after failed Start: state %s err %v", m.State(), m.Err())
	}
	if err := m.Start(context.Background()); !errors.Is(err, ErrAlreadyStarted) {
		t.Fatalf("second Start: %v", err)
	}
	if _, err := m.DialVsock(context.Background(), AgentVsockPort); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("DialVsock: %v", err)
	}
	if err := m.Stop(context.Background(), time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "data.img")); !os.IsNotExist(err) {
		t.Fatalf("data disk created for an invalid bundle: %v", err)
	}
}

// TestBootEngine boots a real engine bundle. It needs the virtualization
// entitlement, so build the test binary, sign it and run it directly:
//
//	go test -c -o /tmp/vm.test ./internal/engine/vm
//	codesign -f -s - --entitlements cmd/vzboot/vzboot.entitlements /tmp/vm.test
//	CARE_VZ_BUNDLE=$PWD/../dist/care-engine-0.1.0-arm64 /tmp/vm.test -test.run TestBootEngine -test.v
func TestBootEngine(t *testing.T) {
	bundle := os.Getenv("CARE_VZ_BUNDLE")
	if bundle == "" {
		t.Skip("CARE_VZ_BUNDLE not set")
	}
	dir := t.TempDir()
	cfg := Config{
		BundleDir:    bundle,
		DataDisk:     filepath.Join(dir, "data.img"),
		DataDiskSize: 8 << 30,
		MemoryMiB:    2048,
		ConsoleLog:   filepath.Join(dir, "console.log"),
	}
	ctx := context.Background()

	// Cold boot on a blank data disk, then a graceful agent shutdown.
	m, took := bootUntilReady(t, cfg)
	t.Logf("cold boot: agent docker_ready after %s", took)

	other, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Start(ctx); !errors.Is(err, ErrDataDiskInUse) {
		t.Fatalf("second VM on the same data disk: %v", err)
	}

	dctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	if c, err := m.DialVsock(dctx, 4242); err == nil {
		_ = c.Close()
		t.Error("dial to a closed vsock port succeeded")
	}
	cancel()
	checkHalfClose(t, m)

	start := time.Now()
	if code, _, err := agentDo(m, http.MethodPost, "/v1/shutdown"); err != nil || code != http.StatusAccepted {
		t.Fatalf("POST /v1/shutdown: %d %v", code, err)
	}
	if err := m.Stop(ctx, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	took = time.Since(start)
	if took >= 30*time.Second || m.State() != StateStopped || m.Err() != nil {
		t.Fatalf("graceful stop: took %s, state %s, err %v", took, m.State(), m.Err())
	}
	t.Logf("graceful shutdown: VM stopped %s after POST /v1/shutdown", took.Round(time.Millisecond))
	if _, err := m.DialVsock(ctx, AgentVsockPort); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("DialVsock after stop: %v", err)
	}

	// Warm boot on the now-formatted disk; Stop without asking the agent
	// times out and force-stops.
	m, took = bootUntilReady(t, cfg)
	t.Logf("warm boot: agent docker_ready after %s", took)
	start = time.Now()
	if err := m.Stop(ctx, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if m.State() != StateStopped || m.Err() != nil {
		t.Fatalf("force stop: state %s, err %v", m.State(), m.Err())
	}
	t.Logf("Stop without agent shutdown: force-stopped after %s", time.Since(start).Round(time.Millisecond))

	log, err := os.ReadFile(cfg.ConsoleLog)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(log), "=== engine VM boot "); n != 2 {
		t.Errorf("console log has %d boot headers, want 2", n)
	}
	if !strings.Contains(string(log), "care-init:") {
		t.Error("console log has no care-init output")
	}
}

func bootUntilReady(t *testing.T, cfg Config) (Machine, time.Duration) {
	t.Helper()
	m, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.ForceStop(context.Background()) })
	if m.State() != StateRunning {
		t.Fatalf("state after Start: %s", m.State())
	}
	for deadline := start.Add(3 * time.Minute); time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
		select {
		case <-m.Done():
			t.Fatalf("VM stopped while booting: %v; see %s", m.Err(), cfg.ConsoleLog)
		default:
		}
		code, body, err := agentDo(m, http.MethodGet, "/v1/status")
		if err == nil && code == http.StatusOK && strings.Contains(body, `"docker_ready":true`) {
			return m, time.Since(start).Round(time.Millisecond)
		}
	}
	t.Fatalf("agent not ready after 3m; see %s", cfg.ConsoleLog)
	return nil, 0
}

// checkHalfClose sends a keep-alive request and half-closes: the agent only
// closes the connection (ending ReadAll) if it sees the EOF, while the
// response must still arrive over the open read half.
func checkHalfClose(t *testing.T, m Machine) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := m.DialVsock(ctx, AgentVsockPort)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	cw, ok := c.(interface{ CloseWrite() error })
	if !ok {
		t.Fatalf("DialVsock returned %T without CloseWrite", c)
	}
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(c, "GET /v1/status HTTP/1.1\r\nHost: agent\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	if err := cw.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(c)
	if err != nil || !strings.HasPrefix(string(b), "HTTP/1.1 200 ") {
		t.Fatalf("half-closed request: %q, %v", b, err)
	}
}

// agentDo makes one HTTP/1.1 request over a raw vsock connection.
func agentDo(m Machine, method, path string) (int, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := m.DialVsock(ctx, AgentVsockPort)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := fmt.Fprintf(c, "%s %s HTTP/1.1\r\nHost: agent\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", method, path); err != nil {
		return 0, "", err
	}
	req, _ := http.NewRequest(method, path, nil)
	resp, err := http.ReadResponse(bufio.NewReader(c), req)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), err
}
