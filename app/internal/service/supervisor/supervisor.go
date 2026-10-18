// Package supervisor owns the engine VM lifecycle in the host service: it
// boots the current slot, waits for the agent, restarts the VM with
// exponential backoff after any stop it did not cause, and carries out the
// start/stop/restart/slot-switch commands it is given. At most one Machine
// exists at a time.
package supervisor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/client"
	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
	"github.com/ohcnetwork/care_desktop/app/internal/engine/vm"
	"github.com/ohcnetwork/care_desktop/app/internal/service/api"
	"github.com/ohcnetwork/care_desktop/app/internal/service/slots"
)

// State and Status are the control API's engine state and snapshot.
type (
	State  = api.EngineState
	Status = api.EngineStatus
)

const (
	StateNoEngine = api.StateNoEngine
	StateStopped  = api.StateStopped
	StateStarting = api.StateStarting
	StateReady    = api.StateReady
	StateNotReady = api.StateNotReady
	StateStopping = api.StateStopping
	StateCrashed  = api.StateCrashed
	StateBackoff  = api.StateBackoff
)

var (
	// ErrNoEngine is returned by Start when no slot is active.
	ErrNoEngine = api.ErrNoEngine
	// ErrUnavailable is returned by DialAgent unless the agent is answering.
	ErrUnavailable = api.ErrEngineUnavailable
	// ErrClosed is returned once Run has returned.
	ErrClosed = errors.New("supervisor is not running")
)

// Slots is the part of *slots.Store the supervisor uses.
type Slots interface {
	Current() (slots.Slot, error)
	Verify(version string) (slots.Slot, error)
	Activate(version string) error
}

// Config configures a Supervisor; zero durations take the defaults.
type Config struct {
	Slots Slots
	// VM is the template for every boot; BundleDir is set to the slot.
	VM         vm.Config
	NewMachine func(vm.Config) (vm.Machine, error) // vm.New when nil
	Log        *slog.Logger

	ReadyTimeout   time.Duration // boot to docker_ready before reporting not_ready (5m)
	PollInterval   time.Duration // agent status poll while starting (1s)
	HealthInterval time.Duration // agent status poll once ready (15s)
	StopTimeout    time.Duration // guest power-off after the shutdown request (30s)
	BackoffMin     time.Duration // first restart delay (1s), doubled per failure
	BackoffMax     time.Duration // restart delay cap (2m)
	StableAfter    time.Duration // uptime that resets the backoff (10m)
}

func (c *Config) setDefaults() {
	def := func(p *time.Duration, v time.Duration) {
		if *p <= 0 {
			*p = v
		}
	}
	def(&c.ReadyTimeout, 5*time.Minute)
	def(&c.PollInterval, time.Second)
	def(&c.HealthInterval, 15*time.Second)
	def(&c.StopTimeout, 30*time.Second)
	def(&c.BackoffMin, time.Second)
	def(&c.BackoffMax, 2*time.Minute)
	def(&c.StableAfter, 10*time.Minute)
	if c.NewMachine == nil {
		c.NewMachine = vm.New
	}
	if c.Log == nil {
		c.Log = slog.New(slog.DiscardHandler)
	}
}

type cmdKind int

const (
	cmdStart cmdKind = iota
	cmdStop
	cmdRestart
	cmdSwitch
)

type command struct {
	kind    cmdKind
	version string
	reply   chan error
}

// boot is one Machine; ctx scopes its monitor goroutine.
type boot struct {
	m         vm.Machine
	version   string
	startedAt time.Time
	ctx       context.Context
	cancel    context.CancelFunc
}

// Supervisor runs the engine. Create it with New, then call Run.
type Supervisor struct {
	cfg  Config
	cmds chan command
	done chan struct{}

	// Owned by the Run goroutine.
	cur      *boot
	want     bool
	failures int
	retry    *time.Timer

	mu   sync.Mutex
	st   Status
	dial vm.Machine // machine the agent is reachable on, or nil
	subs map[chan Status]struct{}
}

