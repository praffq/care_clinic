package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/sys/mdns"
	"github.com/ohcnetwork/care_desktop/app/internal/vm"
)

func TestAdvertisementNotificationsAreClassifiedAndDeduplicated(t *testing.T) {
	a := roleApp(t)
	var events []string
	a.emitEvent = func(event string, args ...any) {
		if event != "care-network-status" {
			t.Fatalf("network error used operation channel: %s", event)
		}
		events = append(events, args[0].(string))
	}
	s := mdns.AdvertisementStatus{Error: "duplicate care.local", Conflict: true}
	_ = a.reportAdvertisementStatus(s)
	_ = a.reportAdvertisementStatus(s)
	a.reportAdvertiseError(errors.New(s.Error))
	if len(events) != 1 || events[0] != failureTitle("network-name") {
		t.Fatalf("conflict emitted multiple notifications: %v", events)
	}
	_ = a.reportAdvertisementStatus(mdns.AdvertisementStatus{Error: "helper handoff failed"})
	if len(events) != 2 || events[1] == failureTitle("network-name") {
		t.Fatalf("generic failure mislabeled: %v", events)
	}
	_ = a.reportAdvertisementStatus(mdns.AdvertisementStatus{})
	if len(events) != 3 || events[2] != "" {
		t.Fatal("recovery did not clear networking notice")
	}
}

func mockAdvertisementHelper(t *testing.T, a *App, handler http.HandlerFunc) {
	t.Helper()
	home, err := os.MkdirTemp("", "ad-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("HOME", home)
	t.Setenv("LOCALAPPDATA", home)
	run := filepath.Join(vm.StateDir(), "run")
	if runtime.GOOS == "darwin" {
		run = filepath.Join(home, ".care-clinic", "run")
	}
	if err := os.MkdirAll(run, 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(run, "control.sock"))
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	a.cfg = Config{Role: roleServer, SetupDone: true, MDNSName: "care.local"}
}

func TestAdvertisementWorkerDoesNotDelayJobsOrOverwritePrimaryError(t *testing.T) {
	if !vm.Supported() {
		t.Skip("helper RPC test")
	}
	a := roleApp(t)
	entered := make(chan struct{}, 1)
	var requests atomic.Int32
	mockAdvertisementHelper(t, a, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/advertisement" {
			requests.Add(1)
			select {
			case entered <- struct{}{}:
			default:
			}
			<-r.Context().Done()
			return
		}
		_ = json.NewEncoder(w).Encode(vm.HostStatus{Running: true, AdvertisementSupported: true})
	})
	done := make(chan []any, 2)
	var mu sync.Mutex
	var primary string
	a.emitEvent = func(event string, args ...any) {
		if event == "care-done" {
			done <- args
		}
		if event == "care-error" {
			mu.Lock()
			primary = args[1].(string)
			mu.Unlock()
		}
	}
	a.advStop = make(chan struct{})
	a.queueAdvertise()
	workerDone := make(chan struct{})
	go func() { defer close(workerDone); a.watchAdvertise() }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not enter slow probe")
	}
	if err := a.runJob(func() error { return errors.New("original clinic error") }, false, "backup-now"); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-done:
		if event[0].(int) != 1 || event[1] != "backup-now" {
			t.Fatalf("wrong completion: %v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("slow advertising delayed care-done")
	}
	mu.Lock()
	if primary != "original clinic error" {
		t.Fatalf("primary failure lost: %q", primary)
	}
	mu.Unlock()
	a.shutdown(context.Background())
	select {
	case <-workerDone:
	case <-time.After(time.Second):
		t.Fatal("closed app retained advertising worker")
	}
}

