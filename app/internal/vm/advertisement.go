package vm

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/sys/atomicfile"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/mdns"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/netfix"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/proc"
)

// AdvertisementConfig persists across app exits and helper updates/rollbacks.
type AdvertisementConfig struct {
	Enabled     bool   `json:"enabled"`
	Name        string `json:"name"`
	InstallDir  string `json:"install_dir"`
	HelperOwned bool   `json:"helper_owned"`
}

func advertisementPath() string { return filepath.Join(StateDir(), "advertisement.json") }

var advertisementConfigMu sync.Mutex

type advertisementManager interface {
	Refresh(string) mdns.AdvertisementStatus
	Stop()
}

func (c AdvertisementConfig) validate() error {
	if !c.Enabled {
		return nil
	}
	if err := mdns.ValidateLabel(c.Name); err != nil {
		return err
	}
	if !filepath.IsAbs(c.InstallDir) {
		return errors.New("the advertised clinic needs an absolute installation directory")
	}
	return nil
}

func SaveAdvertisement(c AdvertisementConfig) error {
	advertisementConfigMu.Lock()
	defer advertisementConfigMu.Unlock()
	if previous, err := loadAdvertisement(); err == nil {
		c.HelperOwned = previous.HelperOwned
	} else {
		if c.Enabled {
			return fmt.Errorf("could not read persistent advertisement ownership: %w", err)
		}
		c.HelperOwned = false
	}
	return writeAdvertisement(c)
}

// SetAdvertisementOwner grants ownership only after the window withdraws its
// responder. Keeping this separate from settings saves prevents takeover races.
func SetAdvertisementOwner(helper bool) error {
	advertisementConfigMu.Lock()
	defer advertisementConfigMu.Unlock()
	c, err := loadAdvertisement()
	if err != nil {
		return err
	}
	c.HelperOwned = helper
	return writeAdvertisement(c)
}

func writeAdvertisement(c AdvertisementConfig) error {
	if err := c.validate(); err != nil {
		return err
	}
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if previous, err := os.ReadFile(advertisementPath()); err == nil && bytes.Equal(previous, b) {
		return nil
	}
	return atomicfile.WritePrivate(advertisementPath(), b)
}

func loadAdvertisement() (AdvertisementConfig, error) {
	var c *AdvertisementConfig
	b, err := os.ReadFile(advertisementPath())
	if os.IsNotExist(err) {
		return AdvertisementConfig{}, nil
	}
	if err != nil {
		return AdvertisementConfig{}, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return AdvertisementConfig{}, err
	}
	if c == nil {
		return AdvertisementConfig{}, errors.New("persistent clinic advertising settings must be a JSON object")
	}
	return *c, c.validate()
}

// RefreshAdvertisement is supported only by helpers advertising that capability.
// Older helpers must remain window-owned; do not infer ownership from VM readiness.
func RefreshAdvertisement(ctx context.Context) error {
	return newClient().do(ctx, http.MethodPost, "/advertisement", nil, nil)
}

func WithdrawAdvertisement(ctx context.Context) error {
	return newClient().do(ctx, http.MethodPost, "/advertisement/withdraw", nil, nil)
}

func (h *host) cancelAdvertisement() {
	h.advertiseCancelMu.Lock()
	defer h.advertiseCancelMu.Unlock()
	h.advertiseWithdrawing = true
	if h.advertiseCancel != nil {
		h.advertiseCancel()
	}
}

func (h *host) withdrawAdvertisement() {
	h.cancelAdvertisement()
	h.advertiseMu.Lock()
	defer h.advertiseMu.Unlock()
	if h.advertiser != nil {
		h.advertiser.Refresh("")
	}
	h.mu.Lock()
	h.status.Advertisement = mdns.AdvertisementStatus{}
	h.mu.Unlock()
	h.advertiseCancelMu.Lock()
	h.advertiseWithdrawing = false
	h.advertiseCancelMu.Unlock()
}

func (h *host) stopAdvertisement() {
	h.cancelAdvertisement()
	h.advertiseMu.Lock()
	defer h.advertiseMu.Unlock()
	if h.advertiser != nil {
		h.advertiser.Stop()
		h.advertiser = nil
	}
	h.mu.Lock()
	h.status.Advertisement.Running = false
	h.status.Advertisement.Verified = false
	h.mu.Unlock()
}