// New returns a stopped Supervisor.
func New(cfg Config) *Supervisor {
	cfg.setDefaults()
	return &Supervisor{
		cfg:  cfg,
		cmds: make(chan command),
		done: make(chan struct{}),
		st:   Status{State: StateStopped, ConsoleLog: cfg.VM.ConsoleLog},
		subs: map[chan Status]struct{}{},
	}
}

// Run supervises until ctx ends, then stops the VM gracefully.
func (s *Supervisor) Run(ctx context.Context) {
	defer close(s.done)
	if _, err := s.cfg.Slots.Current(); errors.Is(err, slots.ErrNoCurrent) {
		s.update(func(st *Status) { st.State = StateNoEngine })
	}
	for {
		var vmDone <-chan struct{}
		if s.cur != nil {
			vmDone = s.cur.m.Done()
		}
		var retry <-chan time.Time
		if s.retry != nil {
			retry = s.retry.C
		}
		select {
		case <-ctx.Done():
			s.cancelRetry()
			sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.cfg.StopTimeout+15*time.Second)
			s.shutdown(sctx)
			cancel()
			return
		case c := <-s.cmds:
			c.reply <- s.handle(ctx, c)
		case <-vmDone:
			s.crashed()
		case <-retry:
			s.retry = nil
			s.update(func(st *Status) { st.Restarts++ })
			_ = s.boot(ctx)
		}
	}
}

func (s *Supervisor) handle(ctx context.Context, c command) error {
	switch c.kind {
	case cmdStart:
		s.want = true
		if s.cur != nil {
			return nil
		}
		s.cancelRetry()
		return s.boot(ctx)
	case cmdStop:
		s.want = false
		s.cancelRetry()
		s.shutdown(ctx)
		return nil
	case cmdRestart:
		s.want = true
		s.cancelRetry()
		s.shutdown(ctx)
		s.failures = 0
		return s.boot(ctx)
	case cmdSwitch:
		if _, err := s.cfg.Slots.Verify(c.version); err != nil {
			return err
		}
		s.cancelRetry()
		s.shutdown(ctx)
		if err := s.cfg.Slots.Activate(c.version); err != nil {
			return err
		}
		s.cfg.Log.Info("activated engine slot", "version", c.version)
		s.want = true
		s.failures = 0
		return s.boot(ctx)
	}
	return fmt.Errorf("unknown command %d", c.kind)
}

// boot starts the current slot; failures schedule a retry.
func (s *Supervisor) boot(ctx context.Context) error {
	sl, err := s.cfg.Slots.Current()
	if errors.Is(err, slots.ErrNoCurrent) {
		s.update(func(st *Status) { st.State = StateNoEngine; st.EngineVersion = "" })
		return ErrNoEngine
	}
	if err != nil {
		return s.failed(fmt.Errorf("engine slot: %w", err))
	}
	cfg := s.cfg.VM
	cfg.BundleDir = sl.Dir
	s.update(func(st *Status) {
		st.State, st.EngineVersion, st.Agent = StateStarting, sl.Version, nil
		st.StartedAt, st.NextRetryAt = time.Time{}, time.Time{}
	})
	m, err := s.cfg.NewMachine(cfg)
	if err != nil {
		return s.failed(fmt.Errorf("create engine VM: %w", err))
	}
	if err := m.Start(ctx); err != nil {
		return s.failed(fmt.Errorf("start engine VM: %w", err))
	}
	bctx, cancel := context.WithCancel(ctx)
	b := &boot{m: m, version: sl.Version, startedAt: time.Now(), ctx: bctx, cancel: cancel}
	s.cur = b
	s.cfg.Log.Info("engine VM started", "version", sl.Version)
	s.update(func(st *Status) { st.StartedAt = b.startedAt })
	go s.monitor(b)
	return nil
}