func TestStopWithdrawsWithoutWaitingForHealthAndKeepsWindowPaused(t *testing.T) {
	a := roleApp(t)
	a.cfg = Config{Role: roleServer, SetupDone: true, MDNSName: "care.local"}
	if vm.Supported() {
		var withdrawn atomic.Bool
		mockAdvertisementHelper(t, a, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/status" {
				_ = json.NewEncoder(w).Encode(vm.HostStatus{Running: true, AdvertisementSupported: true})
				return
			}
			if r.URL.Path == "/advertisement/withdraw" {
				withdrawn.Store(true)
				w.WriteHeader(http.StatusNoContent)
				return
			}
			t.Errorf("Stop invoked slow advertisement verification: %s", r.URL.Path)
		})
		a.advHelper = true
		done := make(chan struct{})
		a.emitEvent = func(event string, _ ...any) {
			if event == "care-done" {
				close(done)
			}
		}
		if err := a.runJob(func() error { return nil }, false, "stop"); err != nil {
			t.Fatal(err)
		}
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("Stop waited for health probe")
		}
		if !withdrawn.Load() {
			t.Fatal("Stop completed without withdrawal")
		}
	} else {
		a.advPaused.Store(true)
	}
	if err := a.startAdvertise(); err != nil || a.adv != nil {
		t.Fatalf("watcher recreated stopped window responder: %v", err)
	}
}

