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
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Code-Hex/vz/v3"
)

const (
	agentVsockPort  = 1024
	defaultDataSize = 64 << 30
	cmdline         = "console=hvc0 care.system=/dev/vda care.data=/dev/vdb"
)

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
	flag.Var(&calls, "call", `extra request to make once ready, "METHOD /path" (repeatable), e.g. "POST /v1/ops/stack.status"`)
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

// ensureDataDisk creates a sparse, all-zero disk image; the engine formats a
// blank disk on first boot.
func ensureDataDisk(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := f.Truncate(defaultDataSize); err != nil {
		_ = f.Close()
		return err
	}
	log.Printf("created sparse data disk %s (%d GiB)", path, defaultDataSize>>30)
	return f.Close()
}

func newConfig(bundle, data string, mem uint64, cpus uint, console *os.File) (*vz.VirtualMachineConfiguration, error) {
	boot, err := vz.NewLinuxBootLoader(filepath.Join(bundle, "vmlinuz"),
		vz.WithInitrd(filepath.Join(bundle, "initramfs")),
		vz.WithCommandLine(cmdline))
	if err != nil {
		return nil, fmt.Errorf("boot loader: %w", err)
	}
	cfg, err := vz.NewVirtualMachineConfiguration(boot, cpus, mem<<20)
	if err != nil {
		return nil, err
	}

	// The guest console is output-only; its input is an idle pipe.
	in, _, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	serialAtt, err := vz.NewFileHandleSerialPortAttachment(in, console)
	if err != nil {
		return nil, err
	}
	serial, err := vz.NewVirtioConsoleDeviceSerialPortConfiguration(serialAtt)
	if err != nil {
		return nil, err
	}
	cfg.SetSerialPortsVirtualMachineConfiguration([]*vz.VirtioConsoleDeviceSerialPortConfiguration{serial})

	sysAtt, err := vz.NewDiskImageStorageDeviceAttachment(filepath.Join(bundle, "system.img"), true)
	if err != nil {
		return nil, fmt.Errorf("system.img: %w", err)
	}
	sysDev, err := vz.NewVirtioBlockDeviceConfiguration(sysAtt)
	if err != nil {
		return nil, err
	}
	dataAtt, err := vz.NewDiskImageStorageDeviceAttachmentWithCacheAndSync(data, false,
		vz.DiskImageCachingModeAutomatic, vz.DiskImageSynchronizationModeFsync)
	if err != nil {
		return nil, fmt.Errorf("data disk: %w", err)
	}
	dataDev, err := vz.NewVirtioBlockDeviceConfiguration(dataAtt)
	if err != nil {
		return nil, err
	}
	// Order matters: system.img is /dev/vda, the data disk /dev/vdb.
	cfg.SetStorageDevicesVirtualMachineConfiguration([]vz.StorageDeviceConfiguration{sysDev, dataDev})

	natAtt, err := vz.NewNATNetworkDeviceAttachment()
	if err != nil {
		return nil, err
	}
	net0, err := vz.NewVirtioNetworkDeviceConfiguration(natAtt)
	if err != nil {
		return nil, err
	}
	mac, err := vz.NewRandomLocallyAdministeredMACAddress()
	if err != nil {
		return nil, err
	}
	net0.SetMACAddress(mac)
	cfg.SetNetworkDevicesVirtualMachineConfiguration([]*vz.VirtioNetworkDeviceConfiguration{net0})

	entropy, err := vz.NewVirtioEntropyDeviceConfiguration()
	if err != nil {
		return nil, err
	}
	cfg.SetEntropyDevicesVirtualMachineConfiguration([]*vz.VirtioEntropyDeviceConfiguration{entropy})

	balloon, err := vz.NewVirtioTraditionalMemoryBalloonDeviceConfiguration()
	if err != nil {
		return nil, err
	}
	cfg.SetMemoryBalloonDevicesVirtualMachineConfiguration([]vz.MemoryBalloonDeviceConfiguration{balloon})

	sock, err := vz.NewVirtioSocketDeviceConfiguration()
	if err != nil {
		return nil, err
	}
	cfg.SetSocketDevicesVirtualMachineConfiguration([]vz.SocketDeviceConfiguration{sock})

	if ok, err := cfg.Validate(); !ok || err != nil {
		return nil, fmt.Errorf("invalid VM configuration: %w", err)
	}
	return cfg, nil
}

