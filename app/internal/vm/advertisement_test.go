package vm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/sys/mdns"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/netfix"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/proc"
)

func advertisementFixture(t *testing.T) *host {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("LOCALAPPDATA", root)
	return &host{
		advertiser: mdns.NewManager(nil), logf: func(string, ...any) {},
		stopReq: make(chan struct{}),
		status:  HostStatus{Running: true, AdvertisementSupported: true},
	}
}

func TestUnavailableClinicIsNotAdvertisedAfterHelperRestart(t *testing.T) {
	h := advertisementFixture(t)
	c := AdvertisementConfig{Enabled: true, Name: "care.local", InstallDir: t.TempDir()}
	if err := SaveAdvertisement(c); err != nil {
		t.Fatal(err)
	}
	if err := SetAdvertisementOwner(true); err != nil {
		t.Fatal(err)
	}
	c.HelperOwned = true
	got, err := loadAdvertisement()
	if err != nil || got != c {
		t.Fatalf("persisted state: %+v %v", got, err)
	}
	// A saved clinic name alone is insufficient: no installed compose/containers.
	h.refreshAdvertisement(context.Background())
	if h.status.Advertisement.Running {
		t.Fatal("VM boot advertised an unavailable clinic")
	}
	h.stopAdvertisement()
	next := &host{advertiser: mdns.NewManager(nil), logf: h.logf}
	next.refreshAdvertisement(context.Background())
	if next.status.Advertisement.Running {
		t.Fatal("restarted helper advertised unavailable clinic")
	}
	if err := SaveAdvertisement(AdvertisementConfig{}); err != nil {
		t.Fatal(err)
	}

	if got, err := loadAdvertisement(); err != nil || got.Enabled {
		t.Fatal("uninstall did not persist withdrawal")
	}
}

func TestAdvertisementRestartsWithPersistedClinicName(t *testing.T) {
	h := advertisementFixture(t)
	c := AdvertisementConfig{Enabled: true, Name: "care.local", InstallDir: t.TempDir()}
	if err := SaveAdvertisement(c); err != nil {
		t.Fatal(err)
	}
	if err := SetAdvertisementOwner(true); err != nil {
		t.Fatal(err)
	}

	restarted := &host{advertiser: &fakeHostAdvertisement{}, logf: h.logf}
	restarted.refreshAdvertisementWith(context.Background(),
		func(_ context.Context, got AdvertisementConfig) (bool, error) {
			if got.Name != c.Name || !got.HelperOwned || !got.Enabled {
				t.Fatalf("helper did not reload persisted advertisement settings: %+v", got)
			}
			return true, nil
		},
		func(proc.Runner) netfix.Status { return netfix.Status{OK: true} })
	if !restarted.status.Advertisement.Running || restarted.status.Advertisement.Name != "care" {
		t.Fatalf("advertisement did not resume after helper restart: %+v", restarted.status.Advertisement)
	}
}

func TestAdvertisementOwnershipSurvivesSettingsAndRequiresGrant(t *testing.T) {
	h := advertisementFixture(t)
	c := AdvertisementConfig{Enabled: true, Name: "care", InstallDir: t.TempDir()}
	if err := SaveAdvertisement(c); err != nil {
		t.Fatal(err)
	}
	h.refreshAdvertisement(context.Background())
	if h.status.Advertisement.Running {
		t.Fatal("helper advertised before the window granted ownership")
	}
	if err := SetAdvertisementOwner(true); err != nil {
		t.Fatal(err)
	}
	c.Name = "renamed"
	if err := SaveAdvertisement(c); err != nil {
		t.Fatal(err)
	}
	got, err := loadAdvertisement()
	if err != nil || !got.HelperOwned || got.Name != c.Name {
		t.Fatalf("settings lost ownership/new name: %+v %v", got, err)
	}
	if err := SetAdvertisementOwner(false); err != nil {
		t.Fatal(err)
	}
	got, err = loadAdvertisement()
	if err != nil || got.HelperOwned {
		t.Fatal("legacy-window ownership was not persisted")
	}
}

