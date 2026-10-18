package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/ohcnetwork/care_desktop/app/internal/feed"
	"github.com/ohcnetwork/care_desktop/app/internal/updates"
)

const (
	releasesAPI = "https://api.github.com/repos/praffq/care_clinic/releases/latest"
	// defaultUpdatesURL is the update feed publish.yml maintains;
	// CARE_UPDATES_URL overrides it.
	defaultUpdatesURL = "https://praffq.github.io/care_clinic/updates.json"

	downloadTimeout = 30 * time.Minute

	// Where an update comes from: the signed update feed, through the update
	// coordinator (a server with the service), or the latest GitHub release.
	sourceFeed    = "feed"
	sourceRelease = "release"
)

type AppUpdate struct {
	Current   string `json:"current"`
	Version   string `json:"version"`
	Available bool   `json:"available"`
	NotesURL  string `json:"notes_url"`
	Asset     string `json:"asset"`
	Size      int64  `json:"size"`
	// Unsigned: the new version is not signed (ad-hoc on macOS) or comes
	// from an unsigned feed.
	Unsigned bool   `json:"unsigned"`
	Source   string `json:"source"`
	// Ready: it is downloaded and verified; RestartToUpdate installs it.
	Ready bool `json:"ready"`
}

type AppUpdateProgress struct {
	Phase string `json:"phase"`
	Done  int64  `json:"done"`
	Total int64  `json:"total"`
}

