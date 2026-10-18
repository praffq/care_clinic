// Package supervisor owns the engine VM lifecycle in the host service: it
// boots the current slot, waits for the agent, restarts the VM with
// exponential backoff after any stop it did not cause, and carries out the
// start/stop/restart/slot-switch commands it is given. At most one Machine
// exists at a time.
//
// A watchdog treats a hung guest like a crash: when the agent is not ready
// within ReadyTimeout of boot, or stops answering for UnreachableProbes
// health polls in a row, the VM is forced off and restarted with backoff.
// An agent that answers while docker is down only reports not_ready.
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

	ReadyTimeout   time.Duration // boot to docker_ready before the watchdog restarts the VM (5m)
	PollInterval   time.Duration // agent status poll while starting (1s)
	HealthInterval time.Duration // agent status poll once ready (15s)
	StopTimeout    time.Duration // guest power-off after the shutdown request (30s)
	// StackStopTimeout bounds stopping the clinic stack before a shutdown (60s).
	StackStopTimeout time.Duration
	// ForceStopTimeout bounds a force stop. A VM still running after it is
	// kept as lingering: no new VM boots on its disk until it stops (30s).
	ForceStopTimeout time.Duration
	BackoffMin       time.Duration // first restart delay (1s), doubled per failure
	BackoffMax       time.Duration // restart delay cap (2m)
	StableAfter      time.Duration // uptime that resets the backoff (10m)
	// UnreachableProbes is how many health polls in a row may fail to reach
	// the agent before the watchdog restarts the VM (4, i.e. 1m at 15s).
	UnreachableProbes int
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
	def(&c.StackStopTimeout, time.Minute)
	def(&c.ForceStopTimeout, 30*time.Second)
	def(&c.BackoffMin, time.Second)
	def(&c.BackoffMax, 2*time.Minute)
	def(&c.StableAfter, 10*time.Minute)
	if c.UnreachableProbes <= 0 {
		c.UnreachableProbes = 4
	}
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

// hang is the watchdog's report that b's agent is unresponsive.
type hang struct {
	b   *boot
	err error
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
	hung chan hang
	done chan struct{}

	// Owned by the Run goroutine.
	cur      *boot
	want     bool
	failures int
	retry    *time.Timer
	// lingering is a VM that did not stop when told to; it may still be
	// using the data disk.
	lingering vm.Machine

	mu      sync.Mutex
	st      Status
	running vm.Machine // the started machine, or nil
	dial    vm.Machine // machine the agent is reachable on, or nil
	subs    map[chan Status]struct{}
}

// New returns a stopped Supervisor.
func New(cfg Config) *Supervisor {
	cfg.setDefaults()
	return &Supervisor{
		cfg:  cfg,
		cmds: make(chan command),
		hung: make(chan hang),
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
			sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.cfg.StackStopTimeout+s.cfg.StopTimeout+15*time.Second)
			s.shutdown(sctx)
			cancel()
			return
		case c := <-s.cmds:
			c.reply <- s.handle(ctx, c)
		case <-vmDone:
			s.crashed()
		case h := <-s.hung:
			if h.b == s.cur {
				s.hungUp(ctx, h.err)
			}
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
	if m := s.lingering; m != nil {
		select {
		case <-m.Done():
			s.lingering = nil
		default:
			return s.failed(errors.New("the previous engine VM has not stopped; not starting another on its disk"))
		}
	}
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
		st.State, st.EngineVersion, st.Agent, st.Stack = StateStarting, sl.Version, nil, nil
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
	s.mu.Lock()
	s.running = m
	s.mu.Unlock()
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
	s.resetIfStable(b)
	err := errors.New("engine VM stopped unexpectedly")
	if e := b.m.Err(); e != nil {
		err = fmt.Errorf("%w: %w", err, e)
	}
	if s.cfg.VM.ConsoleLog != "" {
		err = fmt.Errorf("%w (see %s)", err, s.cfg.VM.ConsoleLog)
	}
	_ = s.failed(err)
}

