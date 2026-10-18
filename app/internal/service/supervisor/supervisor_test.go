package supervisor

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/agent"
	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
	"github.com/ohcnetwork/care_desktop/app/internal/engine/vm"
	"github.com/ohcnetwork/care_desktop/app/internal/service/api"
	"github.com/ohcnetwork/care_desktop/app/internal/service/slots"
	"github.com/ohcnetwork/care_desktop/app/internal/service/slots/slotstest"
	"github.com/ohcnetwork/care_desktop/app/internal/service/supervisor/supervisortest"
)

type env struct {
	sup     *Supervisor
	store   *slots.Store
	factory *supervisortest.Factory
	cancel  context.CancelFunc
	stopped chan struct{}
}

func newEnv(t *testing.T, versions ...string) *env {
	t.Helper()
	dir := t.TempDir()
	store := slots.New(filepath.Join(dir, "slots"), "arm64")
	for _, v := range versions {
		path, sum := slotstest.WriteTestBundle(t, slotstest.BundleEntries(v, "arm64"))
		if _, err := store.Install(t.Context(), path, sum); err != nil {
			t.Fatal(err)
		}
	}
	if len(versions) > 0 {
		if err := store.Activate(versions[0]); err != nil {
			t.Fatal(err)
		}
	}
	f := &supervisortest.Factory{Dir: dir}
	sup := New(Config{
		Slots:            store,
		VM:               vm.Config{DataDisk: filepath.Join(dir, "data.img"), ConsoleLog: filepath.Join(dir, "console.log")},
		NewMachine:       f.New,
		ReadyTimeout:     300 * time.Millisecond,
		PollInterval:     10 * time.Millisecond,
		HealthInterval:   20 * time.Millisecond,
		StopTimeout:      time.Second,
		ForceStopTimeout: 100 * time.Millisecond,
		BackoffMin:       20 * time.Millisecond,
		BackoffMax:       200 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	e := &env{sup: sup, store: store, factory: f, cancel: cancel, stopped: make(chan struct{})}
	go func() { sup.Run(ctx); close(e.stopped) }()
	t.Cleanup(func() {
		cancel()
		<-e.stopped
		if f.Overlapped() {
			t.Error("two machines ran at once")
		}
	})
	return e
}

// wait returns the first snapshot for which ok is true.
func (e *env) wait(t *testing.T, what string, ok func(Status) bool) Status {
	t.Helper()
	ch, cancel := e.sup.Subscribe()
	defer cancel()
	timeout := time.After(5 * time.Second)
	var last Status
	for {
		select {
		case last = <-ch:
			if ok(last) {
				return last
			}
		case <-timeout:
			t.Fatalf("timed out waiting for %s; last status %+v", what, last)
		}
	}
}

func (e *env) waitState(t *testing.T, s State) Status {
	t.Helper()
	return e.wait(t, string(s), func(st Status) bool { return st.State == s })
}

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestNoEngine(t *testing.T) {
	e := newEnv(t)
	e.waitState(t, StateNoEngine)
	if err := e.sup.Start(ctxT(t)); !errors.Is(err, ErrNoEngine) {
		t.Fatalf("start = %v", err)
	}
	if _, err := e.sup.DialAgent(ctxT(t)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("dial = %v", err)
	}
	if len(e.factory.Machines()) != 0 {
		t.Fatal("a machine was created")
	}
}

func TestBootReadyAndStop(t *testing.T) {
	e := newEnv(t, "1.0.0")
	if err := e.sup.Start(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	st := e.waitState(t, StateReady)
	if st.EngineVersion != "1.0.0" || st.Agent == nil || !st.Agent.DockerReady || st.StartedAt.IsZero() {
		t.Fatalf("status = %+v", st)
	}
	m := e.factory.Last()
	if m.Config.BundleDir != e.store.Dir("1.0.0") || m.Config.DataDisk == "" {
		t.Fatalf("vm config = %+v", m.Config)
	}
	e.wait(t, "SetTime", func(Status) bool {
		for _, c := range m.Exec.Calls() {
			if c.Method == "SetTime" && time.Since(c.Time) < time.Minute {
				return true
			}
		}
		return false
	})
	conn, err := e.sup.DialAgent(ctxT(t))
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	// A second Start is a no-op.
	if err := e.sup.Start(ctxT(t)); err != nil || len(e.factory.Machines()) != 1 {
		t.Fatalf("second start: %v, %d machines", err, len(e.factory.Machines()))
	}

	if err := e.sup.Stop(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	if st := e.sup.Status(); st.State != StateStopped || st.Agent != nil {
		t.Fatalf("after stop: %+v", st)
	}
	if m.Forced() || m.State() != vm.StateStopped {
		t.Fatalf("not a graceful stop: forced=%v state=%v", m.Forced(), m.State())
	}
	if _, err := e.sup.DialAgent(ctxT(t)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("dial after stop = %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if n := len(e.factory.Machines()); n != 1 {
		t.Fatalf("restarted after stop: %d machines", n)
	}
}

func TestCrashRestartsWithBackoff(t *testing.T) {
	e := newEnv(t, "1.0.0")
	if err := e.sup.Start(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	e.waitState(t, StateReady)
	first := e.factory.Last()
	first.Crash(errors.New("boom"))
	st := e.waitState(t, StateBackoff)
	if !strings.Contains(st.LastError, "unexpectedly") || !strings.Contains(st.LastError, "boom") || !strings.Contains(st.LastError, "console.log") {
		t.Fatalf("last error = %q", st.LastError)
	}
	if st.NextRetryAt.IsZero() {
		t.Fatal("no retry time")
	}
	st = e.wait(t, "ready after restart", func(s Status) bool { return s.State == StateReady && s.Restarts == 1 })
	if e.factory.Last() == first {
		t.Fatal("no new machine")
	}
	// A guest power-off the service did not ask for is also a crash.
	e.factory.Last().Crash(nil)
	e.wait(t, "second restart", func(s Status) bool { return s.State == StateReady && s.Restarts == 2 })
}

func TestStartFailuresBackOff(t *testing.T) {
	e := newEnv(t, "1.0.0")
	e.factory.StartErr = errors.New("data disk in use")
	if err := e.sup.Start(ctxT(t)); err == nil {
		t.Fatal("start succeeded")
	}
	e.wait(t, "three retries", func(s Status) bool { return s.Restarts >= 3 && s.State == StateBackoff })
	if err := e.sup.Stop(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	n := len(e.factory.Machines())
	time.Sleep(300 * time.Millisecond)
	if len(e.factory.Machines()) != n || e.sup.Status().State != StateStopped {
		t.Fatalf("kept retrying after stop: %+v", e.sup.Status())
	}
}

func TestBackoffDelays(t *testing.T) {
	s := New(Config{BackoffMin: time.Second, BackoffMax: 2 * time.Minute})
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 64 * time.Second, 2 * time.Minute, 2 * time.Minute}
	for i, n := range []int{1, 2, 3, 7, 8, 100} {
		if got := s.backoff(n); got != want[i] {
			t.Errorf("backoff(%d) = %v, want %v", n, got, want[i])
		}
	}
}

// Docker going down while the agent answers is reported, not restarted.
func TestNotReadyWhileRunningKeepsVM(t *testing.T) {
	e := newEnv(t, "1.0.0")
	if err := e.sup.Start(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	e.waitState(t, StateReady)
	m := e.factory.Last()
	m.Exec.SetPingErr(errors.New("docker down"))
	st := e.waitState(t, StateNotReady)
	if st.Agent == nil || st.Agent.DockerReady || !strings.Contains(st.LastError, "not ready") {
		t.Fatalf("status = %+v", st)
	}
	// The agent answers, so it is reachable for diagnostics.
	conn, err := e.sup.DialAgent(ctxT(t))
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	time.Sleep(10 * 20 * time.Millisecond) // many health intervals
	if n := len(e.factory.Machines()); n != 1 || m.Forced() {
		t.Fatalf("watchdog fired on a reachable agent: %d machines, forced=%v", n, m.Forced())
	}
	m.Exec.SetPingErr(nil)
	e.waitState(t, StateReady)
}

func TestWatchdogNeverReady(t *testing.T) {
	e := newEnv(t, "1.0.0")
	e.factory.Setup = func(x *agent.FakeExecutor) { x.PingErr = errors.New("docker down") }
	if err := e.sup.Start(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	first := e.factory.Last()
	st := e.waitState(t, StateBackoff)
	if !strings.Contains(st.LastError, "watchdog") || !strings.Contains(st.LastError, "after boot") {
		t.Fatalf("last error = %q", st.LastError)
	}
	if !first.Forced() {
		t.Fatal("hung VM was not forced off")
	}
	e.factory.Setup = nil
	e.wait(t, "ready after restart", func(s Status) bool { return s.State == StateReady && s.Restarts == 1 })
	if e.factory.Last() == first {
		t.Fatal("no new machine")
	}
}

func TestWatchdogUnreachableWhileRunning(t *testing.T) {
	e := newEnv(t, "1.0.0")
	if err := e.sup.Start(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	e.waitState(t, StateReady)
	first := e.factory.Last()
	first.Hang()
	st := e.waitState(t, StateBackoff)
	if !strings.Contains(st.LastError, "unreachable for 4 health checks") {
		t.Fatalf("last error = %q", st.LastError)
	}
	if !first.Forced() {
		t.Fatal("hung VM was not forced off")
	}
	e.wait(t, "ready after restart", func(s Status) bool { return s.State == StateReady && s.Restarts == 1 })
}

func TestWatchdogVMWontStop(t *testing.T) {
	e := newEnv(t, "1.0.0")
	if err := e.sup.Start(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	e.waitState(t, StateReady)
	first := e.factory.Last()
	first.Stick(errors.New("hypervisor wedged"))
	first.Hang()
	e.wait(t, "refusing to boot over the stuck VM", func(s Status) bool {
		return s.State == StateBackoff && strings.Contains(s.LastError, "has not stopped")
	})
	if n := len(e.factory.Machines()); n != 1 {
		t.Fatalf("booted %d machines while the first was still running", n)
	}
	// Run stays responsive.
	if err := e.sup.Stop(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	if st := e.sup.Status(); st.State != StateStopped {
		t.Fatalf("state after stop = %s", st.State)
	}
	if err := e.sup.Start(ctxT(t)); err == nil {
		t.Fatal("started while the old VM is still running")
	}
	first.Crash(nil) // finally stops
	e.wait(t, "ready after the old VM stopped", func(s Status) bool { return s.State == StateReady })
	if n := len(e.factory.Machines()); n != 2 {
		t.Fatalf("machines = %d, want 2", n)
	}
}

func TestDialGuest(t *testing.T) {
	e := newEnv(t, "1.0.0")
	if _, err := e.sup.DialGuest(ctxT(t), vm.AgentVsockPort); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("dial while stopped = %v", err)
	}
	if err := e.sup.Start(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	e.waitState(t, StateReady)
	conn, err := e.sup.DialGuest(ctxT(t), vm.AgentVsockPort)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if _, err := e.sup.DialGuest(ctxT(t), 8080); err == nil || errors.Is(err, ErrUnavailable) {
		t.Fatalf("dial of a closed guest port = %v", err)
	}
	if err := e.sup.Stop(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.sup.DialGuest(ctxT(t), vm.AgentVsockPort); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("dial after stop = %v", err)
	}
}

func TestSwitchSlot(t *testing.T) {
	e := newEnv(t, "1.0.0", "2.0.0")
	if err := e.sup.Switch(ctxT(t), "3.0.0"); !errors.Is(err, slots.ErrNotInstalled) {
		t.Fatalf("switch to missing = %v", err)
	}
	if err := e.sup.Start(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	e.waitState(t, StateReady)
	first := e.factory.Last()
	if err := e.sup.Switch(ctxT(t), "2.0.0"); err != nil {
		t.Fatal(err)
	}
	if first.State() != vm.StateStopped || first.Forced() {
		t.Fatalf("old VM: state=%v forced=%v", first.State(), first.Forced())
	}
	st := e.waitState(t, StateReady)
	if st.EngineVersion != "2.0.0" || e.factory.Last().Config.BundleDir != e.store.Dir("2.0.0") {
		t.Fatalf("status = %+v", st)
	}
	p, _ := e.store.Pointer()
	if p != (slots.Pointer{Current: "2.0.0", Previous: "1.0.0"}) {
		t.Fatalf("pointer = %+v", p)
	}
}

func TestStopForcesWhenAgentRefuses(t *testing.T) {
	e := newEnv(t, "1.0.0")
	e.factory.Setup = func(x *agent.FakeExecutor) { x.PoweredOff = nil } // the guest never powers off
	if err := e.sup.Start(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	e.waitState(t, StateReady)
	m := e.factory.Last()
	if err := e.sup.Stop(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	if !m.Forced() {
		t.Fatal("VM was not force-stopped after the stop timeout")
	}
}

func TestRunStopsVMOnExit(t *testing.T) {
	e := newEnv(t, "1.0.0")
	if err := e.sup.Start(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	e.waitState(t, StateReady)
	e.cancel()
	<-e.stopped
	m := e.factory.Last()
	if m.State() != vm.StateStopped || m.Forced() {
		t.Fatalf("state=%v forced=%v", m.State(), m.Forced())
	}
	if err := e.sup.Start(ctxT(t)); !errors.Is(err, ErrClosed) {
		t.Fatalf("start after exit = %v", err)
	}
}

func (e *env) waitStack(t *testing.T, s api.StackState) Status {
	t.Helper()
	return e.wait(t, "stack "+string(s), func(st Status) bool { return st.Stack != nil && st.Stack.State == s })
}

func TestStackStartsAfterBootAndStopsFirst(t *testing.T) {
	e := newEnv(t, "1.0.0")
	if err := e.sup.Start(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	e.waitStack(t, api.StackRunning)
	m := e.factory.Last()
	if err := e.sup.Stop(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	if st := e.sup.Status(); st.Stack == nil || st.Stack.State != api.StackStopped {
		t.Fatalf("stack after stop = %+v", st.Stack)
	}
	var methods []string
	for _, c := range m.Exec.Calls() {
		if c.Method == "Ping" || c.Method == "SetTime" {
			continue
		}
		methods = append(methods, c.Method)
		if up, ok := c.Op.(*protocol.StackUp); ok && !up.Wait {
			t.Errorf("StackUp = %+v, want Wait", up)
		}
	}
	if got := strings.Join(methods, " "); got != "StackUp StackStop PowerOff" {
		t.Fatalf("agent calls = %s", got)
	}
}

func TestStackStartOutcomes(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want api.StackState
	}{
		{&protocol.OpError{Code: protocol.CodeNotInstalled, Message: "no clinic"}, api.StackNotInstalled},
		{&protocol.OpError{Code: protocol.CodeCommandFailed, Message: "unhealthy"}, api.StackFailed},
	} {
		e := newEnv(t, "1.0.0")
		e.factory.Setup = func(x *agent.FakeExecutor) {
			x.Handle = func(_ context.Context, op protocol.Op, _ agent.Emitter) (any, error) {
				if _, ok := op.(*protocol.StackUp); ok {
					return nil, tc.err
				}
				return nil, nil
			}
		}
		if err := e.sup.Start(ctxT(t)); err != nil {
			t.Fatal(err)
		}
		st := e.waitStack(t, tc.want)
		if tc.want == api.StackFailed && !strings.Contains(st.Stack.Error, "unhealthy") {
			t.Errorf("stack error = %q", st.Stack.Error)
		}
		if st.State != StateReady {
			t.Errorf("engine state = %s; a stack failure must not affect the engine", st.State)
		}
	}
}

func TestStackStateFollowsTheEngineAfterBoot(t *testing.T) {
	e := newEnv(t, "1.0.0")
	var mu sync.Mutex
	var services []protocol.ServiceState
	e.factory.Setup = func(x *agent.FakeExecutor) {
		x.Handle = func(_ context.Context, op protocol.Op, _ agent.Emitter) (any, error) {
			switch op.(type) {
			case *protocol.StackUp:
				return nil, &protocol.OpError{Code: protocol.CodeNotInstalled, Message: "no clinic"}
			case *protocol.StackStatus:
				mu.Lock()
				defer mu.Unlock()
				return protocol.StackStatusResult{Services: slices.Clone(services)}, nil
			}
			return nil, nil
		}
	}
	if err := e.sup.Start(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	e.waitStack(t, api.StackNotInstalled)

	mu.Lock()
	services = []protocol.ServiceState{{Name: "backend", State: "running", Health: "healthy"}, {Name: "db", State: "running"}}
	mu.Unlock()
	e.waitStack(t, api.StackRunning)

	mu.Lock()
	services = []protocol.ServiceState{{Name: "backend", State: "exited"}, {Name: "db", State: "exited"}}
	mu.Unlock()
	e.waitStack(t, api.StackStopped)

	mu.Lock()
	services = nil
	mu.Unlock()
	time.Sleep(100 * time.Millisecond)
	if st := e.sup.Status(); st.Stack == nil || st.Stack.State != api.StackStopped {
		t.Fatalf("an empty stack status changed the state: %+v", st.Stack)
	}
}

// A stack start still running at shutdown must not make the agent refuse to
// power off (which would force the VM off).
func TestStopWaitsForBusyAgent(t *testing.T) {
	e := newEnv(t, "1.0.0")
	e.factory.Setup = func(x *agent.FakeExecutor) {
		x.Handle = func(ctx context.Context, op protocol.Op, _ agent.Emitter) (any, error) {
			if _, ok := op.(*protocol.StackUp); ok {
				time.Sleep(300 * time.Millisecond)
			}
			return nil, nil
		}
	}
	if err := e.sup.Start(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	e.waitStack(t, api.StackStarting)
	m := e.factory.Last()
	if err := e.sup.Stop(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	if m.Forced() {
		t.Fatal("VM was forced off while the agent was busy")
	}
}
