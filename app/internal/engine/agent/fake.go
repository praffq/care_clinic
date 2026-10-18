package agent

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
)

// FakeCall records one FakeExecutor invocation.
type FakeCall struct {
	Method   string
	Op       protocol.Op // nil for Ping, Containers, SetTime, PowerOff
	Body     []byte      // image.load tarball
	Password []byte      // manage password
	Time     time.Time   // SetTime argument
}

// FakeExecutor is an in-memory Executor for tests. Every op call is recorded
// and then delegated to Handle (when set), which may emit frames and return a
// result (a value of the op's result type, or nil) and an error.
type FakeExecutor struct {
	Handle func(ctx context.Context, op protocol.Op, em Emitter) (any, error)

	PingErr       error
	ContainerList []string
	ContainersErr error
	SetTimeErr    error
	PowerOffErr   error
	// PoweredOff, when non-nil, is closed on the first PowerOff call.
	PoweredOff chan struct{}

	mu    sync.Mutex
	calls []FakeCall
	once  sync.Once
}

var _ Executor = (*FakeExecutor)(nil)

// Calls returns a copy of the recorded calls.
func (f *FakeExecutor) Calls() []FakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]FakeCall(nil), f.calls...)
}

func (f *FakeExecutor) record(c FakeCall) {
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.mu.Unlock()
}

func (f *FakeExecutor) do(ctx context.Context, method string, op protocol.Op, em Emitter) (any, error) {
	f.record(FakeCall{Method: method, Op: op})
	if f.Handle == nil {
		return nil, nil
	}
	return f.Handle(ctx, op, em)
}

func (f *FakeExecutor) none(ctx context.Context, method string, op protocol.Op, em Emitter) error {
	_, err := f.do(ctx, method, op, em)
	return err
}

func (f *FakeExecutor) Ping(context.Context) error {
	f.record(FakeCall{Method: "Ping"})
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.PingErr
}

// SetPingErr changes PingErr while the executor is in use.
func (f *FakeExecutor) SetPingErr(err error) {
	f.mu.Lock()
	f.PingErr = err
	f.mu.Unlock()
}

func (f *FakeExecutor) StackUp(ctx context.Context, op protocol.StackUp, em Emitter) error {
	return f.none(ctx, "StackUp", &op, em)
}

func (f *FakeExecutor) StackStop(ctx context.Context, op protocol.StackStop, em Emitter) error {
	return f.none(ctx, "StackStop", &op, em)
}

func (f *FakeExecutor) StackDown(ctx context.Context, op protocol.StackDown, em Emitter) error {
	return f.none(ctx, "StackDown", &op, em)
}

func (f *FakeExecutor) StackStatus(ctx context.Context) (protocol.StackStatusResult, error) {
	res, err := f.do(ctx, "StackStatus", &protocol.StackStatus{}, nopEmitter{})
	r, _ := res.(protocol.StackStatusResult)
	if r.Services == nil {
		r.Services = []protocol.ServiceState{}
	}
	return r, err
}

func (f *FakeExecutor) StackLogs(ctx context.Context, op protocol.StackLogs, em Emitter) error {
	return f.none(ctx, "StackLogs", &op, em)
}

func (f *FakeExecutor) RunMigrations(ctx context.Context, em Emitter) error {
	return f.none(ctx, "RunMigrations", &protocol.RunMigrations{}, em)
}

func (f *FakeExecutor) ImagePull(ctx context.Context, op protocol.ImagePull, em Emitter) error {
	return f.none(ctx, "ImagePull", &op, em)
}

func (f *FakeExecutor) ImageLoad(ctx context.Context, op protocol.ImageLoad, tar io.Reader, em Emitter) error {
	body, err := io.ReadAll(tar)
	if err != nil {
		return err
	}
	f.record(FakeCall{Method: "ImageLoad", Op: &op, Body: body})
	if f.Handle == nil {
		return nil
	}
	_, err = f.Handle(ctx, &op, em)
	return err
}

func (f *FakeExecutor) ImageBuild(ctx context.Context, op protocol.ImageBuild, em Emitter) (protocol.ImageBuildResult, error) {
	res, err := f.do(ctx, "ImageBuild", &op, em)
	r, _ := res.(protocol.ImageBuildResult)
	return r, err
}

func (f *FakeExecutor) ImagePrune(ctx context.Context, op protocol.ImagePrune, em Emitter) error {
	return f.none(ctx, "ImagePrune", &op, em)
}

func (f *FakeExecutor) DBDump(ctx context.Context, op protocol.DBDump, em Emitter) (protocol.DBDumpResult, error) {
	res, err := f.do(ctx, "DBDump", &op, em)
	r, _ := res.(protocol.DBDumpResult)
	return r, err
}

func (f *FakeExecutor) DBRestore(ctx context.Context, op protocol.DBRestore, em Emitter) error {
	return f.none(ctx, "DBRestore", &op, em)
}

func (f *FakeExecutor) Manage(ctx context.Context, op protocol.ManageCmd, password []byte, em Emitter) error {
	f.record(FakeCall{Method: "Manage", Op: &op, Password: append([]byte(nil), password...)})
	if f.Handle == nil {
		return nil
	}
	_, err := f.Handle(ctx, &op, em)
	return err
}

func (f *FakeExecutor) BackupRun(ctx context.Context, em Emitter) (protocol.BackupRunResult, error) {
	res, err := f.do(ctx, "BackupRun", &protocol.BackupRun{}, em)
	r, _ := res.(protocol.BackupRunResult)
	return r, err
}

func (f *FakeExecutor) Containers(context.Context) ([]string, error) {
	f.record(FakeCall{Method: "Containers"})
	return f.ContainerList, f.ContainersErr
}

func (f *FakeExecutor) SetTime(_ context.Context, now time.Time) error {
	f.record(FakeCall{Method: "SetTime", Time: now})
	return f.SetTimeErr
}

func (f *FakeExecutor) PowerOff(context.Context) error {
	f.record(FakeCall{Method: "PowerOff"})
	f.mu.Lock()
	ch := f.PoweredOff
	f.mu.Unlock()
	if ch != nil {
		f.once.Do(func() { close(ch) })
	}
	return f.PowerOffErr
}

type nopEmitter struct{}

func (nopEmitter) Progress(protocol.Progress) {}
func (nopEmitter) Log(protocol.LogLine)       {}