func TestAdvertisementWorkerCoalescesAndReadsLatestName(t *testing.T) {
	if !vm.Supported() {
		t.Skip("helper RPC test")
	}
	a := roleApp(t)
	first := make(chan struct{})
	fresh := make(chan string, 1)
	var requests atomic.Int32
	mockAdvertisementHelper(t, a, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/advertisement" {
			if requests.Add(1) == 1 {
				close(first)
				<-r.Context().Done()
				return
			}
			data, err := os.ReadFile(filepath.Join(vm.StateDir(), "advertisement.json"))
			if err != nil {
				t.Error(err)
				return
			}
			var c vm.AdvertisementConfig
			if err := json.Unmarshal(data, &c); err != nil {
				t.Error(err)
				return
			}
			select {
			case fresh <- c.Name:
			default:
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		a.cfgMu.RLock()
		name := mdns.Label(a.cfg.MDNSName)
		a.cfgMu.RUnlock()
		_ = json.NewEncoder(w).Encode(vm.HostStatus{Running: true, AdvertisementSupported: true,
			Advertisement: mdns.AdvertisementStatus{Name: name, Running: true, Verified: true}})
	})
	a.advStop = make(chan struct{})
	a.queueAdvertise()
	workerDone := make(chan struct{})
	go func() { defer close(workerDone); a.watchAdvertise() }()
	<-first
	a.cfgMu.Lock()
	a.cfg.MDNSName = "latest.local"
	a.cfgMu.Unlock()
	// Keep the worker out while many requests are collapsed into one wakeup.
	a.advWorkMu.Lock()
	a.advGeneration++
	a.advCancel()
	for range 20 {
		select {
		case a.advWake <- struct{}{}:
		default:
		}
	}
	a.advWorkMu.Unlock()
	select {
	case name := <-fresh:
		if name != "latest.local" {
			t.Fatalf("stale name reconciled: %s", name)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("latest name was not reconciled")
	}
	a.shutdown(context.Background())
	<-workerDone
}

func TestPersistentAdvertisementControlsQuitWarning(t *testing.T) {
	ready := vm.HostStatus{Running: true, AdvertisementSupported: true,
		Advertisement: mdns.AdvertisementStatus{Name: "care", Running: true, Verified: true}}
	if !persistentNameStatus("care.local", ready).OK ||
		strings.Contains(clinicQuitMessage("care.local", ready), "may stop working") {
		t.Fatal("confirmed persistent owner still warns about window closure")
	}
	cases := []vm.HostStatus{{}, {Running: true}, ready, ready, ready, ready}
	cases[2].Advertisement.Verified = false
	cases[3].Advertisement.Error = "duplicate network name"
	cases[4].Advertisement.Name = "other"
	cases[5].Running = false
	for _, s := range cases {
		if persistentNameStatus("care.local", s).OK ||
			!strings.Contains(clinicQuitMessage("care.local", s), "may stop working") {
			t.Fatalf("unconfirmed owner suppressed warning: %+v", s)
		}
	}
}

func TestWindowShutdownDoesNotDisablePersistedHelperAdvertisement(t *testing.T) {
	a := roleApp(t)
	a.cfg = Config{Role: roleServer, SetupDone: true, MDNSName: "care.local"}
	if err := a.saveConfig(a.cfg); err != nil {
		t.Fatal(err)
	}

	a.advStop = make(chan struct{})
	a.shutdown(context.Background())
	if a.loadConfig().MDNSName != "care.local" {
		t.Fatal("window closure forgot server state")
	}
	if vm.Supported() {
		data, err := os.ReadFile(filepath.Join(vm.StateDir(), "advertisement.json"))
		if err != nil {
			t.Fatal(err)
		}
		var c vm.AdvertisementConfig
		if err := json.Unmarshal(data, &c); err != nil || !c.Enabled || c.Name != "care.local" {
			t.Fatalf("window closure disabled helper state: %+v %v", c, err)
		}
	}
}

func TestCapableHelperNeverFallsBackToWindowAdvertiser(t *testing.T) {
	if !vm.Supported() {
		t.Skip("native Linux retains window ownership")
	}
	a := roleApp(t)
	home, err := os.MkdirTemp("", "mdns-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("HOME", home)
	t.Setenv("LOCALAPPDATA", home)
	a.cfg = Config{Role: roleServer, SetupDone: true, MDNSName: "care.local"}
	run := filepath.Join(vm.StateDir(), "run")
	if runtime.GOOS == "darwin" {
		run = filepath.Join(os.Getenv("HOME"), ".care-clinic", "run")
	}
	if err := os.MkdirAll(run, 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(run, "control.sock"))
	if err != nil {
		t.Fatal(err)
	}
	s := vm.HostStatus{Running: true, AdvertisementSupported: true,
		Advertisement: mdns.AdvertisementStatus{Error: "no LAN interface"}}
	var refreshes atomic.Int32
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/advertisement" {
			refreshes.Add(1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_ = json.NewEncoder(w).Encode(s)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	a.adv = mdns.NewManager(nil)
	if err := a.startAdvertise(); err == nil || err.Error() != "no LAN interface" {
		t.Fatalf("helper error hidden: %v", err)
	}
	if a.adv != nil || refreshes.Load() != 1 {
		t.Fatal("window competed with capable helper")
	}
	a.shutdown(context.Background())
	if refreshes.Load() != 1 {
		t.Fatal("window shutdown withdrew helper advertising")
	}
}

func TestSetupDoesNotAdvertiseChosenName(t *testing.T) {
	a := roleApp(t)
	a.cfg = Config{Role: roleServer}
	if err := a.SetMDNSName("care"); err != nil {
		t.Fatal(err)
	}
	if a.advRunning() || a.loadConfig().MDNSName != "care.local" {
		t.Fatal("setup must save the chosen name without claiming it on the LAN")
	}
	if err := a.startAdvertise(); err != nil || a.advRunning() {
		t.Fatalf("watcher advertised an uninstalled clinic: %v", err)
	}
}

func TestMDNSStatusRejectsInvalidName(t *testing.T) {
	a := roleApp(t)
	for _, name := range []string{"", "bad name", "care.example.com"} {
		if status := a.MDNSStatus(name); status.OK || status.Message == "" {
			t.Fatalf("invalid hostname reported ready: %+v", status)
		}

	}
}

func TestOccupiedNameBlocksSetupStatusOverLAN(t *testing.T) {
	host := os.Getenv("CARE_MDNS_CONFLICT_TEST_HOST")
	if host == "" {
		t.Skip("set CARE_MDNS_CONFLICT_TEST_HOST to an existing remote clinic hostname")
	}
	a := roleApp(t)
	a.cfg = Config{Role: roleServer, MDNSName: "different-saved-name.local"}
	status := a.MDNSStatus(host)
	if status.OK || !strings.Contains(status.Message, "already in use") {
		t.Fatalf("setup did not reject the name currently entered in the form: %+v", status)
	}
	if a.advRunning() {
		t.Fatal("checking an occupied name must not advertise it")
	}
	t.Log(status.Message)
}
