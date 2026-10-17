package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/careversions"
	"github.com/ohcnetwork/care_desktop/app/internal/clinic"
	"github.com/ohcnetwork/care_desktop/app/internal/compose"
	"github.com/ohcnetwork/care_desktop/app/internal/health"
	"github.com/ohcnetwork/care_desktop/app/internal/release"
	"github.com/ohcnetwork/care_desktop/app/internal/updates"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

var checking atomic.Bool

type CareUpdate struct {
	Backend  string `json:"backend"`
	Frontend string `json:"frontend"`
}

type CareCheck struct {
	Running bool   `json:"running"`
	Found   bool   `json:"found"`
	Error   string `json:"error,omitempty"`
}

func (a *App) CareUpdateStatus() (status clinic.ChannelStatus, err error) {
	defer a.logError(&err)
	if err := a.requireSetup(); err != nil {
		return clinic.ChannelStatus{}, err
	}
	return a.engine().ChannelStatus(), nil
}

func (a *App) CheckCareUpdate() (err error) {
	defer a.logError(&err)
	if err := a.requireSetup(); err != nil {
		return err
	}
	if !checking.CompareAndSwap(false, true) {
		return nil
	}
	go func() {
		defer checking.Store(false)
		a.checkCareUpdate()
	}()
	return nil
}

func (a *App) DismissCareUpdate() (err error) {
	defer a.logError(&err)
	return a.withJob(func() error {
		if err := a.requireStableClinic(); err != nil {
			return err
		}
		return a.engine().DeclineUpdate()
	})
}

func (a *App) checkCareUpdate() {
	if !a.updatesAllowed() {
		return
	}
	a.emit("care-check", CareCheck{Running: true})
	if a.updateCoordinator == nil {
		err := errors.New("the update coordinator is unavailable")
		a.logln("update check: " + err.Error())
		a.emit("care-check", CareCheck{Error: "CARE updates couldn't be checked. Try again, or open the log file for support."})
		return
	}
	var update compose.Update
	err := a.updateCoordinator.Check(context.Background(), func(parent context.Context) error {
		ctx, cancel := context.WithTimeout(parent, 20*time.Second)
		defer cancel()
		client := release.NewClient(a.installDir())
		manifest, err := client.Fetch(ctx, a.pins.UpdatesURL)
		if err != nil {
			return err
		}
		verified, raw, err := client.ReadVerified()
		if err != nil {
			return err
		}
		if verified.Revision != manifest.Revision {
			return fmt.Errorf("update metadata changed during check (revision %d to %d)", manifest.Revision, verified.Revision)
		}
		if err := a.updateCoordinator.SaveVerifiedManifest(manifest, raw); err != nil {
			return err
		}
		engine := a.EngineStatus()
		if engine.Supported && engine.UpdateNeeded {
			if err := a.provisioner().PrepareEngineUpdate(); err != nil {
				return fmt.Errorf("prepare the app's pinned CARE engine update: %w", err)
			}
		}
		care, err := careversions.FromRelease(manifest, a.pins)
		if err != nil {
			return err
		}
		update, err = a.engineForUpdate().CheckForUpdateManifest(care)
		return err
	})
	if err != nil {
		a.logln("update check: " + err.Error())
		a.emit("care-check", CareCheck{Error: "CARE updates couldn't be checked. Try again, or open the log file for support."})
		return
	}
	found := update.Any()
	a.emit("care-check", CareCheck{Found: found})
	if !found || !a.updatesAllowed() {
		return
	}
	a.emit("care-update", CareUpdate{Backend: update.Backend, Frontend: update.Frontend})
}

func (a *App) isClosing() bool {
	a.jobMu.RLock()
	defer a.jobMu.RUnlock()
	return a.closing
}

func (a *App) updatesAllowed() bool {
	cfg := a.loadConfig()
	if a.isClosing() || cfg.Role != roleServer || !cfg.SetupDone || cfg.Removing {
		return false
	}
	pending, err := a.engine().PendingPluginRecovery()
	return err == nil && !pending
}

func (a *App) updatesAbandoned() bool {
	cfg := a.loadConfig()
	return a.isClosing() || cfg.Role == roleClient || cfg.Removing
}

const (
	updateInterval = time.Hour
	updateWarmup   = 30 * time.Second
)