// failed records err and, when the engine is wanted, schedules a retry.
func (s *Supervisor) failed(err error) error {
	s.cfg.Log.Error("engine failed", "err", err)
	s.update(func(st *Status) {
		st.State = StateCrashed
		st.LastError, st.LastErrorAt = err.Error(), time.Now()
		st.Agent, st.StartedAt = nil, time.Time{}
	})
	if !s.want {
		return err
	}
	s.failures++
	d := s.backoff(s.failures)
	s.retry = time.NewTimer(d)
	s.cfg.Log.Info("restarting engine after backoff", "delay", d, "failures", s.failures)
	s.update(func(st *Status) { st.State, st.NextRetryAt = StateBackoff, time.Now().Add(d) })
	return err
}

// backoff returns the delay before retry n (1-based).
func (s *Supervisor) backoff(n int) time.Duration {
	d := s.cfg.BackoffMin << min(n-1, 30)
	if d > s.cfg.BackoffMax || d <= 0 {
		d = s.cfg.BackoffMax
	}
	return d
}

// crashed handles a VM that stopped without being asked to.
func (s *Supervisor) crashed() {
	b := s.cur
	s.detach()
	if time.Since(b.startedAt) >= s.cfg.StableAfter {
		s.failures = 0
	}
	err := errors.New("engine VM stopped unexpectedly")
	if e := b.m.Err(); e != nil {
		err = fmt.Errorf("%w: %w", err, e)
	}
	if s.cfg.VM.ConsoleLog != "" {
		err = fmt.Errorf("%w (see %s)", err, s.cfg.VM.ConsoleLog)
	}
	_ = s.failed(err)
}

// detach forgets the current boot and stops its monitor.
func (s *Supervisor) detach() {
	b := s.cur
	s.cur = nil
	s.mu.Lock()
	b.cancel()
	s.dial = nil
	s.mu.Unlock()
}

// shutdown stops the current VM: the agent powers the guest off, and the VM
// is force-stopped when that fails or takes longer than StopTimeout.
func (s *Supervisor) shutdown(ctx context.Context) {
	b := s.cur
	if b == nil {
		s.update(func(st *Status) {
			if st.State != StateNoEngine {
				st.State = StateStopped
			}
			st.NextRetryAt = time.Time{}
		})
		return
	}
	s.detach()
	s.update(func(st *Status) { st.State = StateStopping })
	start := time.Now()
	c := agentClient(b.m)
	sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err := c.Shutdown(sctx)
	cancel()
	if err != nil {
		s.cfg.Log.Warn("agent shutdown failed; forcing the VM off", "err", err)
		err = b.m.ForceStop(ctx)
	} else {
		err = b.m.Stop(ctx, s.cfg.StopTimeout)
	}
	if err != nil {
		s.cfg.Log.Error("stopping engine VM", "err", err)
	}
	<-b.m.Done()
	s.cfg.Log.Info("engine VM stopped", "took", time.Since(start).Round(time.Millisecond))
	s.update(func(st *Status) {
		st.State, st.Agent, st.StartedAt, st.NextRetryAt = StateStopped, nil, time.Time{}, time.Time{}
	})
}

func (s *Supervisor) cancelRetry() {
	if s.retry != nil {
		s.retry.Stop()
		s.retry = nil
	}
}

// monitor waits for the agent, syncs the clock and then polls its health
// until b is stopped. It only reports state; restarts are decided by Run.
func (s *Supervisor) monitor(b *boot) {
	c := agentClient(b.m)
	defer c.Close()
	ctx, cancel := context.WithTimeout(b.ctx, s.cfg.ReadyTimeout)
	st, err := c.WaitReady(ctx, s.cfg.PollInterval)
	cancel()
	if b.ctx.Err() != nil {
		return
	}
	if err == nil {
		if err := c.SetTime(b.ctx, time.Now()); err != nil {
			s.cfg.Log.Warn("set engine clock", "err", err)
		}
		s.cfg.Log.Info("engine ready", "version", b.version, "after", time.Since(b.startedAt).Round(time.Millisecond))
	}
	s.health(b, st, err)
	t := time.NewTicker(s.cfg.HealthInterval)
	defer t.Stop()
	for {
		select {
		case <-b.ctx.Done():
			return
		case <-t.C:
		}
		rctx, cancel := context.WithTimeout(b.ctx, 10*time.Second)
		st, err := c.Status(rctx)
		cancel()
		if err == nil && !st.DockerReady {
			err = errors.New("docker is not ready")
		}
		s.health(b, st, err)
	}
}

