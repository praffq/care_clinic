package updates_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/feed"
	"github.com/ohcnetwork/care_desktop/app/internal/updates"
	"github.com/ohcnetwork/care_desktop/app/internal/updates/updatestest"
)

// fakeDesktop is the app side of a desktop update. version is the running
// app's version: "relaunching" sets it to what the helper installed.
type fakeDesktop struct {
	mu        sync.Mutex
	version   string
	installed string // what the helper put in place on handoff
	calls     []string
	verifyErr error
	swapFails bool
	svcErrs   []error
}

func (d *fakeDesktop) log(s string) { d.calls = append(d.calls, s) }

func (d *fakeDesktop) Verify(_ context.Context, installer, version string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	b, err := os.ReadFile(installer)
	if err != nil {
		return err
	}
	if string(b) != "installer "+version {
		return errors.New("wrong installer")
	}
	d.log("verify " + version)
	return d.verifyErr
}

func (d *fakeDesktop) Handoff(_ context.Context, _, version string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.log("handoff " + version)
	d.installed = version
	if d.swapFails {
		d.installed = d.version
	}
	return nil
}

func (d *fakeDesktop) UpdateService(context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.log("service")
	if len(d.svcErrs) > 0 {
		err := d.svcErrs[0]
		d.svcErrs = d.svcErrs[1:]
		return err
	}
	return nil
}

func (d *fakeDesktop) relaunch() {
	d.mu.Lock()
	d.version = d.installed
	d.mu.Unlock()
}

func (d *fakeDesktop) callLog() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return strings.Join(d.calls, ", ")
}

func newDesktopHarness(t *testing.T) (*harness, *fakeDesktop) {
	d := &fakeDesktop{version: "1.0.0"}
	h := newHarness(t, func(o *updates.Options) {
		d.mu.Lock()
		o.DesktopVersion = d.version
		d.mu.Unlock()
		o.Desktop = d
	})
	return h, d
}

// relaunch quits the app (the helper swaps it) and opens the new one.
func (h *harness) relaunch(d *fakeDesktop) {
	d.relaunch()
	h.restart()
}

func TestDesktopUpdateRestartsAndContinuesPlan(t *testing.T) {
	h, d := newDesktopHarness(t)
	h.installCare("A")
	// Engine 0.2.0 needs the new desktop app, so the plan is desktop first.
	h.publish(spec{rev: 2, desktops: []string{"1.0.0", "1.1.0"}, engines: []eng{{"0.1.0", 1}, {"0.2.0", 1}}, cares: []string{"A"}, engineNeeds: ">=1.1.0"})
	r, err := h.c.CheckPlan(context.Background())
	if err != nil || steps(r) != "desktop 1.0.0->1.1.0, engine 0.1.0->0.2.0" || r.Prefetched {
		t.Fatalf("check = %+v, %v", r, err)
	}
	if r.Steps[0].Size != int64(len("installer 1.1.0")) {
		t.Fatalf("desktop size = %d", r.Steps[0].Size)
	}
	var got []int64
	dl, err := h.c.PrepareDesktop(context.Background(), func(done, _ int64) { got = append(got, done) })
	if err != nil || dl.Version != "1.1.0" || len(got) == 0 || got[len(got)-1] != int64(len("installer 1.1.0")) {
		t.Fatalf("prepare = %+v, %v (progress %v)", dl, err, got)
	}

	res, err := h.apply()
	if !errors.Is(err, updates.ErrRestartRequired) || res.Desktop == nil || res.Desktop.Version != "1.1.0" {
		t.Fatalf("apply = %+v, %v", res, err)
	}
	st := h.status()
	if st.Journal == nil || st.Journal.State != updates.StateRestarting || !st.Journal.Desktop.Continue {
		t.Fatalf("journal = %+v", st.Journal)
	}
	if _, err := h.apply(); !errors.Is(err, updates.ErrInFlight) {
		t.Fatalf("apply while restarting = %v", err)
	}
	// Opening the old app's coordinator again (e.g. a second process)
	// leaves the journal for the new app.
	h.restart()
	if st := h.status(); st.Journal == nil || st.Journal.State != updates.StateRestarting {
		t.Fatalf("journal after reopen = %+v", st.Journal)
	}

	h.relaunch(d)
	if err := h.c.Resume(context.Background()); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if got := d.callLog(); got != "verify 1.1.0, handoff 1.1.0, service" {
		t.Fatalf("desktop calls = %s", got)
	}
	h.wantOn("0.2.0", "A")
	hs, _ := h.c.History()
	var results []string
	for _, e := range hs[len(hs)-2:] {
		results = append(results, e.Component+" "+e.To+" "+e.Result)
	}
	if strings.Join(results, ", ") != "desktop 1.1.0 applied, engine 0.2.0 applied" {
		t.Fatalf("history = %v", results)
	}
	if st := h.status(); st.Installed.Desktop != "1.1.0" {
		t.Fatalf("installed desktop = %s", st.Installed.Desktop)
	}
}