// hungUp forces off the current VM, whose agent the watchdog found
// unresponsive, and handles it as a crash.
func (s *Supervisor) hungUp(ctx context.Context, cause error) {
	b := s.cur
	s.detach()
	s.resetIfStable(b)
	s.cfg.Log.Error("engine watchdog: forcing the VM off", "err", cause)
	s.update(func(st *Status) { st.State = StateStopping })
	fctx, cancel := context.WithTimeout(ctx, s.cfg.ForceStopTimeout)
	defer cancel()
	if err := b.m.ForceStop(fctx); err != nil {
		s.cfg.Log.Error("force-stopping engine VM", "err", err)
	}
	if err := s.awaitStop(fctx, b.m); err != nil {
		cause = fmt.Errorf("%w; %w", cause, err)
	}
	_ = s.failed(fmt.Errorf("engine watchdog restarted the VM: %w", cause))
}

// awaitStop waits until m stops or ctx ends. A VM that has not stopped by
// then is kept as lingering so the Run loop stays responsive without
// booting a second VM on the same disk.
func (s *Supervisor) awaitStop(ctx context.Context, m vm.Machine) error {
	select {
	case <-m.Done():
		return nil
	case <-ctx.Done():
	}
	s.lingering = m
	err := errors.New("engine VM did not stop")
	s.cfg.Log.Error("engine VM is not stopping; no new VM will boot until it does", "err", err)
	return err
}

func (s *Supervisor) resetIfStable(b *boot) {
	if time.Since(b.startedAt) >= s.cfg.StableAfter {
		s.failures = 0
	}
}

