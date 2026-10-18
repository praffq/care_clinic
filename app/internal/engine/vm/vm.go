// Package vm boots the CARE engine VM bundle (see engine/README.md) under the
// host's hypervisor. The OS-neutral parts live here: Config, the Machine
// interface, bundle validation and data disk creation. New is implemented per
// OS: Apple Virtualization.framework on macOS (vz_darwin.go), QEMU with the
// Windows Hypervisor Platform on Windows (qemu.go, qemu_windows.go); other
// platforms return errors.ErrUnsupported.
//
// A Machine is one boot: New, Start, and once it has stopped (Done is closed)
// a new Machine is needed to boot again.
//
// Graceful shutdown is the caller's job: send the agent POST /v1/shutdown
// (over DialVsock), then call Stop, which waits for the guest to power off and
// force-stops it on timeout. The agent shuts OpenRC down in order (clinic
// stack, dockerd, filesystems) and the same request works under QEMU, whereas
// the engine image has no ACPI/power-key handling for a hypervisor stop
// request to act on; it also keeps this package free of the engine protocol.
package vm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// AgentVsockPort is the vsock port care-engine-agent listens on.
const AgentVsockPort = 1024

// Defaults applied by Config.WithDefaults.
const (
	DefaultDataDiskSize       = 64 << 30
	DefaultMemoryMiB          = 4096
	DefaultCPUs               = 2
	DefaultConsoleLogMaxBytes = 8 << 20

	minMemoryMiB = 512
)

// Bundle files the VM boots from; see engine/README.md "Bundle layout".
const (
	KernelFile    = "vmlinuz"
	InitrdFile    = "initramfs"
	SystemImgFile = "system.img"
)

// Config describes one engine VM.
type Config struct {
	// BundleDir is an extracted engine bundle (vmlinuz, initramfs, system.img).
	BundleDir string
	// DataDisk is the persistent raw data disk (/dev/vdb). It is created
	// sparse with mode 0600 and DataDiskSize bytes when absent; the engine
	// formats a blank disk on first boot.
	DataDisk     string
	DataDiskSize int64
	MemoryMiB    uint64
	CPUs         uint
	// ConsoleLog receives the guest console (kernel, initramfs, OpenRC). It
	// is rotated to ConsoleLog+".1" when it would exceed ConsoleLogMaxBytes.
	ConsoleLog         string
	ConsoleLogMaxBytes int64
	// ConsoleMirror, if set, also receives the console output.
	ConsoleMirror io.Writer
	// ExtraCmdline is appended to the kernel command line, e.g. "care.debug".
	ExtraCmdline string
	// QEMUDir is the QEMU installation the Windows driver runs
	// (qemu-system-x86_64.exe, share/); it defaults to "qemu" next to the
	// executable. Unused on macOS.
	QEMUDir string
}

// WithDefaults returns c with zero fields set to their defaults.
func (c Config) WithDefaults() Config {
	if c.DataDiskSize == 0 {
		c.DataDiskSize = DefaultDataDiskSize
	}
	if c.MemoryMiB == 0 {
		c.MemoryMiB = DefaultMemoryMiB
	}
	if c.CPUs == 0 {
		c.CPUs = DefaultCPUs
	}
	if c.ConsoleLogMaxBytes == 0 {
		c.ConsoleLogMaxBytes = DefaultConsoleLogMaxBytes
	}
	return c
}

// Validate checks c after WithDefaults. It does not touch the filesystem;
// see ValidateBundle and EnsureDataDisk.
func (c Config) Validate() error {
	var errs []error
	if c.BundleDir == "" {
		errs = append(errs, errors.New("bundle directory is required"))
	}
	if c.DataDisk == "" {
		errs = append(errs, errors.New("data disk path is required"))
	}
	if c.ConsoleLog == "" {
		errs = append(errs, errors.New("console log path is required"))
	}
	if err := checkDiskSize(c.DataDiskSize); err != nil {
		errs = append(errs, err)
	}
	if c.MemoryMiB < minMemoryMiB {
		errs = append(errs, fmt.Errorf("memory %d MiB is below the %d MiB minimum", c.MemoryMiB, minMemoryMiB))
	}
	if c.CPUs < 1 {
		errs = append(errs, errors.New("at least one CPU is required"))
	}
	if c.ConsoleLogMaxBytes <= 0 {
		errs = append(errs, errors.New("console log size cap must be positive"))
	}
	if strings.ContainsAny(c.ExtraCmdline, "\x00\n\r") {
		errs = append(errs, errors.New("extra kernel command line must be a single line"))
	}
	for _, f := range strings.Fields(c.ExtraCmdline) {
		if k, _, _ := strings.Cut(f, "="); k == "console" || k == "care.system" || k == "care.data" {
			errs = append(errs, fmt.Errorf("extra kernel command line must not set %s", k))
		}
	}
	return errors.Join(errs...)
}

