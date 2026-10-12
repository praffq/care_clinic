package mdns

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

func TestManagerRefreshHonorsCancellation(t *testing.T) {
	m, all := managerFixture()
	m.Refresh("care")
	entered := make(chan struct{})
	m.check = func(ctx context.Context, _ string) error {
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.RefreshContext(ctx, "care"); close(done) }()
	<-entered
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("manager ignored cancelled probe")
	}
	m.Stop()
	if !(*all)[0].stopped || m.Status().Running {
		t.Fatal("cancelled probe prevented withdrawal")
	}
}

type fakeAdvertisement struct {
	name    string
	stopped bool
	changed bool
	ipError error
	probe   error
}

func (a *fakeAdvertisement) Name() string              { return a.name }
func (a *fakeAdvertisement) Stop()                     { a.stopped = true }
func (a *fakeAdvertisement) IPsChanged() (bool, error) { return a.changed, a.ipError }
func (a *fakeAdvertisement) Resolves() error           { return a.probe }

func managerFixture() (*Manager, *[]*fakeAdvertisement) {
	var all []*fakeAdvertisement
	m := NewManager(nil)
	m.check = func(context.Context, string) error { return nil }
	m.advertise = func(_ context.Context, name string, _ func(string)) (managedAdvertiser, error) {
		a := &fakeAdvertisement{name: name}
		all = append(all, a)
		return a, nil
	}
	return m, &all
}

func TestManagerNameNetworkAndStop(t *testing.T) {
	m, all := managerFixture()
	if s := m.Refresh(""); s.Running || len(*all) != 0 {
		t.Fatal("disabled clinic advertised")
	}
	if s := m.Refresh("care.local"); !s.Running || !s.Verified || s.Name != "care" {
		t.Fatalf("not ready: %+v", s)
	}
	m.Refresh("care")
	if len(*all) != 1 {
		t.Fatal("duplicate advertiser for the same owner/name")
	}
	first := (*all)[0]
	first.changed = true
	m.Refresh("care")
	if !first.stopped || len(*all) != 2 {
		t.Fatal("IP transition did not replace responder")
	}
	m.Refresh("new-care")
	if !(*all)[1].stopped || len(*all) != 3 {
		t.Fatal("domain change did not withdraw old name")
	}
	m.Refresh("")
	if !(*all)[2].stopped || m.Status().Running {
		t.Fatal("stopped clinic still advertised")
	}
	m.Refresh("new-care")
	m.Stop()
	if m.Status().Running || !(*all)[3].stopped {
		t.Fatal("helper shutdown kept advertiser")
	}
}

func TestManagerConflictAndRecovery(t *testing.T) {
	m, all := managerFixture()
	m.Refresh("care")
	m.check = func(context.Context, string) error {
		return &ConflictError{Host: "care.local", Address: net.IPv4(10, 0, 0, 2)}
	}
	s := m.Refresh("care")
	if !s.Conflict || s.Running || s.Verified || s.Error == "" || !(*all)[0].stopped {
		t.Fatalf("conflict not withdrawn/reported: %+v", s)
	}
	m.check = func(context.Context, string) error { return nil }
	if s := m.Refresh("care"); !s.Verified || s.Error != "" || s.Conflict {
		t.Fatalf("conflict did not recover: %+v", s)
	}
}

func TestManagerProbeFailureRetriesAndReports(t *testing.T) {
	m, all := managerFixture()
	m.Refresh("care")
	(*all)[0].probe = errors.New("no hostname answer")
	if s := m.Refresh("care"); s.Verified || s.Error == "" || !s.Running {
		t.Fatalf("first miss not reported: %+v", s)
	}
	if s := m.Refresh("care"); s.Running || !(*all)[0].stopped {
		t.Fatal("second miss must withdraw broken responder")
	}
	if s := m.Refresh("care"); !s.Verified || len(*all) != 2 {
		t.Fatal("failed responder not retried")
	}
	m.advertise = func(context.Context, string, func(string)) (managedAdvertiser, error) {
		return nil, errors.New("no LAN")
	}
	m.Stop()
	if s := m.Refresh("care"); s.Running || s.Error != "no LAN" {
		t.Fatalf("missing interface not reported: %+v", s)
	}
}