func TestDesktopSwapFailureKeepsOldApp(t *testing.T) {
	h, d := newDesktopHarness(t)
	h.installCare("A")
	h.publish(spec{rev: 2, desktops: []string{"1.0.0", "1.1.0"}, engines: []eng{{"0.1.0", 1}}, cares: []string{"A"}})
	h.check()
	d.swapFails = true
	if _, err := h.apply(); !errors.Is(err, updates.ErrRestartRequired) {
		t.Fatalf("apply = %v", err)
	}
	h.relaunch(d) // the helper put the old app back and reopened it
	err := h.c.Resume(context.Background())
	oe, ok := errors.AsType[*updates.OpError](err)
	if !ok || oe.Step != updates.StepRelaunch || oe.Result != updates.ResultRolledBack || !strings.Contains(err.Error(), "still version 1.0.0") {
		t.Fatalf("resume = %v", err)
	}
	if e := h.lastHistory(); e.Component != feed.ComponentDesktop || e.Result != updates.ResultRolledBack {
		t.Fatalf("history = %+v", e)
	}
	if st := h.status(); st.Journal != nil || st.Installed.Desktop != "1.0.0" {
		t.Fatalf("status = %+v", st)
	}
	h.wantOn("0.1.0", "A")
}

func TestDesktopVerifyFailureIsNotRetried(t *testing.T) {
	h, d := newDesktopHarness(t)
	h.installCare("A")
	h.publish(spec{rev: 2, desktops: []string{"1.0.0", "1.1.0"}, engines: []eng{{"0.1.0", 1}}, cares: []string{"A"}})
	h.check()
	d.verifyErr = updates.MarkPermanent(errors.New("signed by another team"))
	_, err := h.apply()
	if oe, ok := errors.AsType[*updates.OpError](err); !ok || oe.Step != updates.StepDesktopVerify || !strings.Contains(err.Error(), "current app was kept") {
		t.Fatalf("apply = %v", err)
	}
	if got := d.callLog(); got != "verify 1.1.0" {
		t.Fatalf("desktop calls = %s", got)
	}
}

func TestDesktopServiceFailureCanBeRetried(t *testing.T) {
	h, d := newDesktopHarness(t)
	h.installCare("A")
	h.publish(spec{rev: 2, desktops: []string{"1.0.0", "1.1.0"}, engines: []eng{{"0.1.0", 1}}, cares: []string{"A"}})
	h.check()
	d.svcErrs = []error{updates.MarkPermanent(errors.New("the administrator prompt was cancelled"))}
	if _, err := h.apply(); !errors.Is(err, updates.ErrRestartRequired) {
		t.Fatalf("apply = %v", err)
	}
	h.relaunch(d)
	err := h.c.Resume(context.Background())
	if !errors.Is(err, updates.ErrIncomplete) || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("resume = %v", err)
	}
	st := h.status()
	if st.Journal == nil || st.Journal.State != updates.StateUncertain || st.Journal.FailedStep != updates.StepServiceUpdate {
		t.Fatalf("journal = %+v", st.Journal)
	}
	h.restart()
	if err := h.c.Resume(context.Background()); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if e := h.lastHistory(); e.Result != updates.ResultApplied {
		t.Fatalf("history = %+v", e)
	}
	h.wantOn("0.1.0", "A")
}