func (a *App) watchForCareUpdates() {
	for {
		if a.updatesAbandoned() {
			return
		}
		wait := updateWarmup
		if a.updatesAllowed() {
			if checking.CompareAndSwap(false, true) {
				if health.Ping().Active {
					a.checkCareUpdate()
				} else {
					a.prepareEngineUpdate()
				}
				checking.Store(false)
			}
			wait = updateInterval
		}
		select {
		case <-a.ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

func (a *App) prepareEngineUpdate() {
	engine := a.EngineStatus()
	if !engine.Supported || !engine.UpdateNeeded {
		return
	}
	if a.updateCoordinator == nil {
		a.logln("engine update preparation: the update coordinator is unavailable")
		return
	}
	err := a.updateCoordinator.Check(context.Background(), func(context.Context) error {
		return a.provisioner().PrepareEngineUpdate()
	})
	if err != nil {
		a.logln("engine update preparation: " + err.Error())
	}
}

const (
	maxDownloadBytes = 512 << 20

	downloadTimeout = 30 * time.Minute

	updateDownloadAttempts = 2
)

type AppUpdate struct {
	Current   string `json:"current"`
	Version   string `json:"version"`
	Available bool   `json:"available"`
	NotesURL  string `json:"notes_url"`
	Asset     string `json:"asset"`
	Size      int64  `json:"size"`
}

type AppUpdateProgress struct {
	Phase string `json:"phase"`
	Done  int64  `json:"done"`
	Total int64  `json:"total"`
}

const (
	phaseDownloading = "downloading"
	phaseVerifying   = "verifying"
	phaseInstalling  = "installing"
	phaseRestarting  = "restarting"
)

func (a *App) updateProgress(phase string, done, total int64) {
	a.emit("app-update-progress", AppUpdateProgress{Phase: phase, Done: done, Total: total})
}

type ghAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

func (a *App) CheckAppUpdate() (update AppUpdate, err error) {
	defer a.logError(&err)
	out := AppUpdate{Current: a.pins.AppVersion}
	if a.updateCoordinator == nil {
		return out, errors.New("the update coordinator is unavailable")
	}
	a.stagedUpdateMu.Lock()
	defer a.stagedUpdateMu.Unlock()
	err = a.updateCoordinator.Check(context.Background(), func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		manifest, err := release.NewClient(a.installDir()).Fetch(ctx, a.pins.UpdatesURL)
		if err != nil {
			return err
		}
		verified, raw, err := release.NewClient(a.installDir()).ReadVerified()
		if err != nil {
			return err
		}
		if verified.Revision != manifest.Revision {
			return fmt.Errorf("update metadata changed during check (revision %d to %d)", manifest.Revision, verified.Revision)
		}
		if err := a.updateCoordinator.SaveVerifiedManifest(manifest, raw); err != nil {
			return err
		}
		out.Version = manifest.Desktop.Version
		asset, ok := manifest.Desktop.Assets[desktopPlatformKey()]
		if !ok || desktopPlatformKey() == "" {
			return fmt.Errorf("published release %s has no installer for this computer", out.Version)
		}
		u, err := url.Parse(asset.URL)
		if err != nil {
			return fmt.Errorf("published desktop installer URL is invalid: %w", err)
		}
		out.Asset, out.Size = filepath.Base(u.Path), asset.Size
		out.Available = newerVersion(out.Current, out.Version)
		if !out.Available {
			return nil
		}
		if staged := a.stagedAppUpdates[out.Version]; staged != "" {
			if digest, err := fileSHA256(staged); err == nil && digest == asset.SHA256 {
				return nil
			}
			delete(a.stagedAppUpdates, out.Version)
		}
		a.updateProgress(phaseDownloading, 0, asset.Size)
		path, err := downloadVerified(ghAsset{Name: out.Asset, URL: asset.URL, Size: asset.Size}, asset.SHA256,
			out.Version, a.logln, func(done int64) { a.updateProgress(phaseDownloading, done, asset.Size) })
		if err != nil {
			return err
		}
		a.stagedAppUpdates[out.Version] = path
		a.logln("The CARE Clinic update is downloaded and checksum-verified; it will not install until staff choose Update.")
		return nil
	})
	return out, err
}

func (a *App) InstallAppUpdate() error {
	return a.runJob(func() error {
		return a.runCoordinatedUpdate(updates.Desktop, a.installStagedDesktopUpdate)
	}, false, "app-update")
}

func (a *App) installStagedDesktopUpdate(j *updates.Journal, checkpoint updates.Checkpoint) error {
	if developmentUpdateBuild || strings.HasSuffix(a.pins.AppVersion, "-dev") {
		return errors.New("update location unavailable: development builds cannot replace themselves; install a released copy of CARE Clinic first")
	}
	target, err := updateTarget()
	if err != nil {
		return err
	}
	manifest, _, err := release.NewClient(a.installDir()).ReadVerified()
	if err != nil {
		return err
	}
	asset, ok := manifest.Desktop.Assets[desktopPlatformKey()]
	if !ok || desktopPlatformKey() == "" {
		return fmt.Errorf("published release %s has no installer for this computer", manifest.Desktop.Version)
	}
	a.stagedUpdateMu.Lock()
	path := a.stagedAppUpdates[manifest.Desktop.Version]
	a.stagedUpdateMu.Unlock()
	if path == "" {
		return errors.New("the desktop update has not been downloaded and verified yet; check for updates again before applying it")
	}
	digest, err := fileSHA256(path)
	if err != nil {
		return fmt.Errorf("read staged desktop update: %w", err)
	}
	if digest != asset.SHA256 {
		return errors.New("the staged desktop update failed its published SHA-256 check")
	}
	version := manifest.Desktop.Version
	if !newerVersion(a.pins.AppVersion, version) {
		return errors.New("this is already the newest published version of CARE Clinic")
	}
	j.Rollback.PreviousApp = target
	j.To.Files = append(j.To.Files, updates.FileDigest{Path: path, SHA256: digest})
	if err := checkpoint(); err != nil {
		return err
	}
	return a.handoffAppUpdate(path, version, asset.SHA256)
}

func (a *App) quitAfterJob() {
	go func() {
		a.jobMu.Lock()
		a.jobMu.Unlock() //nolint:staticcheck // SA2001: waiting for the lock is the point
		wruntime.Quit(a.ctx)
	}()
}

func fetch(url string, timeout time.Duration, fn func(io.Reader) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "care-clinic")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("couldn't reach GitHub to check for updates: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub answered HTTP %d for %s", resp.StatusCode, url)
	}
	return fn(resp.Body)
}

