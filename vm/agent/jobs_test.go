package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func jobFixture(t *testing.T, script string) *httptest.Server {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX guest-command fixture")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte("#!/bin/sh\n"+script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	a := newAPI("/missing-docker.sock")
	t.Cleanup(a.closeIdleConnections)
	server := httptest.NewServer(a.routes())
	t.Cleanup(server.Close)
	return server
}

func TestJobsStreamBothOutputsAndExitStatus(t *testing.T) {
	server := jobFixture(t, "printf '%s\\n' \"$1:$CLINIC_JOB_TEST\"\nprintf 'diagnostic\\n' >&2\nexit 7\n")
	in, _ := json.Marshal(operationRequest{Operation: "compose", Args: []string{"ps"}, Env: []string{"CLINIC_JOB_TEST=fixture"}})
	resp, err := http.Post(server.URL+"/v1/jobs", "application/json", bytes.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatal(resp.Status)
	}
	dec := json.NewDecoder(resp.Body)
	var stdout, stderr strings.Builder
	for {
		var e jobEvent
		if err := dec.Decode(&e); err != nil {
			t.Fatal(err)
		}
		switch e.Stream {
		case "stdout":
			stdout.WriteString(e.Data)
		case "stderr":
			stderr.WriteString(e.Data)
		}
		if e.Exit != nil {
			if *e.Exit != 7 || stdout.String() != "compose:fixture\n" || stderr.String() != "diagnostic\n" {
				t.Fatalf("exit %d stdout=%q stderr=%q", *e.Exit, stdout.String(), stderr.String())
			}
			break
		}
	}
}

func TestJobsRejectInvalidRequests(t *testing.T) {
	server := jobFixture(t, "exit 99")
	for _, body := range []string{
		`{"operation":"shell","args":["rm"]}`,
		`{"operation":"compose","dir":"relative"}`,
		`{"operation":"compose","env":["DOCKER_HOST=tcp://elsewhere"]}`,
		`{"operation":"compose","env":["PATH=/untrusted"]}`,
		`{"operation":"compose","unexpected":true}`,
		`{"operation":"compose"} {}`,
	} {
		resp, err := http.Post(server.URL+"/v1/jobs", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: %s", body, resp.Status)
		}
	}
}

func TestBuildxVersionIsAnAgentOperation(t *testing.T) {
	if err := validateOperation(operationRequest{Operation: "buildx", Args: []string{"version"}}); err != nil {
		t.Fatalf("buildx plugin operation is unavailable: %v", err)
	}
}

func TestJobsFlushBeforeCommandExitsAndCancel(t *testing.T) {
	server := jobFixture(t, "printf 'started\\n'\nsleep 30\n")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/jobs",
		strings.NewReader(`{"operation":"exec","args":["container","job"]}`))
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal("output was not flushed while the command ran:", err)
	}
	var e jobEvent
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil || e.Data != "started\n" {
		t.Fatalf("first event %+v: %v", e, err)
	}
	cancel()
	_, err = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err == nil {
		t.Fatal("cancelled job stream unexpectedly succeeded")
	}
}

func TestNativeModeDoesNotExposeVMShutdown(t *testing.T) {
	a := newAPI("/missing.sock")
	defer a.closeIdleConnections()
	a.native = true
	for _, endpoint := range []string{"/v1/shutdown", "/v1/time", "/v1/mount"} {
		w := httptest.NewRecorder()
		a.routes().ServeHTTP(w, httptest.NewRequest(http.MethodPost, endpoint, nil))
		if w.Code != http.StatusNotFound {
			t.Fatalf("native %s returned %d", endpoint, w.Code)
		}
	}
}