func TestDesktopIncompleteRollbackIsRecorded(t *testing.T) {
	h, d := newDesktopHarness(t)
	h.installCare("A")
	h.publish(spec{rev: 2, desktops: []string{"1.0.0", "1.1.0"}, engines: []eng{{"0.1.0", 1}}, cares: []string{"A"}})
	h.check()
	d.svcErrs = []error{updates.MarkPermanent(errors.New("cancelled"))}
	_, _ = h.apply()
	h.relaunch(d)
	if err := h.c.Resume(context.Background()); !errors.Is(err, updates.ErrIncomplete) {
		t.Fatalf("resume = %v", err)
	}
	if err := h.c.Rollback(context.Background()); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if e := h.lastHistory(); e.Result != updates.ResultIncomplete || e.FailedStep != updates.StepServiceUpdate {
		t.Fatalf("history = %+v", e)
	}
	if st := h.status(); st.Journal != nil {
		t.Fatalf("journal = %+v", st.Journal)
	}
}

func TestDesktopCrashBeforeHandoffResumes(t *testing.T) {
	h, d := newDesktopHarness(t)
	h.installCare("A")
	h.publish(spec{rev: 2, desktops: []string{"1.0.0", "1.1.0"}, engines: []eng{{"0.1.0", 1}}, cares: []string{"A"}})
	h.check()
	h.hook.set(crashAt("before:" + updates.StepHandoff))
	if _, err := h.apply(); !errors.Is(err, errCrash) {
		t.Fatalf("apply = %v", err)
	}
	h.hook.set(nil)
	h.restart()
	if st := h.status(); st.Journal == nil || st.Journal.State != updates.StateUncertain {
		t.Fatalf("journal = %+v", st.Journal)
	}
	if err := h.c.Resume(context.Background()); !errors.Is(err, updates.ErrRestartRequired) {
		t.Fatalf("resume = %v", err)
	}
	h.relaunch(d)
	if err := h.c.Resume(context.Background()); err != nil {
		t.Fatalf("resume after relaunch: %v", err)
	}
	if e := h.lastHistory(); e.Component != feed.ComponentDesktop || e.Result != updates.ResultApplied {
		t.Fatalf("history = %+v", e)
	}
}

func TestFetchFileResumesPartialDownload(t *testing.T) {
	h := newHarness(t)
	body := []byte(strings.Repeat("care clinic ", 1000))
	url := h.serve("/big", body)
	dst := filepath.Join(t.TempDir(), "big")
	if err := os.WriteFile(dst+".part", body[:5000], 0o600); err != nil {
		t.Fatal(err)
	}
	var last int64
	if err := updates.FetchFile(context.Background(), nil, url, dst, updatestest.SHA256(body), int64(len(body)), func(done, _ int64) { last = done }); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); string(b) != string(body) || last != int64(len(body)) {
		t.Fatalf("file = %d bytes, progress %d", len(b), last)
	}
	if _, err := os.Stat(dst + ".part"); !os.IsNotExist(err) {
		t.Fatalf(".part left: %v", err)
	}
	h.mu.Lock()
	reqs := strings.Join(h.requests, "; ")
	h.mu.Unlock()
	if reqs != "/big bytes=5000-" {
		t.Fatalf("requests = %s", reqs)
	}
	// A verified file is reused without a request.
	if err := updates.FetchFile(context.Background(), nil, url, dst, updatestest.SHA256(body), int64(len(body)), nil); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	n := len(h.requests)
	h.mu.Unlock()
	if n != 1 {
		t.Fatalf("%d requests", n)
	}
}

func TestFetchFileRejectsBadChecksum(t *testing.T) {
	h := newHarness(t)
	url := h.serve("/f", []byte("tampered"))
	dst := filepath.Join(t.TempDir(), "f")
	err := updates.FetchFile(context.Background(), nil, url, dst, updatestest.SHA256([]byte("original")), 0, nil)
	if !errors.Is(err, updates.ErrChecksum) {
		t.Fatalf("err = %v", err)
	}
	for _, p := range []string{dst, dst + ".part"} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s left: %v", p, err)
		}
	}
}

// A server that ignores Range sends the whole file again; it replaces the
// partial file instead of being appended to it.
func TestFetchFileRestartsWithoutRangeSupport(t *testing.T) {
	body := []byte("the whole installer")
	srv := newPlainServer(t, body)
	dst := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(dst+".part", []byte("the who"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := updates.FetchFile(context.Background(), nil, srv, dst, updatestest.SHA256(body), int64(len(body)), nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); string(b) != string(body) {
		t.Fatalf("file = %q", b)
	}
}

func newPlainServer(t *testing.T, body []byte) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) }))
	t.Cleanup(srv.Close)
	return srv.URL + "/f"
}