// Cmdline returns the kernel command line for a hypervisor whose console
// device is console (hvc0 for a virtio console).
func (c Config) Cmdline(console string) string {
	s := "console=" + console + " care.system=/dev/vda care.data=/dev/vdb"
	if extra := strings.Join(strings.Fields(c.ExtraCmdline), " "); extra != "" {
		s += " " + extra
	}
	return s
}

// ValidateBundle checks that dir holds the files needed to boot.
func ValidateBundle(dir string) error {
	st, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("engine bundle: %w", err)
	}
	if !st.IsDir() {
		return fmt.Errorf("engine bundle %s is not a directory", dir)
	}
	var errs []error
	for _, name := range []string{KernelFile, InitrdFile, SystemImgFile} {
		st, err := os.Stat(filepath.Join(dir, name))
		switch {
		case errors.Is(err, os.ErrNotExist):
			errs = append(errs, fmt.Errorf("engine bundle %s: missing %s", dir, name))
		case err != nil:
			errs = append(errs, fmt.Errorf("engine bundle: %w", err))
		case !st.Mode().IsRegular():
			errs = append(errs, fmt.Errorf("engine bundle %s: %s is not a regular file", dir, name))
		case st.Size() == 0:
			errs = append(errs, fmt.Errorf("engine bundle %s: %s is empty", dir, name))
		}
	}
	return errors.Join(errs...)
}

// EnsureDataDisk creates a sparse, all-zero disk image of size bytes with mode
// 0600 if path does not exist, and reports whether it did. An existing path
// must be a regular file; it is never resized or modified.
func EnsureDataDisk(path string, size int64) (created bool, err error) {
	if err := checkDiskSize(size); err != nil {
		return false, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		st, err := os.Stat(path)
		if err != nil {
			return false, fmt.Errorf("data disk: %w", err)
		}
		if !st.Mode().IsRegular() {
			return false, fmt.Errorf("data disk %s is not a regular file", path)
		}
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("create data disk: %w", err)
	}
	if err := setSparse(f); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return false, fmt.Errorf("create data disk: %w", err)
	}
	if err := f.Truncate(size); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return false, fmt.Errorf("create data disk: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return false, fmt.Errorf("create data disk: %w", err)
	}
	return true, nil
}

func checkDiskSize(size int64) error {
	if size <= 0 || size%512 != 0 {
		return fmt.Errorf("data disk size %d must be a positive multiple of 512", size)
	}
	return nil
}

// State is a Machine's lifecycle state.
type State int

const (
	StateNew      State = iota // created, not started
	StateStarting              // Start in progress
	StateRunning
	StateStopping // the hypervisor is stopping the VM
	StateStopped  // the guest powered off or the VM was force-stopped
	StateError    // the hypervisor stopped the VM because of an error
)

func (s State) String() string {
	switch s {
	case StateNew:
		return "new"
	case StateStarting:
		return "starting"
	case StateRunning:
		return "running"
	case StateStopping:
		return "stopping"
	case StateStopped:
		return "stopped"
	case StateError:
		return "error"
	}
	return fmt.Sprintf("State(%d)", int(s))
}

var (
	// ErrNotRunning is returned by DialVsock when the VM is not running.
	ErrNotRunning = errors.New("engine VM is not running")
	// ErrAlreadyStarted is returned by a second Start.
	ErrAlreadyStarted = errors.New("engine VM was already started")
	// ErrVMError is Machine.Err after the hypervisor stopped the VM with an error.
	ErrVMError = errors.New("engine VM stopped with a hypervisor error")
	// ErrDataDiskInUse is returned by Start when another VM (in any process)
	// has the data disk attached.
	ErrDataDiskInUse = errors.New("engine data disk is in use by another VM")
)

// Machine is one boot of the engine VM. Its methods are safe for concurrent use.
type Machine interface {
	// Start validates the bundle, creates the data disk if needed and boots
	// the VM. It returns once the hypervisor reports the VM running, which
	// is well before the agent answers.
	Start(ctx context.Context) error
	// DialVsock opens a stream to a guest vsock port.
	DialVsock(ctx context.Context, port uint32) (net.Conn, error)
	State() State
	// Done is closed once a started VM has stopped (guest power-off, force
	// stop or hypervisor error) or Start has failed, after the console log
	// has been flushed and closed. A Done the caller did not cause is a crash.
	Done() <-chan struct{}
	// Err is nil until Done is closed; then nil for a power-off or force
	// stop, ErrVMError for a hypervisor error, or Start's error.
	Err() error
	// Stop waits up to timeout for the VM to stop (after the caller asked the
	// agent to shut down) and force-stops it otherwise. It returns ctx.Err()
	// if ctx ends first, leaving the VM as it is.
	Stop(ctx context.Context, timeout time.Duration) error
	// ForceStop stops the VM immediately, like pulling the power cord, and
	// waits until it has stopped or ctx ends.
	ForceStop(ctx context.Context) error
}
