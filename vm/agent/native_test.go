package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestNativeAgentSubprocess(t *testing.T) {
	socket := os.Getenv("CARE_AGENT_SOCKET_FIXTURE")
	if socket == "" {
		return
	}
	if err := runNative(socket); err != nil {
		t.Fatal(err)
	}
}

func TestNativeAgentSocketServesOperations(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native agent runs on Linux")
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte("#!/bin/sh\nprintf 'native-agent-result\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Keep the absolute path under macOS's 104-byte Unix socket limit even
	// when tests must store fixtures inside a long worktree instead of /tmp.
	var suffix [2]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	socketDir, err := filepath.Abs(filepath.Join("../..", fmt.Sprintf(".s%x", suffix)))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(socketDir, 0o700); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(socketDir) }()
	socket := filepath.Join(socketDir, "s")
	cmd := exec.Command(os.Args[0], "-test.run=^TestNativeAgentSubprocess$")
	cmd.Env = append(os.Environ(), "CARE_AGENT_SOCKET_FIXTURE="+socket, "PATH="+bin)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = os.Remove(socket)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, err := net.DialTimeout("unix", socket, 100*time.Millisecond)
		if err == nil {
			_ = c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("native socket did not become ready:", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	info, err := os.Stat(socket)
	if err != nil || info.Mode().Perm() != 0o660 {
		t.Fatalf("unprotected native socket: %v %v", info, err)
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	resp, err := client.Post("http://agent/v1/jobs", "application/json", strings.NewReader(`{"operation":"version"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	dec := json.NewDecoder(resp.Body)
	var output strings.Builder
	for {
		var frame jobEvent
		if err := dec.Decode(&frame); err != nil {
			t.Fatal(err)
		}
		output.WriteString(frame.Data)
		if frame.Error != "" {
			t.Fatal(frame.Error)
		}
		if frame.Exit != nil {
			if *frame.Exit != 0 || output.String() != "native-agent-result\n" {
				t.Fatalf("exit=%d output=%q", *frame.Exit, output.String())
			}
			break
		}
	}
}

func TestNativeAgentRejectsUnprotectedSocketDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := runNative(filepath.Join(dir, "agent.sock")); err == nil {
		t.Fatal("world-accessible socket directory was accepted")
	}
}