type progressWriter struct {
	done     int64
	last     time.Time
	progress func(int64)
}

func (w *progressWriter) Write(p []byte) (int, error) {
	w.done += int64(len(p))
	if w.progress != nil && time.Since(w.last) >= 200*time.Millisecond {
		w.last = time.Now()
		w.progress(w.done)
	}
	return len(p), nil
}

func download(url, path string, progress func(int64)) (string, error) {
	var digest string
	err := fetch(url, downloadTimeout, func(body io.Reader) error {
		file, err := os.Create(path)
		if err != nil {
			return err
		}
		sum := sha256.New()
		counter := &progressWriter{progress: progress}
		written, err := io.Copy(io.MultiWriter(file, sum, counter), io.LimitReader(body, maxDownloadBytes+1))
		if progress != nil {
			progress(written)
		}
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return fmt.Errorf("the download did not finish: %w", err)
		}
		if written > maxDownloadBytes {
			return errors.New("the download is larger than any CARE Clinic installer should be")
		}
		digest = hex.EncodeToString(sum.Sum(nil))
		return nil
	})
	return digest, err
}

func downloadVerified(asset ghAsset, want, version string, log func(string), progress func(int64)) (string, error) {
	if filepath.Base(asset.Name) != asset.Name || strings.ContainsAny(asset.Name, `/\`) || asset.Name == "." || asset.Name == "" {
		return "", errors.New("the release has an unsafe installer filename")
	}
	if digest, err := hex.DecodeString(want); err != nil || len(digest) != sha256.Size {
		return "", errors.New("the release has an invalid SHA-256 checksum")
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	root := filepath.Join(cache, "CARE Clinic", "updates")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(root, "update-")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, asset.Name)
	var problem string
	for attempt := 1; attempt <= updateDownloadAttempts; attempt++ {
		log("Downloading CARE Clinic " + version + " (" + asset.Name + ")...")
		sum, err := download(asset.URL, path, progress)
		switch {
		case err != nil:
			problem = err.Error()
		case sum != want:
			problem = "its SHA-256 is " + sum + " but release " + version + " publishes " + want
		default:
			return path, nil
		}
		_ = os.Remove(path)
		if attempt < updateDownloadAttempts {
			log("The download was incomplete or damaged (" + problem + "). Trying once more...")
		}
	}
	_ = os.RemoveAll(dir)
	return "", fmt.Errorf("the CARE Clinic %s update didn't download properly and was deleted without being installed (%s). "+
		"Check this computer's internet connection and choose Update again", version, problem)
}

func newerVersion(current, candidate string) bool {
	cur, curDev := strings.CutSuffix(current, "-dev")
	cand := strings.TrimSuffix(candidate, "-dev")
	a, b := versionParts(cur), versionParts(cand)
	for i := range a {
		if a[i] != b[i] {
			return b[i] > a[i]
		}
	}
	return curDev
}

func versionParts(v string) [3]int {
	var out [3]int
	for i, part := range strings.SplitN(v, ".", 3) {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return [3]int{}
		}
		out[i] = n
	}
	return out
}
