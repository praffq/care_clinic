package main

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/service/api"
	"github.com/ohcnetwork/care_desktop/app/internal/service/slots"
	"github.com/ohcnetwork/care_desktop/app/internal/stack/stacktest"
)

// fakeAPI is a care-clinic-service fake: the stack part relays to a real
// agent server (stacktest); the engine part is scripted.
type fakeAPI struct {
	*stacktest.Service

	mu        sync.Mutex
	status    api.ServiceStatus
	statusErr error
	// onStart, if set, is called by Start (under the lock).
	onStart   func(*api.ServiceStatus)
	installed []string // "<size>:<sha>"
	activated []string
	starts    int
	copies    int
	installFn func(data []byte, sha string) (slots.Slot, error)
	slotList  api.SlotList
}

func newFakeAPI(t *testing.T) *fakeAPI {
	return &fakeAPI{
		Service: fakeService(t),
		status: api.ServiceStatus{
			Engine: api.EngineStatus{State: api.StateReady, Stack: &api.StackStatus{State: api.StackRunning}},
			Slots:  slots.Pointer{Current: "0.1.1"},
		},
	}
}

func (f *fakeAPI) set(fn func(*api.ServiceStatus)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(&f.status)
}

func (f *fakeAPI) Status(context.Context) (api.ServiceStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status, f.statusErr
}

func (f *fakeAPI) Watch(ctx context.Context, fn func(api.ServiceStatus)) error {
	st, err := f.Status(ctx)
	if err != nil {
		return err
	}
	fn(st)
	<-ctx.Done()
	return ctx.Err()
}

func (f *fakeAPI) Start(context.Context) (api.EngineStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	if f.onStart != nil {
		f.onStart(&f.status)
	}
	return f.status.Engine, nil
}

func (f *fakeAPI) Slots(context.Context) (api.SlotList, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.slotList, nil
}

func (f *fakeAPI) InstallEngine(_ context.Context, r io.Reader, size int64, sha string) (slots.Slot, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return slots.Slot{}, err
	}
	if int64(len(data)) != size {
		return slots.Slot{}, errors.New("size mismatch")
	}
	f.mu.Lock()
	f.installed = append(f.installed, sha)
	fn := f.installFn
	f.mu.Unlock()
	if fn != nil {
		return fn(data, sha)
	}
	return slots.Slot{Version: string(data)}, nil
}

func (f *fakeAPI) Activate(_ context.Context, version string) (api.EngineStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.activated = append(f.activated, version)
	f.status.Slots.Current = version
	return f.status.Engine, nil
}

func (f *fakeAPI) CopyBackups(context.Context) (api.BackupStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.copies++
	if f.status.Backups == nil {
		return api.BackupStatus{}, nil
	}
	return *f.status.Backups, nil
}