// detach forgets the current boot and stops its monitor.
func (s *Supervisor) detach() {
	b := s.cur
	s.cur = nil
	s.mu.Lock()
	b.cancel()
	s.dial, s.running = nil, nil
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
	c := agentClient(b.m)
	defer c.Close()
	if s.Status().State == StateReady {
		s.stopStack(ctx, c)
	}
	s.detach()
	s.update(func(st *Status) { st.State = StateStopping })
	start := time.Now()
	sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err := whenIdle(sctx, func() error { return c.Shutdown(sctx) })
	cancel()
	// Bounds the power-off wait and the force stop after it.
	sctx, cancel = context.WithTimeout(ctx, s.cfg.StopTimeout+s.cfg.ForceStopTimeout)
	defer cancel()
	if err != nil {
		s.cfg.Log.Warn("agent shutdown failed; forcing the VM off", "err", err)
		err = b.m.ForceStop(sctx)
	} else {
		err = b.m.Stop(sctx, s.cfg.StopTimeout)
	}
	if err != nil {
		s.cfg.Log.Error("stopping engine VM", "err", err)
	}
	if err := s.awaitStop(sctx, b.m); err != nil {
		s.update(func(st *Status) {
			st.State, st.Agent, st.StartedAt, st.NextRetryAt = StateCrashed, nil, time.Time{}, time.Time{}
			st.LastError, st.LastErrorAt = err.Error(), time.Now()
		})
		return
	}
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
// until b is stopped. It reports state and, as the watchdog, hangs; Run
// decides what to do about them.
func (s *Supervisor) monitor(b *boot) {
	c := agentClient(b.m)
	defer c.Close()
	ctx, cancel := context.WithTimeout(b.ctx, s.cfg.ReadyTimeout)
	st, err := c.WaitReady(ctx, s.cfg.PollInterval)
	cancel()
	if b.ctx.Err() != nil {
		return
	}
	if err != nil {
		s.health(b, st, err)
		s.reportHang(b, fmt.Errorf("not ready %s after boot: %w", s.cfg.ReadyTimeout, err))
		return
	}
	if err := c.SetTime(b.ctx, time.Now()); err != nil {
		s.cfg.Log.Warn("set engine clock", "err", err)
	}
	s.cfg.Log.Info("engine ready", "version", b.version, "after", time.Since(b.startedAt).Round(time.Millisecond))
	s.health(b, st, nil)
	go s.startStack(b)
	t := time.NewTicker(s.cfg.HealthInterval)
	defer t.Stop()
	misses := 0
	for {
		select {
		case <-b.ctx.Done():
			return
		case <-t.C:
		}
		rctx, cancel := context.WithTimeout(b.ctx, 10*time.Second)
		st, err := c.Status(rctx)
		cancel()
		if b.ctx.Err() != nil {
			return
		}
		if err != nil {
			misses++
		} else {
			misses = 0
		}
		if err == nil && !st.DockerReady {
			err = errors.New("docker is not ready")
		}
		s.health(b, st, err)
		if misses >= s.cfg.UnreachableProbes {
			s.reportHang(b, fmt.Errorf("agent unreachable for %d health checks: %w", misses, err))
			return
		}
	}
}

// reportHang hands a hang of b to Run unless b is stopped first.
func (s *Supervisor) reportHang(b *boot, err error) {
	select {
	case s.hung <- hang{b: b, err: err}:
	case <-b.ctx.Done():
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

// startStack brings up the installed clinic after b booted, so CARE comes
// back after a reboot without the app. It never migrates or updates.
func (s *Supervisor) startStack(b *boot) {
	s.setStack(b, api.StackStarting, nil)
	c := agentClient(b.m)
	defer c.Close()
	err := opWhenIdle(b.ctx, c, protocol.StackUp{Wait: true, TimeoutSec: 600})
	var oe *protocol.OpError
	switch {
	case b.ctx.Err() != nil:
	case err == nil:
		s.cfg.Log.Info("clinic stack started")
		s.setStack(b, api.StackRunning, nil)
	case errors.As(err, &oe) && oe.Code == protocol.CodeNotInstalled:
		s.setStack(b, api.StackNotInstalled, nil)
	default:
		s.cfg.Log.Error("clinic stack start failed", "err", err)
		s.setStack(b, api.StackFailed, err)
	}
}

// stopStack stops the clinic's containers so the guest shutdown does not
// cut off the database mid-write.
func (s *Supervisor) stopStack(ctx context.Context, c *client.Client) {
	s.setStack(nil, api.StackStopping, nil)
	ctx, cancel := context.WithTimeout(ctx, s.cfg.StackStopTimeout)
	defer cancel()
	start := time.Now()
	if err := opWhenIdle(ctx, c, protocol.StackStop{}); err != nil {
		s.cfg.Log.Warn("stopping the clinic stack", "err", err)
		s.setStack(nil, api.StackFailed, err)
		return
	}
	s.cfg.Log.Info("clinic stack stopped", "took", time.Since(start).Round(time.Millisecond))
	s.setStack(nil, api.StackStopped, nil)
}

// opWhenIdle runs op, waiting while the agent is busy with another one.
func opWhenIdle(ctx context.Context, c *client.Client, op protocol.Op) error {
	return whenIdle(ctx, func() error { return c.Op(ctx, op, nil, nil) })
}

// whenIdle calls f until the agent is not busy with another op.
func whenIdle(ctx context.Context, f func() error) error {
	for {
		err := f()
		if !errors.Is(err, client.ErrBusy) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// setStack records the stack state unless b (when given) was stopped.
func (s *Supervisor) setStack(b *boot, state api.StackState, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b != nil && b.ctx.Err() != nil {
		return
	}
	st := &api.StackStatus{State: state, Since: time.Now()}
	if err != nil {
		st.Error = err.Error()
	}
	s.st.Stack = st
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
	if st.Stack != nil {
		k := *st.Stack
		st.Stack = &k
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

// DialGuest connects to a vsock port of the started VM, whether or not its
// agent is ready, and fails with ErrUnavailable when no VM is running.
func (s *Supervisor) DialGuest(ctx context.Context, port uint32) (net.Conn, error) {
	s.mu.Lock()
	m := s.running
	s.mu.Unlock()
	if m == nil {
		return nil, ErrUnavailable
	}
	return m.DialVsock(ctx, port)
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
