//go:build darwin && cgo

package vm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"time"
	"unsafe"

	"github.com/Code-Hex/vz/v3"
)

// Threading: Virtualization.framework requires every call on a
// VZVirtualMachine (and its devices) to happen on the dispatch queue the VM
// was created with. vz/v3 creates a private serial queue per VM and routes
// all calls, completion handlers and state observation through it, so this
// driver may be used from any goroutine and needs no main-thread run loop
// (only vz's StartGraphicApplication, unused here, does). The binary must be
// signed with the com.apple.security.virtualization entitlement.
//
// vz exposes state changes as a single unbounded channel, so exactly one
// goroutine (watch) consumes it and fans out through State, Done and Err.

const virtioConsole = "hvc0"

type vzMachine struct {
	cfg Config

	mu      sync.Mutex
	started bool
	state   State
	vm      *vz.VirtualMachine
	sock    *vz.VirtioSocketDevice
	err     error

	finishOnce sync.Once
	done       chan struct{}
	cleanup    []func()
}

// New returns a Machine for cfg (after Config.WithDefaults). It does not
// touch the filesystem until Start.
func New(cfg Config) (Machine, error) {
	cfg = cfg.WithDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("engine VM config: %w", err)
	}
	return &vzMachine{cfg: cfg, done: make(chan struct{})}, nil
}

func (m *vzMachine) Start(ctx context.Context) (err error) {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return ErrAlreadyStarted
	}
	m.started, m.state = true, StateStarting
	m.mu.Unlock()
	defer func() {
		if err != nil {
			m.finish(StateError, err)
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := ValidateBundle(m.cfg.BundleDir); err != nil {
		return err
	}
	if _, err := EnsureDataDisk(m.cfg.DataDisk, m.cfg.DataDiskSize); err != nil {
		return err
	}
	if err := checkDataDiskFree(m.cfg.DataDisk); err != nil {
		return err
	}

	console, err := m.startConsole()
	if err != nil {
		return err
	}
	vcfg, err := m.vzConfig(console)
	if err != nil {
		return err
	}
	vm, err := vz.NewVirtualMachine(vcfg)
	if err != nil {
		return fmt.Errorf("create VM: %w", err)
	}
	socks := vm.SocketDevices()
	if len(socks) == 0 {
		return errors.New("VM has no virtio socket device")
	}
	m.mu.Lock()
	m.vm, m.sock = vm, socks[0]
	m.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}
	if err := vm.Start(); err != nil {
		return fmt.Errorf("start VM: %w", err)
	}
	m.setState(StateRunning)
	go m.watch(vm)
	return nil
}

// startConsole opens the console log and returns the write end of a pipe
// whose output is copied to it (and to ConsoleMirror).
func (m *vzMachine) startConsole() (*os.File, error) {
	log, err := m.cfg.openConsole()
	if err != nil {
		return nil, err
	}
	_, _ = fmt.Fprintf(log, "\n=== engine VM boot %s (%s) ===\n", time.Now().Format(time.RFC3339), m.cfg.BundleDir)
	r, w, err := os.Pipe()
	if err != nil {
		_ = log.Close()
		return nil, err
	}
	copied := make(chan struct{})
	go func() {
		defer close(copied)
		copyConsole(r, log, m.cfg.ConsoleMirror)
	}()
	m.onFinish(func() {
		_ = w.Close()
		// The hypervisor should have released w; don't hang if it has not.
		select {
		case <-copied:
		case <-time.After(time.Second):
		}
		_ = r.Close()
		<-copied
		_ = log.Close()
	})
	return w, nil
}