func TestAdvertisementConfigFailureIsVisibleAndRetryable(t *testing.T) {
	h := advertisementFixture(t)
	if err := SaveAdvertisement(AdvertisementConfig{Enabled: true, Name: "bad name", InstallDir: t.TempDir()}); err == nil {
		t.Fatal("invalid name accepted")
	}
	if err := SaveAdvertisement(AdvertisementConfig{Enabled: true, Name: "care", InstallDir: "relative"}); err == nil {
		t.Fatal("relative install directory accepted")
	}
	if err := os.MkdirAll(StateDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(advertisementPath(), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.refreshAdvertisement(context.Background())
	if h.status.Advertisement.Error == "" || h.status.Advertisement.Running {
		t.Fatal("invalid persisted state silently ignored")
	}
	if err := SaveAdvertisement(AdvertisementConfig{}); err != nil {
		t.Fatal(err)
	}
	h.refreshAdvertisement(context.Background())
	if h.status.Advertisement.Error != "" {
		t.Fatal("fixed persisted state did not recover")
	}
}

func TestCorruptAdvertisementCannotGrantEnabledOwnership(t *testing.T) {
	advertisementFixture(t)
	for _, data := range []string{"{", "null"} {
		if err := os.MkdirAll(StateDir(), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(advertisementPath(), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := SaveAdvertisement(AdvertisementConfig{Enabled: true, Name: "care", InstallDir: t.TempDir()}); err == nil {
			t.Fatal("corrupt state silently assigned enabled ownership")
		}
		if err := SetAdvertisementOwner(true); err == nil {
			t.Fatal("corrupt state granted ownership")
		}
		if err := SaveAdvertisement(AdvertisementConfig{HelperOwned: true}); err != nil {
			t.Fatal(err)
		}
		got, err := loadAdvertisement()
		if err != nil || got.Enabled || got.HelperOwned {
			t.Fatalf("disabled repair retained unsafe ownership: %+v %v", got, err)
		}
	}
}

func TestAdvertisementRoutesCapabilityAndWithdrawal(t *testing.T) {
	h := advertisementFixture(t)
	for _, path := range []string{"/advertisement", "/stop"} {
		response := httptest.NewRecorder()
		h.routes().ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, nil))
		if response.Code != http.StatusNoContent {
			t.Fatalf("%s: %d", path, response.Code)
		}
	}
	// A request racing shutdown cannot restart the advertiser.
	h.refreshAdvertisement(context.Background())
	if h.advertiser != nil || h.status.Advertisement.Running {
		t.Fatal("stopping helper restarted advertiser")
	}
	response := httptest.NewRecorder()
	h.routes().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/status", nil))
	var got HostStatus
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil || !got.AdvertisementSupported {
		t.Fatalf("missing capability: %+v %v", got, err)
	}
}

func TestClinicContainersMustMatchInstalledProject(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "install")
	good := "caddy|" + dir + "\nbackend|" + dir + "\nfrontend|" + dir
	if !clinicContainersAvailable(good, dir) {
		t.Fatal("installed project rejected")
	}
	for _, out := range []string{"", "caddy|" + dir, good + "-other", "caddy|/other\nbackend|/other\nfrontend|/other"} {
		if clinicContainersAvailable(out, dir) {
			t.Fatalf("wrong/incomplete project advertised: %q", out)
		}
	}
}

func TestClinicHealthProbeUsesClinicTLSAndHostWithoutDNS(t *testing.T) {
	for _, code := range []int{http.StatusOK, http.StatusServiceUnavailable} {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Host != "care.local" || r.TLS.ServerName != "care.local" || r.URL.Path != "/ping/" {
				t.Errorf("incorrect clinic probe: host %q, SNI %q, path %q", r.Host, r.TLS.ServerName, r.URL.Path)
			}
			w.WriteHeader(code)
		}))
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		err := probeClinic(ctx, server.URL+"/ping/", "care.local")
		cancel()
		server.Close()
		if (err == nil) != (code == http.StatusOK) {
			t.Fatalf("HTTP %d readiness: %v", code, err)
		}
	}
}

