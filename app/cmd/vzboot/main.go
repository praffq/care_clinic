//go:build darwin

// Command vzboot boots a CARE engine bundle with Apple's
// Virtualization.framework and talks to care-engine-agent over virtio-vsock.
// It is a development harness for the macOS host service, not shipped.
//
//	vzboot --bundle dist/care-engine-0.1.0-arm64 --data /tmp/data.img [--mem 4096 --cpus 2]
//
// The binary needs the com.apple.security.virtualization entitlement; build
// it with build.sh in this directory.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/vm"
)

const stopTimeout = 30 * time.Second

type callFlags []string

func (c *callFlags) String() string     { return strings.Join(*c, ", ") }
func (c *callFlags) Set(v string) error { *c = append(*c, v); return nil }

func main() {
	bundle := flag.String("bundle", "", "extracted engine bundle directory (vmlinuz, initramfs, system.img)")
	data := flag.String("data", "", "persistent data disk image; created sparse (64 GiB) when absent")
	mem := flag.Uint64("mem", 4096, "memory in MiB")
	cpus := flag.Uint("cpus", 2, "virtual CPUs")
	consoleLog := flag.String("console-log", "", "console log file (default: <data>.console.log)")
	readyTimeout := flag.Duration("timeout", 3*time.Minute, "how long to wait for docker_ready")
	exit := flag.Bool("exit", false, "shut the VM down after the status check and calls instead of waiting for SIGINT")
	var calls callFlags
	flag.Var(&calls, "call", `extra request to make once ready, "METHOD /path [JSON body]" (repeatable), e.g. "POST /v1/ops/stack.status"`)
	flag.Parse()
	log.SetFlags(log.Ltime | log.Lmicroseconds)
	if *bundle == "" || *data == "" {
		flag.Usage()
		os.Exit(2)
	}
	if *consoleLog == "" {
		*consoleLog = *data + ".console.log"
	}
	if err := run(*bundle, *data, *consoleLog, *mem, *cpus, *readyTimeout, calls, *exit); err != nil {
		log.Fatal(err)
	}
}

// agentClient returns an HTTP client whose connections are vsock streams to
// the agent.
func agentClient(m vm.Machine) *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return m.DialVsock(ctx, vm.AgentVsockPort)
		},
		DisableKeepAlives: true,
	}}
}

func do(ctx context.Context, c *http.Client, method, path string, body []byte) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, "http://agent"+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	return resp.StatusCode, bytes.TrimSpace(b), err
}

func run(bundle, data, consoleLog string, mem uint64, cpus uint, readyTimeout time.Duration, calls []string, exit bool) error {
	created, err := vm.EnsureDataDisk(data, vm.DefaultDataDiskSize)
	if err != nil {
		return err
	}
	if created {
		log.Printf("created sparse data disk %s (%d GiB)", data, vm.DefaultDataDiskSize>>30)
	}
	m, err := vm.New(vm.Config{
		BundleDir:     bundle,
		DataDisk:      data,
		MemoryMiB:     mem,
		CPUs:          cpus,
		ConsoleLog:    consoleLog,
		ConsoleMirror: os.Stdout,
	})
	if err != nil {
		return err
	}

	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, os.Interrupt, syscall.SIGTERM)
	ctx := context.Background()
	start := time.Now()
	if err := m.Start(ctx); err != nil {
		return err
	}
	log.Printf("VM started (%d vCPU, %d MiB); console log: %s", cpus, mem, consoleLog)
	client := agentClient(m)
	stopped := m.Done()

	deadline := time.Now().Add(readyTimeout)
	for ready := false; !ready; {
		select {
		case <-stopped:
			return fmt.Errorf("VM stopped before the agent was ready (state %s, err %v); see %s", m.State(), m.Err(), consoleLog)
		case <-sigc:
			return shutdown(ctx, m, client)
		case <-time.After(time.Second):
		}
		code, body, err := do(ctx, client, http.MethodGet, "/v1/status", nil)
		switch {
		case err == nil && code == http.StatusOK && bytes.Contains(body, []byte(`"docker_ready":true`)):
			ready = true
			log.Printf("agent ready after %s: GET /v1/status -> %s", time.Since(start).Round(time.Millisecond), body)
		case time.Now().After(deadline):
			return fmt.Errorf("agent not ready after %s: code=%d err=%v body=%s", readyTimeout, code, err, body)
		}
	}

	for _, c := range calls {
		method, rest, ok := strings.Cut(c, " ")
		if !ok {
			return fmt.Errorf("--call %q: want \"METHOD /path [JSON body]\"", c)
		}
		path, reqBody, _ := strings.Cut(rest, " ")
		var body []byte
		if reqBody != "" {
			body = []byte(reqBody)
		} else if method == http.MethodPost || method == http.MethodPut {
			body = []byte("{}")
		}
		code, out, err := do(ctx, client, method, path, body)
		if err != nil {
			return fmt.Errorf("%s: %w", c, err)
		}
		log.Printf("%s -> %d %s", c, code, out)
	}

	if !exit {
		log.Print("running; press Ctrl-C to shut the engine down")
		select {
		case <-sigc:
		case <-stopped:
			return fmt.Errorf("VM stopped unexpectedly (state %s, err %v); see %s", m.State(), m.Err(), consoleLog)
		}
	}
	return shutdown(ctx, m, client)
}

// shutdown asks the agent to power the guest off and force-stops the VM if
// it is still running 30s later.
func shutdown(ctx context.Context, m vm.Machine, client *http.Client) error {
	start := time.Now()
	code, body, err := do(ctx, client, http.MethodPost, "/v1/shutdown", nil)
	log.Printf("POST /v1/shutdown -> %d %s (err=%v)", code, body, err)
	if err := m.Stop(ctx, stopTimeout); err != nil {
		return err
	}
	took := time.Since(start).Round(time.Millisecond)
	if took >= stopTimeout {
		log.Printf("VM force-stopped after %s", took)
	} else {
		log.Printf("VM powered off after %s", took)
	}
	return m.Err()
}