func (h *host) refreshAdvertisement(ctx context.Context) {
	h.refreshAdvertisementWith(ctx, h.clinicAvailable, netfix.Check)
}

func (h *host) refreshAdvertisementWith(ctx context.Context,
	availableClinic func(context.Context, AdvertisementConfig) (bool, error),
	checkNetwork func(proc.Runner) netfix.Status,
) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	h.advertiseMu.Lock()
	defer h.advertiseMu.Unlock()
	if ctx.Err() != nil {
		return
	}
	if h.advertiser == nil {
		return
	}
	h.advertiseCancelMu.Lock()
	if h.advertiseWithdrawing {
		h.advertiseCancelMu.Unlock()
		return
	}
	h.advertiseCancel = cancel
	h.advertiseCancelMu.Unlock()
	defer func() {
		h.advertiseCancelMu.Lock()
		h.advertiseCancel = nil
		h.advertiseCancelMu.Unlock()
	}()
	c, err := loadAdvertisement()
	name := ""
	if err == nil && c.Enabled && c.HelperOwned {
		var available bool
		available, err = availableClinic(ctx, c)
		if available && err == nil {
			name = c.Name
			status := checkNetwork(proc.Runner{Ctx: ctx})
			if status.Applicable && !status.OK {
				err = fmt.Errorf("persistent clinic advertising needs network repair: %s; %s", status.Message, status.How)
			}
		}
	}
	// Settings/removal may have changed while Docker or health was checked.
	latest, latestErr := loadAdvertisement()
	if ctx.Err() != nil || latestErr != nil || latest != c {
		name = ""
	}
	var s mdns.AdvertisementStatus
	if manager, ok := h.advertiser.(*mdns.Manager); ok {
		s = manager.RefreshContext(ctx, name)
	} else {
		s = h.advertiser.Refresh(name)
	}
	latest, latestErr = loadAdvertisement()
	if ctx.Err() != nil || latestErr != nil || latest != c {
		s = h.advertiser.Refresh("")
	}
	if err != nil && ctx.Err() == nil {
		s.Error = err.Error()
	}
	h.mu.Lock()
	previous := h.status.Advertisement.Error
	h.status.Advertisement = s
	h.mu.Unlock()
	if s.Error != "" && s.Error != previous {
		h.logf("mDNS: %s", s.Error)
	}
}

func (h *host) clinicAvailable(ctx context.Context, c AdvertisementConfig) (bool, error) {
	fi, err := os.Stat(filepath.Join(c.InstallDir, "docker-compose.yml"))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !fi.Mode().IsRegular() {
		return false, errors.New("the advertised clinic compose file is not a regular file")
	}
	h.mu.Lock()
	ready := h.status.Docker
	h.mu.Unlock()
	if !ready {
		return false, nil
	}
	env := append(os.Environ(), Env()...)
	run := proc.Runner{Ctx: ctx, Env: env}
	// Inspect the actual daemon, not a public port that another process could own.
	out, err := run.Capture(filepath.Join(BinDir(), exe("docker")), "ps",
		"--filter", "label=com.docker.compose.project=care-clinic",
		"--format", `{{.Label "com.docker.compose.service"}}|{{.Label "com.docker.compose.project.working_dir"}}`)
	if err != nil {
		return false, fmt.Errorf("could not check the advertised clinic containers: %w", err)
	}
	if !clinicContainersAvailable(out, c.InstallDir) {
		return false, nil
	}
	if err := probeClinic(ctx, "https://127.0.0.1/ping/", mdns.Label(c.Name)+".local"); err != nil {
		return false, err
	}
	return true, nil
}

func probeClinic(ctx context.Context, url, host string) error {
	// Confirm CARE rather than just a booted VM or a listening proxy.
	transport := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true, ServerName: host}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Host = host
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("the advertised clinic health probe failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the advertised clinic health probe returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func clinicContainersAvailable(out, install string) bool {
	services := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		service, dir, ok := strings.Cut(line, "|")
		if ok && (filepath.Clean(dir) == filepath.Clean(install) || dir == GuestPath(install)) {
			services[service] = true
		}
	}
	return services["caddy"] && services["backend"] && services["frontend"]
}