// health records the outcome of waiting for (or polling) b's agent unless b
// has been stopped meanwhile.
func (s *Supervisor) health(b *boot, st protocol.Status, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b.ctx.Err() != nil {
		return
	}
	prev := s.st.State
	s.st.State = StateReady
	if err != nil {
		s.st.State = StateNotReady
	}
	if st.EngineVersion != "" { // the agent answered
		s.st.Agent = &st
		s.dial = b.m
	}
	if err != nil && s.st.State != prev {
		s.st.LastError, s.st.LastErrorAt = "engine not ready: "+err.Error(), time.Now()
		s.cfg.Log.Warn("engine not ready", "err", err)
	}
	s.publishLocked()
}

func agentClient(m vm.Machine) *client.Client {
	return client.New(func(ctx context.Context) (net.Conn, error) {
		return m.DialVsock(ctx, vm.AgentVsockPort)
	})
}

func (s *Supervisor) update(f func(*Status)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(&s.st)
	s.publishLocked()
}

func (s *Supervisor) publishLocked() {
	snap := s.snapshotLocked()
	for ch := range s.subs {
		select {
		case <-ch:
		default:
		}
		ch <- snap
	}
}

func (s *Supervisor) snapshotLocked() Status {
	st := s.st
	if st.Agent != nil {
		a := *st.Agent
		st.Agent = &a
	}
	if !st.StartedAt.IsZero() {
		st.UptimeSeconds = int64(time.Since(st.StartedAt) / time.Second)
	}
	return st
}

// Status returns the current snapshot.
func (s *Supervisor) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked()
}

// Subscribe returns a channel that receives the current snapshot and then
// the latest one after every change (intermediate snapshots may be skipped),
// and a function that ends the subscription.
func (s *Supervisor) Subscribe() (<-chan Status, func()) {
	ch := make(chan Status, 1)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	ch <- s.snapshotLocked()
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}
}

// DialAgent connects to the agent of the running VM. It fails with
// ErrUnavailable unless the agent has answered (ready or not_ready).
func (s *Supervisor) DialAgent(ctx context.Context) (net.Conn, error) {
	s.mu.Lock()
	m := s.dial
	s.mu.Unlock()
	if m == nil {
		return nil, ErrUnavailable
	}
	return m.DialVsock(ctx, vm.AgentVsockPort)
}

// Start boots the engine (if it is not running) and keeps it running. It
// returns once the VM has started, before the agent is ready.
func (s *Supervisor) Start(ctx context.Context) error { return s.send(ctx, command{kind: cmdStart}) }

// Stop shuts the engine down gracefully and keeps it stopped.
func (s *Supervisor) Stop(ctx context.Context) error { return s.send(ctx, command{kind: cmdStop}) }

// Restart stops the engine (if running) and boots it again.
func (s *Supervisor) Restart(ctx context.Context) error {
	return s.send(ctx, command{kind: cmdRestart})
}

// Switch verifies the slot for version, stops the engine, makes the slot
// current and boots it.
func (s *Supervisor) Switch(ctx context.Context, version string) error {
	return s.send(ctx, command{kind: cmdSwitch, version: version})
}

func (s *Supervisor) send(ctx context.Context, c command) error {
	c.reply = make(chan error, 1)
	select {
	case s.cmds <- c:
	case <-s.done:
		return ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-c.reply:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