func TestHelperWithdrawalCancelsSlowAvailabilityBeforeItCanAdvertise(t *testing.T) {
	h := advertisementFixture(t)
	h.advertiser = &fakeHostAdvertisement{}
	if err := SaveAdvertisement(AdvertisementConfig{Enabled: true, Name: "care", InstallDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if err := SetAdvertisementOwner(true); err != nil {
		t.Fatal(err)
	}
	entered, finished := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		h.refreshAdvertisementWith(context.Background(),
			func(ctx context.Context, _ AdvertisementConfig) (bool, error) {
				close(entered)
				<-ctx.Done()
				return true, ctx.Err()
			}, func(proc.Runner) netfix.Status { return netfix.Status{OK: true} })
	}()
	<-entered
	withdrawn := make(chan struct{})
	go func() { h.withdrawAdvertisement(); close(withdrawn) }()
	select {
	case <-withdrawn:
	case <-time.After(time.Second):
		t.Fatal("withdrawal waited for health deadline")
	}
	<-finished
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.status.Advertisement.Running || h.status.Advertisement.Error != "" {
		t.Fatalf("cancelled refresh resurfaced after withdrawal: %+v", h.status.Advertisement)
	}
}

func TestHelperDiscardsChangedDesiredStateAfterSlowProbe(t *testing.T) {
	h := advertisementFixture(t)
	h.advertiser = &fakeHostAdvertisement{}
	c := AdvertisementConfig{Enabled: true, Name: "care", InstallDir: t.TempDir()}
	if err := SaveAdvertisement(c); err != nil {
		t.Fatal(err)
	}
	if err := SetAdvertisementOwner(true); err != nil {
		t.Fatal(err)
	}
	h.refreshAdvertisementWith(context.Background(),
		func(context.Context, AdvertisementConfig) (bool, error) {
			c.Enabled = false
			if err := SaveAdvertisement(c); err != nil {
				t.Fatal(err)
			}
			return true, nil
		}, func(proc.Runner) netfix.Status { return netfix.Status{OK: true} })
	if h.status.Advertisement.Running {
		t.Fatal("slow result re-enabled removed clinic")
	}
}

type fakeHostAdvertisement struct {
	stopped bool
}

func (a *fakeHostAdvertisement) Refresh(name string) mdns.AdvertisementStatus {
	return mdns.AdvertisementStatus{Name: mdns.Label(name), Running: name != "", Verified: name != ""}
}

func (a *fakeHostAdvertisement) Stop() { a.stopped = true }

func TestHostAdvertisementAvailabilityAndNetworkTransitions(t *testing.T) {
	h := advertisementFixture(t)
	manager := &fakeHostAdvertisement{}
	h.advertiser = manager
	c := AdvertisementConfig{Enabled: true, Name: "care", InstallDir: t.TempDir()}
	if err := SaveAdvertisement(c); err != nil {
		t.Fatal(err)
	}
	if err := SetAdvertisementOwner(true); err != nil {
		t.Fatal(err)
	}
	available := true
	var clinicErr error
	network := netfix.Status{Applicable: true, OK: true}
	refresh := func() {
		h.refreshAdvertisementWith(context.Background(),
			func(context.Context, AdvertisementConfig) (bool, error) { return available, clinicErr },
			func(proc.Runner) netfix.Status { return network })
	}
	refresh()
	if !h.status.Advertisement.Verified {
		t.Fatal("available clinic did not advertise")
	}
	available = false
	refresh()
	if h.status.Advertisement.Running {
		t.Fatal("stopped containers continued advertising")
	}
	available = true
	refresh()
	if !h.status.Advertisement.Running {
		t.Fatal("clinic restart did not resume advertising")
	}
	c.Name = "renamed"
	if err := SaveAdvertisement(c); err != nil {
		t.Fatal(err)
	}
	refresh()
	if h.status.Advertisement.Name != "renamed" {
		t.Fatal("settings change did not reach helper")
	}
	clinicErr = errors.New("health probe failed")
	refresh()
	if h.status.Advertisement.Running || h.status.Advertisement.Error != clinicErr.Error() {
		t.Fatal("health failure not withdrawn/reported")
	}
	clinicErr = nil
	network.OK = false
	network.Message, network.How = "public profile", "use Fix network"
	refresh()
	if !h.status.Advertisement.Running || h.status.Advertisement.Error == "" {
		t.Fatal("network warning withdrew the persistent clinic name")
	}
	network.OK = true
	refresh()
	if !h.status.Advertisement.Verified || h.status.Advertisement.Error != "" {
		t.Fatal("network repair did not recover advertisement")
	}
	c.Enabled = false
	if err := SaveAdvertisement(c); err != nil {
		t.Fatal(err)
	}
	refresh()
	if h.status.Advertisement.Running {
		t.Fatal("removing clinic continued advertising")
	}
	h.stopAdvertisement()
	refresh()
	if !manager.stopped || h.advertiser != nil || h.status.Advertisement.Running {
		t.Fatal("shutdown race reopened advertiser")
	}
}