// agentClient returns an HTTP client whose connections are vsock streams to
// the agent.
func agentClient(dev *vz.VirtioSocketDevice) *http.Client {
	type result struct {
		c   net.Conn
		err error
	}
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			ch := make(chan result, 1)
			go func() {
				c, err := dev.Connect(agentVsockPort)
				if err != nil {
					ch <- result{nil, err}
					return
				}
				ch <- result{c, nil}
			}()
			select {
			case r := <-ch:
				return r.c, r.err
			case <-ctx.Done():
				go func() {
					if r := <-ch; r.c != nil {
						_ = r.c.Close()
					}
				}()
				return nil, ctx.Err()
			}
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
	if err := ensureDataDisk(data); err != nil {
		return fmt.Errorf("data disk: %w", err)
	}
	logf, err := os.Create(consoleLog)
	if err != nil {
		return err
	}
	defer func() { _ = logf.Close() }()
	consoleR, consoleW, err := os.Pipe()
	if err != nil {
		return err
	}
	go func() { _, _ = io.Copy(io.MultiWriter(logf, os.Stdout), consoleR) }()

	cfg, err := newConfig(bundle, data, mem, cpus, consoleW)
	if err != nil {
		return err
	}
	vm, err := vz.NewVirtualMachine(cfg)
	if err != nil {
		return err
	}

	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, os.Interrupt, syscall.SIGTERM)
	start := time.Now()
	if err := vm.Start(); err != nil {
		return fmt.Errorf("start VM: %w", err)
	}
	log.Printf("VM started (%d vCPU, %d MiB); console log: %s", cpus, mem, consoleLog)
	devs := vm.SocketDevices()
	if len(devs) == 0 {
		return errors.New("VM has no virtio socket device")
	}
	client := agentClient(devs[0])
	ctx := context.Background()

	stopped := make(chan struct{})
	go func() {
		for st := range vm.StateChangedNotify() {
			if st == vz.VirtualMachineStateStopped || st == vz.VirtualMachineStateError {
				close(stopped)
				return
			}
		}
	}()

	deadline := time.Now().Add(readyTimeout)
	for ready := false; !ready; {
		select {
		case <-stopped:
			return fmt.Errorf("VM stopped before the agent was ready (state %s); see %s", vm.State(), consoleLog)
		case <-sigc:
			return shutdown(ctx, vm, client, stopped)
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
		method, path, ok := strings.Cut(c, " ")
		if !ok {
			return fmt.Errorf("--call %q: want \"METHOD /path\"", c)
		}
		var body []byte
		if method == http.MethodPost || method == http.MethodPut {
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
			return fmt.Errorf("VM stopped unexpectedly; see %s", consoleLog)
		}
	}
	return shutdown(ctx, vm, client, stopped)
}

// shutdown asks the agent to power the guest off and force-stops the VM if
// it is still running 30s later.
func shutdown(ctx context.Context, vm *vz.VirtualMachine, client *http.Client, stopped <-chan struct{}) error {
	start := time.Now()
	code, body, err := do(ctx, client, http.MethodPost, "/v1/shutdown", nil)
	log.Printf("POST /v1/shutdown -> %d %s (err=%v)", code, body, err)
	select {
	case <-stopped:
		log.Printf("VM powered off after %s", time.Since(start).Round(time.Millisecond))
		return nil
	case <-time.After(30 * time.Second):
	}
	log.Print("VM still running after 30s; force-stopping")
	if vm.CanStop() {
		return vm.Stop()
	}
	return errors.New("VM cannot be stopped")
}