const (
	phaseDownloading = "downloading"
	phaseVerifying   = "verifying"
	phaseReady       = "ready"
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

type ghRelease struct {
	Tag     string    `json:"tag_name"`
	HTMLURL string    `json:"html_url"`
	Assets  []ghAsset `json:"assets"`
}

// releaseVersion is the desktop version of a release tag: desktop-vX.Y.Z,
// as release.yml creates it, or a legacy vX.Y.Z.
func releaseVersion(tag string) (string, error) {
	tag = strings.TrimSpace(tag)
	for _, prefix := range []string{"desktop-v", "v"} {
		if v, ok := strings.CutPrefix(tag, prefix); ok && v != "" {
			return v, nil
		}
	}
	return "", fmt.Errorf("the latest release %q is not a CARE Clinic desktop release", tag)
}

// foundUpdate is what CheckAppUpdate found and where to download it from.
type foundUpdate struct {
	info AppUpdate
	// release source
	asset  ghAsset
	sha256 string
}

// CheckAppUpdate looks for a newer desktop app: in the update feed when this
// computer runs the clinic service, otherwise (or while no feed is
// published) in the latest GitHub release.
func (a *App) CheckAppUpdate() (update AppUpdate, err error) {
	defer a.logError(&err)
	if !a.appUpdateChecksOn() {
		return AppUpdate{Current: a.pins.AppVersion}, errAppUpdateChecksOff
	}
	f, err := a.findAppUpdate(false)
	if err != nil {
		return f.info, err
	}
	a.appUpdateMu.Lock()
	if p := a.prepared; p != nil && p.Version == f.info.Version && p.Source == f.info.Source {
		f.info.Ready, f.info.Unsigned = true, f.info.Unsigned || p.Unsigned
	}
	a.appUpdateMu.Unlock()
	return f.info, nil
}

func (a *App) findAppUpdate(withSum bool) (foundUpdate, error) {
	f := foundUpdate{info: AppUpdate{Current: a.pins.AppVersion}}
	if c, err := a.desktopCoordinator(); err != nil {
		return f, err
	} else if c != nil {
		r, err := c.CheckPlan(a.clientContext())
		switch {
		case errors.Is(err, updates.ErrNotFound):
			a.logln("No update feed is published yet; checking the latest release instead.")
		case err != nil:
			return f, err
		default:
			f.info.Source, f.info.Unsigned = sourceFeed, r.Unsigned
			f.info.Version = f.info.Current
			for _, s := range r.Steps {
				if s.Component == feed.ComponentDesktop {
					f.info.Version, f.info.Size, f.info.Available = s.To, s.Size, true
				}
			}
			f.info.NotesURL = "https://github.com/praffq/care_clinic/releases/tag/desktop-v" + f.info.Version
			return f, nil
		}
	}
	rel, err := latestRelease()
	if err != nil {
		return f, err
	}
	f.info.Source = sourceRelease
	if f.info.Version, err = releaseVersion(rel.Tag); err != nil {
		return f, err
	}
	f.info.NotesURL = rel.HTMLURL
	asset, ok := platformAsset(rel)
	if !ok {
		return f, fmt.Errorf("release %s has no installer for this computer", f.info.Version)
	}
	f.asset = asset
	f.info.Asset, f.info.Size = asset.Name, asset.Size
	f.info.Available = newerVersion(f.info.Current, f.info.Version)
	if m, ok := findAsset(rel, func(name string) bool { return name == "release-manifest.json" }); ok {
		f.info.Unsigned = releaseUnsigned(m.URL)
	}
	if withSum {
		sums, ok := findAsset(rel, func(name string) bool { return name == "SHA256SUMS" })
		if !ok {
			return f, fmt.Errorf("release %s publishes no checksums, so its installer cannot be verified", f.info.Version)
		}
		if f.sha256, err = expectedSum(sums.URL, asset.Name); err != nil {
			return f, err
		}
	}
	return f, nil
}

// releaseUnsigned reads release-manifest.json's signing status for this
// platform; an unreadable manifest counts as signed (the bundle check on
// macOS has the final say).
func releaseUnsigned(url string) bool {
	var m struct {
		Signing struct {
			MacOS   string `json:"macos"`
			Windows string `json:"windows"`
		} `json:"signing"`
	}
	if err := fetch(url, 20*time.Second, func(body io.Reader) error {
		return json.NewDecoder(io.LimitReader(body, 1<<20)).Decode(&m)
	}); err != nil {
		return false
	}
	switch runtime.GOOS {
	case "darwin":
		return m.Signing.MacOS == "ad-hoc"
	case "windows":
		return m.Signing.Windows == "unsigned" || m.Signing.Windows == "signpath-test"
	}
	return false
}

// DownloadAppUpdate downloads the newer app (resuming an interrupted
// download), verifies it and emits the "ready" phase; RestartToUpdate then
// installs it.
func (a *App) DownloadAppUpdate() error {
	if !a.appUpdateChecksOn() {
		return a.logged(errAppUpdateChecksOff)
	}
	return a.runJob(func() error {
		f, err := a.findAppUpdate(true)
		if err != nil {
			return err
		}
		version := f.info.Version
		if !f.info.Available {
			return errors.New("this is already the newest published version of CARE Clinic")
		}
		if developmentUpdateBuild || strings.HasSuffix(a.pins.AppVersion, "-dev") {
			return errors.New("update location unavailable: development builds cannot replace themselves; install a released copy of CARE Clinic first")
		}
		if _, err := updateTarget(); err != nil {
			return err
		}
		a.updateProgress(phaseDownloading, 0, f.info.Size)
		progress := func(done, total int64) { a.updateProgress(phaseDownloading, done, total) }
		a.logln("Downloading CARE Clinic " + version + "...")
		var path, digest string
		if f.info.Source == sourceFeed {
			c, err := a.desktopCoordinator()
			if err != nil {
				return err
			}
			dl, err := c.PrepareDesktop(a.clientContext(), progress)
			if err != nil {
				return downloadFailed(version, err)
			}
			if dl.Version != version {
				return fmt.Errorf("the update feed changed while downloading (now %s); check for updates again", dl.Version)
			}
			path, digest = dl.Path, filepath.Base(dl.Path)
		} else {
			if path, err = downloadRelease(a.clientContext(), f.asset, f.sha256, progress); err != nil {
				return downloadFailed(version, err)
			}
			digest = f.sha256
		}
		a.logln("Download verified (SHA-256 " + digest + ").")
		a.updateProgress(phaseVerifying, 0, 0)
		p, err := prepareAppUpdate(path, version, digest)
		if err != nil {
			return err
		}
		p.Source, p.Unsigned = f.info.Source, p.Unsigned || f.info.Unsigned
		a.setPrepared(p)
		a.updateProgress(phaseReady, 0, 0)
		return nil
	}, false, "app-update")
}

func downloadFailed(version string, err error) error {
	return fmt.Errorf("the CARE Clinic %s update didn't download properly and nothing was installed (%w). "+
		"Check this computer's internet connection and choose Download again; it continues where it stopped", version, err)
}

// RestartToUpdate installs the downloaded update: the helper replaces the
// app after it quits and opens the new version. From the feed this is a
// journaled step of the update plan the new app finishes.
func (a *App) RestartToUpdate() error {
	return a.runJob(func() error {
		a.appUpdateMu.Lock()
		p := a.prepared
		a.appUpdateMu.Unlock()
		if p == nil {
			return errors.New("download the update first")
		}
		if p.Source == sourceFeed {
			c, err := a.desktopCoordinator()
			if err != nil {
				return err
			}
			_, err = c.Apply(a.clientContext())
			return a.afterDesktopStep(err)
		}
		if err := a.startHandoff(p); err != nil {
			return err
		}
		return a.commitHandoff()
	}, false, "app-update")
}

func (a *App) setPrepared(p *preparedUpdate) {
	a.appUpdateMu.Lock()
	old := a.prepared
	a.prepared = p
	a.appUpdateMu.Unlock()
	if old != nil && (p == nil || old.Work != p.Work) {
		_ = os.RemoveAll(old.Work)
	}
}

func (a *App) quitAfterJob() {
	go func() {
		a.jobMu.Lock()
		a.jobMu.Unlock() //nolint:staticcheck // SA2001: waiting for the lock is the point
		wruntime.Quit(a.ctx)
	}()
}

func latestRelease() (ghRelease, error) {
	var rel ghRelease
	err := fetch(releasesAPI, 20*time.Second, func(body io.Reader) error {
		if err := json.NewDecoder(io.LimitReader(body, 4<<20)).Decode(&rel); err != nil {
			return fmt.Errorf("couldn't read the release listing: %w", err)
		}
		return nil
	})
	if err != nil {
		return rel, err
	}
	if strings.TrimSpace(rel.Tag) == "" {
		return rel, errors.New("no published CARE Clinic release was found")
	}
	return rel, nil
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

func platformAsset(rel ghRelease) (ghAsset, bool) {
	switch runtime.GOOS {
	case "windows":
		return findAsset(rel, func(name string) bool { return strings.HasSuffix(name, "-setup.exe") })
	case "darwin":
		return findAsset(rel, func(name string) bool { return strings.HasSuffix(name, ".dmg") })
	}
	return ghAsset{}, false
}

func findAsset(rel ghRelease, match func(string) bool) (ghAsset, bool) {
	for _, asset := range rel.Assets {
		if match(asset.Name) && asset.URL != "" {
			return asset, true
		}
	}
	return ghAsset{}, false
}

// downloadRelease downloads a release installer into the update cache,
// resuming an interrupted download, and verifies its SHA-256.
func downloadRelease(ctx context.Context, asset ghAsset, want string, progress updates.Progress) (string, error) {
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
	dir := filepath.Join(cache, "CARE Clinic", "updates", "downloads")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	path := filepath.Join(dir, want+"-"+asset.Name)
	err = updates.FetchFile(ctx, nil, asset.URL, path, want, asset.Size, progress)
	if errors.Is(err, updates.ErrChecksum) {
		// The damaged copy is gone; one fresh attempt.
		err = updates.FetchFile(ctx, nil, asset.URL, path, want, asset.Size, progress)
	}
	return path, err
}

func expectedSum(url, name string) (string, error) {
	var want string
	err := fetch(url, 2*time.Minute, func(body io.Reader) error {
		data, err := io.ReadAll(io.LimitReader(body, 1<<20))
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(data), "\n") {
			sum, file, ok := strings.Cut(strings.TrimSpace(line), "  ")
			if ok && file == name {
				want = strings.ToLower(sum)
				return nil
			}
		}
		return fmt.Errorf("the published checksums do not cover %s", name)
	})
	return want, err
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