func (m *vzMachine) vzConfig(console *os.File) (*vz.VirtualMachineConfiguration, error) {
	c := m.cfg
	boot, err := vz.NewLinuxBootLoader(filepath.Join(c.BundleDir, KernelFile),
		vz.WithInitrd(filepath.Join(c.BundleDir, InitrdFile)),
		vz.WithCommandLine(c.Cmdline(virtioConsole)))
	if err != nil {
		return nil, fmt.Errorf("boot loader: %w", err)
	}
	cfg, err := vz.NewVirtualMachineConfiguration(boot, c.CPUs, c.MemoryMiB<<20)
	if err != nil {
		return nil, err
	}

	// The guest console is output-only; its input is an idle pipe.
	in, inW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	m.onFinish(func() { _ = in.Close(); _ = inW.Close() })
	serialAtt, err := vz.NewFileHandleSerialPortAttachment(in, console)
	if err != nil {
		return nil, err
	}
	serial, err := vz.NewVirtioConsoleDeviceSerialPortConfiguration(serialAtt)
	if err != nil {
		return nil, err
	}
	cfg.SetSerialPortsVirtualMachineConfiguration([]*vz.VirtioConsoleDeviceSerialPortConfiguration{serial})

	sysAtt, err := vz.NewDiskImageStorageDeviceAttachment(filepath.Join(c.BundleDir, SystemImgFile), true)
	if err != nil {
		return nil, fmt.Errorf("system.img: %w", err)
	}
	sysDev, err := vz.NewVirtioBlockDeviceConfiguration(sysAtt)
	if err != nil {
		return nil, err
	}
	dataAtt, err := vz.NewDiskImageStorageDeviceAttachmentWithCacheAndSync(c.DataDisk, false,
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

// watch consumes vz's state notifications until the VM stops. Notifications
// buffered before Start returned are replayed; Starting is ignored because
// Start already reported it and the VM is running by now.
func (m *vzMachine) watch(vm *vz.VirtualMachine) {
	for st := range vm.StateChangedNotify() {
		switch st {
		case vz.VirtualMachineStateRunning:
			m.setState(StateRunning)
		case vz.VirtualMachineStateStopping:
			m.setState(StateStopping)
		case vz.VirtualMachineStateStopped:
			m.finish(StateStopped, nil)
			return
		case vz.VirtualMachineStateError:
			m.finish(StateError, ErrVMError)
			return
		}
	}
}

func (m *vzMachine) setState(s State) {
	m.mu.Lock()
	defer m.mu.Unlock()
	select {
	case <-m.done:
	default:
		m.state = s
	}
}

func (m *vzMachine) onFinish(f func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanup = append(m.cleanup, f)
}

// finish records the final state, releases the console and pipes (in
// reverse order of acquisition) and then closes done.
func (m *vzMachine) finish(s State, err error) {
	m.finishOnce.Do(func() {
		m.mu.Lock()
		m.state, m.err = s, err
		cleanup := m.cleanup
		m.cleanup = nil
		m.mu.Unlock()
		for i := len(cleanup) - 1; i >= 0; i-- {
			cleanup[i]()
		}
		close(m.done)
	})
}

func (m *vzMachine) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

func (m *vzMachine) Done() <-chan struct{} { return m.done }

func (m *vzMachine) Err() error {
	select {
	case <-m.done:
	default:
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.err
}

func (m *vzMachine) DialVsock(ctx context.Context, port uint32) (net.Conn, error) {
	m.mu.Lock()
	sock, state := m.sock, m.state
	m.mu.Unlock()
	if state != StateRunning || sock == nil {
		return nil, ErrNotRunning
	}
	type result struct {
		c   net.Conn
		err error
	}
	ch := make(chan result, 1)
	go func() {
		c, err := sock.Connect(port)
		if err != nil {
			ch <- result{nil, fmt.Errorf("vsock port %d: %w", port, err)}
			return
		}
		ch <- result{withCloseWrite(c), nil}
	}()
	abandon := func() {
		go func() {
			if r := <-ch; r.c != nil {
				_ = r.c.Close()
			}
		}()
	}
	select {
	case r := <-ch:
		return r.c, r.err
	case <-ctx.Done():
		abandon()
		return nil, ctx.Err()
	case <-m.done:
		abandon()
		return nil, ErrNotRunning
	}
}

// vsockConn adds CloseWrite to a vz connection so forwarders can pass a
// client's half-close on to the guest: Virtualization.framework turns
// shutdown(SHUT_WR) on its socket into a vsock shutdown, but vz keeps that
// socket (a *net.UnixConn) in an unexported field.
type vsockConn struct {
	*vz.VirtioSocketConnection
	cw interface{ CloseWrite() error }
}

func (c *vsockConn) CloseWrite() error { return c.cw.CloseWrite() }

// withCloseWrite wraps c when its underlying socket supports CloseWrite, and
// returns c unchanged otherwise (a vz upgrade renaming the field only loses
// half-close; vz_darwin_test checks it is still found).
func withCloseWrite(c *vz.VirtioSocketConnection) net.Conn {
	f := reflect.ValueOf(c).Elem().FieldByName("rawConn")
	if !f.IsValid() || f.Kind() != reflect.Interface {
		return c
	}
	raw := reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem().Interface()
	if cw, ok := raw.(interface{ CloseWrite() error }); ok {
		return &vsockConn{c, cw}
	}
	return c
}

func (m *vzMachine) Stop(ctx context.Context, timeout time.Duration) error {
	if m.State() == StateNew {
		return nil
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-m.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
	}
	return m.ForceStop(ctx)
}

func (m *vzMachine) ForceStop(ctx context.Context) error {
	m.mu.Lock()
	vm := m.vm
	m.mu.Unlock()
	select {
	case <-m.done:
		return nil
	default:
	}
	if vm == nil {
		return ErrNotRunning
	}
	if err := vm.Stop(); err != nil {
		select {
		case <-m.done:
			return nil
		default:
			return fmt.Errorf("force-stop VM: %w", err)
		}
	}
	select {
	case <-m.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
