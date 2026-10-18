package advertise

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/sys/mdns"
)

type fakeAdv struct {
	name     string
	checks   []error
	changes  []bool
	resolves error

	mu      sync.Mutex
	stopped bool
}

func (f *fakeAdv) Name() string { return f.name }

func (f *fakeAdv) IPsChanged() (bool, error) {
	if len(f.changes) == 0 {
		return false, nil
	}
	c := f.changes[0]
	f.changes = f.changes[1:]
	return c, nil
}

func (f *fakeAdv) CheckAvailable() error {
	if len(f.checks) == 0 {
		return nil
	}
	err := f.checks[0]
	f.checks = f.checks[1:]
	return err
}

func (f *fakeAdv) Resolves() error { return f.resolves }

func (f *fakeAdv) Stop() {
	f.mu.Lock()
	f.stopped = true
	f.mu.Unlock()
}

func (f *fakeAdv) isStopped() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stopped
}

func duplicate() error {
	return &mdns.ConflictError{Host: "care.local", Address: net.ParseIP("192.168.1.9")}
}

// factory hands out the given advertisers in order (then fresh ones) and
// records the names asked for.
type factory struct {
	mu    sync.Mutex
	next  []*fakeAdv
	errs  []error
	names []string
	made  []*fakeAdv
}

func (f *factory) advertise(name string, _ func(string)) (Advertiser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.names = append(f.names, name)
	if len(f.errs) > 0 {
		err := f.errs[0]
		f.errs = f.errs[1:]
		if err != nil {
			return nil, err
		}
	}
	a := &fakeAdv{name: name}
	if len(f.next) > 0 {
		a = f.next[0]
		f.next = f.next[1:]
	}
	f.made = append(f.made, a)
	return a, nil
}

func (f *factory) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.made)
}

func newService(f *factory) *Service {
	return New(Config{Name: "care", Advertise: f.advertise, Interval: time.Hour, Recheck: time.Minute, Settle: 12 * time.Second})
}

func TestConflictIsConfirmedThenStopsAndReports(t *testing.T) {
	adv := &fakeAdv{name: "care", checks: []error{duplicate(), duplicate()}}
	f := &factory{next: []*fakeAdv{adv}}
	s := newService(f)
	now := time.Now()
	s.start(now)
	if !s.Status().Advertising {
		t.Fatalf("status = %+v", s.Status())
	}
	// No conflict probe before the advertisement settles.
	if d := s.check(now.Add(time.Second)); d != time.Hour || len(adv.checks) != 2 {
		t.Fatalf("probed before settling: %v, %d checks left", d, len(adv.checks))
	}
	later := now.Add(13 * time.Second)
	if d := s.check(later); d != time.Minute || adv.isStopped() || s.Status().Conflict != nil {
		t.Fatalf("first sighting: next %v, stopped %v, status %+v", d, adv.isStopped(), s.Status())
	}
	if d := s.check(later.Add(time.Minute)); d != time.Hour {
		t.Fatalf("second sighting: next %v", d)
	}
	st := s.Status()
	if !adv.isStopped() || st.Advertising || st.Conflict == nil || st.Conflict.Address != "192.168.1.9" || st.LastError == "" {
		t.Fatalf("conflict not reported: stopped %v, status %+v", adv.isStopped(), st)
	}
}

func TestSingleSightingIsNotAConflict(t *testing.T) {
	adv := &fakeAdv{name: "care", checks: []error{duplicate(), nil}}
	s := newService(&factory{next: []*fakeAdv{adv}})
	now := time.Now()
	s.start(now)
	s.check(now.Add(13 * time.Second))
	s.check(now.Add(time.Minute))
	if adv.isStopped() || s.Status().Conflict != nil || !s.Status().Advertising {
		t.Fatalf("stopped %v, status %+v", adv.isStopped(), s.Status())
	}
}

func TestLANChangeReadvertises(t *testing.T) {
	adv := &fakeAdv{name: "care", changes: []bool{true}}
	f := &factory{next: []*fakeAdv{adv}}
	s := newService(f)
	now := time.Now()
	s.start(now)
	s.check(now.Add(time.Second))
	if !adv.isStopped() || f.count() != 2 || !s.Status().Advertising {
		t.Fatalf("not re-advertised: stopped %v, made %d", adv.isStopped(), f.count())
	}
}

func TestStopsResolvingReadvertises(t *testing.T) {
	adv := &fakeAdv{name: "care", resolves: errors.New("timeout")}
	f := &factory{next: []*fakeAdv{adv}}
	s := newService(f)
	now := time.Now()
	s.start(now)
	s.check(now.Add(time.Second))
	if adv.isStopped() {
		t.Fatal("re-advertised after one miss")
	}
	s.check(now.Add(2 * time.Second))
	if !adv.isStopped() || f.count() != 2 {
		t.Fatalf("not re-advertised after two misses: made %d", f.count())
	}
}

func TestStartFailureRetriesSoon(t *testing.T) {
	f := &factory{errs: []error{errors.New("no multicast-capable LAN IPv4 interface found")}}
	s := newService(f)
	if d := s.start(time.Now()); d != time.Minute {
		t.Fatalf("retry after %v", d)
	}
	if st := s.Status(); st.Advertising || st.LastError == "" || st.Conflict != nil {
		t.Fatalf("status = %+v", st)
	}
	s.check(time.Now())
	if st := s.Status(); !st.Advertising || st.LastError != "" {
		t.Fatalf("status after retry = %+v", st)
	}
}

func TestRunRenameAndStop(t *testing.T) {
	f := &factory{}
	s := New(Config{Name: "care", Advertise: f.advertise, Interval: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	waitFor(t, func() bool { return s.Status().Advertising })
	s.SetName("clinic-2")
	waitFor(t, func() bool { st := s.Status(); return st.Advertising && st.Name == "clinic-2" })
	cancel()
	<-done
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.made) != 2 || f.names[1] != "clinic-2" || !f.made[0].isStopped() || !f.made[1].isStopped() {
		t.Fatalf("names %v, made %d", f.names, len(f.made))
	}
	if s.Status().Advertising {
		t.Fatal("still advertising after Run returned")
	}
}

func TestDisabled(t *testing.T) {
	f := &factory{}
	s := New(Config{Name: "care", Disabled: true, Advertise: f.advertise})
	s.Run(context.Background())
	s.SetName("other")
	if st := s.Status(); st.Enabled || st.Advertising || st.Name != "other" || f.count() != 0 {
		t.Fatalf("status = %+v, made %d", st, f.count())
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
