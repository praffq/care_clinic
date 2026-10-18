// Package supervisortest provides a fake engine VM whose "vsock" reaches an
// in-process agent server backed by agent.FakeExecutor.
package supervisortest

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/agent"
	"github.com/ohcnetwork/care_desktop/app/internal/engine/vm"
)

// Factory creates Machines; use its New as supervisor.Config.NewMachine.
type Factory struct {
	// Dir holds the fake agents' data; it must exist.
	Dir string
	// Setup, when set, configures each machine's executor before it starts.
	Setup func(*agent.FakeExecutor)
	// StartErr, when set, makes Start fail.
	StartErr error

	mu       sync.Mutex
	machines []*Machine
	overlap  bool
}

// New implements supervisor.Config.NewMachine.
func (f *Factory) New(cfg vm.Config) (vm.Machine, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.machines {
		select {
		case <-m.done:
		default:
			if m.State() != vm.StateNew {
				f.overlap = true
			}
		}
	}
	m := &Machine{Config: cfg, Exec: &agent.FakeExecutor{PoweredOff: make(chan struct{})}, done: make(chan struct{}), startErr: f.StartErr}
	if f.Setup != nil {
		f.Setup(m.Exec)
	}
	dir := filepath.Join(f.Dir, "agent")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	m.agentCfg = agent.Config{
		EngineVersion: filepath.Base(cfg.BundleDir),
		DataRoot:      dir,
		SchemaFile:    filepath.Join(dir, ".data-schema"),
		FilesRoot:     filepath.Join(dir, "files"),
		BackupsRoot:   filepath.Join(dir, "backups"),
		ShutdownDelay: 10 * time.Millisecond,
	}
	f.machines = append(f.machines, m)
	return m, nil
}

// Machines returns the machines created so far.
func (f *Factory) Machines() []*Machine {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*Machine(nil), f.machines...)
}

// Last returns the most recent machine, or nil.
func (f *Factory) Last() *Machine {
	ms := f.Machines()
	if len(ms) == 0 {
		return nil
	}
	return ms[len(ms)-1]
}

// Overlapped reports whether a machine was created while another was running.
func (f *Factory) Overlapped() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.overlap
}

// Machine is a fake vm.Machine.
type Machine struct {
	Config vm.Config
	Exec   *agent.FakeExecutor

	agentCfg agent.Config
	startErr error

	mu     sync.Mutex
	state  vm.State
	err    error
	ln     net.Listener
	srv    *http.Server
	done   chan struct{}
	forced bool
}

var _ vm.Machine = (*Machine)(nil)

func (m *Machine) Start(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state != vm.StateNew {
		return vm.ErrAlreadyStarted
	}
	if m.startErr != nil {
		m.state, m.err = vm.StateError, m.startErr
		close(m.done)
		return m.startErr
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	m.ln, m.state = ln, vm.StateRunning
	m.srv = &http.Server{Handler: agent.NewServer(m.agentCfg, m.Exec), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = m.srv.Serve(ln) }()
	go func() {
		select {
		case <-m.Exec.PoweredOff:
			m.halt(nil, false)
		case <-m.done:
		}
	}()
	return nil
}

// halt stops the VM: a guest power-off, a force stop, or a crash with err.
func (m *Machine) halt(err error, forced bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state != vm.StateRunning {
		return
	}
	m.state, m.err, m.forced = vm.StateStopped, err, forced
	if err != nil {
		m.state = vm.StateError
	}
	_ = m.srv.Close()
	close(m.done)
}

// Crash stops the VM as if the hypervisor failed (err) or the guest powered
// off on its own (nil).
func (m *Machine) Crash(err error) { m.halt(err, false) }

// Hang makes the agent unreachable while the VM keeps running, like a
// wedged guest.
func (m *Machine) Hang() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state == vm.StateRunning {
		_ = m.srv.Close()
	}
}

// Forced reports whether the VM was force-stopped.
func (m *Machine) Forced() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.forced
}

func (m *Machine) DialVsock(ctx context.Context, port uint32) (net.Conn, error) {
	m.mu.Lock()
	ln, st := m.ln, m.state
	m.mu.Unlock()
	if st != vm.StateRunning {
		return nil, vm.ErrNotRunning
	}
	if port != vm.AgentVsockPort {
		return nil, errors.New("connection refused")
	}
	var d net.Dialer
	return d.DialContext(ctx, "tcp", ln.Addr().String())
}

func (m *Machine) State() vm.State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

func (m *Machine) Done() <-chan struct{} { return m.done }

func (m *Machine) Err() error {
	select {
	case <-m.done:
	default:
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.err
}

func (m *Machine) Stop(ctx context.Context, timeout time.Duration) error {
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

func (m *Machine) ForceStop(context.Context) error {
	m.halt(nil, true)
	<-m.done
	return nil
}
