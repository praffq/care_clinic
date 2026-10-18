package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/feed"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/appremoval"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/elevate"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/logdir"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/proc"
)

const macUpdateStepTimeout = 5 * time.Minute

func updateTarget() (string, error) {
	target, err := appremoval.Target()
	if err != nil {
		return "", fmt.Errorf("update location unavailable: install CARE Clinic in a permanent folder first, then open that copy and update again (%w)", err)
	}
	if runtime.GOOS == "windows" {
		target = filepath.Dir(target)
	}
	return target, nil
}

// preparedUpdate is a downloaded, verified installer ready to hand off.
type preparedUpdate struct {
	Version   string
	Installer string
	Digest    string
	Source    string
	// Work is a private folder for the helper; on macOS it holds the
	// staged bundle.
	Work         string
	Target       string
	Staged       string
	Verification string
	// Unsigned: the new app is ad-hoc signed (macOS) or the release says it
	// is unsigned (Windows).
	Unsigned bool
}

// updateHandoff is a started helper waiting for the continue file.
type updateHandoff struct {
	cmd    *exec.Cmd
	commit string
	work   string
}

// prepareAppUpdate checks that installer can replace this app: on macOS it
// unpacks the bundle and verifies its signature, version, bundle id and
// signing team (macUpdateTrust).
func prepareAppUpdate(installer, version, digest string) (p *preparedUpdate, err error) {
	target, err := updateTarget()
	if err != nil {
		return nil, err
	}
	got, err := fileSHA256(installer)
	if err != nil {
		return nil, err
	}
	if got != digest {
		return nil, fmt.Errorf("the downloaded installer's SHA-256 is %s, not the published %s", got, digest)
	}
	work, err := newUpdateWorkDir()
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(work)
		}
	}()
	p = &preparedUpdate{Version: version, Installer: installer, Digest: digest, Work: work, Target: target}
	switch runtime.GOOS {
	case "darwin":
		if p.Staged, err = stageMacBundle(installer, work, filepath.Base(target)); err != nil {
			return nil, err
		}
		if p.Unsigned, err = verifyMacBundle(p.Staged, target, version); err != nil {
			return nil, err
		}
		if p.Verification, err = macCopiedBundleVerification(p.Staged); err != nil {
			return nil, err
		}
	case "windows":
		// The helper runs the installer from its own folder, under the name
		// NSIS expects.
		p.Installer = filepath.Join(work, "CARE-Clinic-"+version+"-setup.exe")
		if err := copyFile(installer, p.Installer); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("in-place updates are not supported on %s", runtime.GOOS)
	}
	return p, nil
}

func newUpdateWorkDir() (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	root := filepath.Join(cache, "CARE Clinic", "updates")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	return os.MkdirTemp(root, "update-")
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// startHandoff starts the helper for p. The helper acknowledges readiness,
// then waits for the continue file (commitHandoff) and for this exact
// process to exit: nothing replaces the application while Wails is still
// using it.
func (a *App) startHandoff(p *preparedUpdate) error {
	ready := filepath.Join(p.Work, "ready")
	helperLog := a.updateHelperLog(p)
	var script, name string
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		elevated, err := macUpdateNeedsElevation(p.Target)
		if err != nil {
			return err
		}
		script = macUpdateHelper(p.Target, p.Staged, p.Work, helperLog, os.Getpid(), elevated, p.Verification)
		name = filepath.Join(p.Work, "install.sh")
		cmd = proc.Command("/bin/sh", name)
	case "windows":
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		exe, err = filepath.EvalSymlinks(exe)
		if err != nil {
			return err
		}
		script = windowsUpdateHelper(exe, p.Installer, p.Work, helperLog, p.Version, p.Digest, os.Getpid())
		name = filepath.Join(p.Work, "install.ps1")
		cmd = proc.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", name)
	default:
		return fmt.Errorf("in-place updates are not supported on %s", runtime.GOOS)
	}
	if err := os.WriteFile(name, []byte(script), 0o600); err != nil {
		return err
	}
	a.updateProgress(phaseInstalling, 0, 0)
	a.logln("Preparing an in-place update of " + p.Target + ". Helper log: " + helperLog)
	if err := startUpdateHelper(cmd, ready, helperLog); err != nil {
		return err
	}
	a.appUpdateMu.Lock()
	a.handoff = &updateHandoff{cmd: cmd, commit: filepath.Join(p.Work, "continue"), work: p.Work}
	a.appUpdateMu.Unlock()
	return nil
}

// commitHandoff lets the helper go ahead and quits once the job ends.
func (a *App) commitHandoff() error {
	a.appUpdateMu.Lock()
	h := a.handoff
	a.handoff = nil
	a.appUpdateMu.Unlock()
	if h == nil {
		return errors.New("the update helper is not running")
	}
	if err := os.WriteFile(h.commit, []byte("continue"), 0o600); err != nil {
		_ = h.cmd.Process.Kill()
		return fmt.Errorf("couldn't authorize the update handoff: %w", err)
	}
	a.updateProgress(phaseRestarting, 0, 0)
	a.logln("Restarting CARE Clinic to finish updating. Clinic containers and data are left alone.")
	a.closing = true
	a.quitAfterJob()
	return nil
}

// abortHandoff stops a helper that will not be committed; the running app
// stays as it is.
func (a *App) abortHandoff() {
	a.appUpdateMu.Lock()
	h := a.handoff
	a.handoff = nil
	a.appUpdateMu.Unlock()
	if h != nil {
		_ = h.cmd.Process.Kill()
	}
}

const (
	appUpdateHelperLog    = "app-update-helper.log"
	appUpdateHelperLogMax = 1 << 20
)

func tailOf(path string, n int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	if st, err := f.Stat(); err == nil && st.Size() > n {
		_, _ = f.Seek(st.Size()-n, io.SeekStart)
	}
	b, _ := io.ReadAll(io.LimitReader(f, n))
	return strings.TrimSpace(string(b))
}

func (a *App) updateHelperLog(p *preparedUpdate) string {
	dir := a.log.Folder()
	if dir == "" {
		return filepath.Join(p.Work, "helper.log")
	}
	path := filepath.Join(dir, appUpdateHelperLog)
	if st, err := os.Stat(path); err == nil && st.Size() > appUpdateHelperLogMax {
		_ = os.Rename(path, filepath.Join(dir, logdir.RotatedName(appUpdateHelperLog, 1)))
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return filepath.Join(p.Work, "helper.log")
	}
	_, _ = fmt.Fprintf(f, "\n=== CARE Clinic update to %s, %s ===\n", p.Version, time.Now().Format(time.RFC3339))
	_ = f.Close()
	return path
}

func startUpdateHelper(cmd *exec.Cmd, ready, helperLog string) error {
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("couldn't start the update helper: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			detail := tailOf(helperLog, 4<<10)
			return fmt.Errorf("the update helper stopped before it was ready (%v): %s", err, detail)
		case <-deadline.C:
			_ = cmd.Process.Kill()
			<-done
			return fmt.Errorf("the update helper did not become ready; CARE Clinic was kept open")
		case <-ticker.C:
			if _, err := os.Stat(ready); err == nil {
				return nil
			}
		}
	}
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	digest := sha256.New()
	if _, err := io.Copy(digest, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func stageMacBundle(dmg, work, name string) (staged string, err error) {
	mount := filepath.Join(work, "mnt")
	if err := os.MkdirAll(mount, 0o700); err != nil {
		return "", err
	}
	if err := macStep("/usr/bin/hdiutil", "attach", "-nobrowse", "-noautoopen", "-readonly", "-mountpoint", mount, dmg); err != nil {
		_ = macStep("/usr/bin/hdiutil", "detach", "-force", mount)
		return "", fmt.Errorf("couldn't open the downloaded disk image: %w", err)
	}
	defer func() {
		if detachErr := macStep("/usr/bin/hdiutil", "detach", "-force", mount); err == nil && detachErr != nil {
			err = fmt.Errorf("couldn't detach the update disk image: %w", detachErr)
		}
	}()
	bundles, _ := filepath.Glob(filepath.Join(mount, "*.app"))
	if len(bundles) != 1 {
		return "", fmt.Errorf("the downloaded disk image holds %d apps, expected exactly one", len(bundles))
	}
	staged = filepath.Join(work, "stage", name)
	if err := os.MkdirAll(filepath.Dir(staged), 0o700); err != nil {
		return "", err
	}
	if err := macStep("/usr/bin/ditto", bundles[0], staged); err != nil {
		return "", fmt.Errorf("couldn't copy the new version out of the disk image: %w", err)
	}
	return staged, nil
}

// verifyMacBundle checks the staged bundle against the running one and
// reports whether it is ad-hoc signed.
func verifyMacBundle(staged, current, version string) (unsigned bool, err error) {
	if err := macStep("/usr/bin/codesign", "--verify", "--deep", "--strict", staged); err != nil {
		return false, fmt.Errorf("the new version's code signature is not intact: %w", err)
	}
	got, err := plistValue(staged, "CFBundleShortVersionString")
	if err != nil {
		return false, err
	}
	if got != version {
		return false, fmt.Errorf("the downloaded app says it is version %s, not %s", got, version)
	}
	newID, err := plistValue(staged, "CFBundleIdentifier")
	if err != nil {
		return false, err
	}
	oldID, err := plistValue(current, "CFBundleIdentifier")
	if err != nil {
		return false, err
	}
	if newID != oldID {
		return false, fmt.Errorf("the downloaded app is %s, not %s", newID, oldID)
	}
	oldTeam, err := macSigningTeam(current)
	if err != nil {
		return false, err
	}
	newTeam, err := macSigningTeam(staged)
	if err != nil {
		return false, err
	}
	if err := macUpdateTrust(oldTeam, newTeam, feed.UnsignedFeedAllowed()); err != nil {
		return false, err
	}
	if newTeam != "" {
		if err := macStep("/usr/sbin/spctl", "--assess", "--type", "execute", staged); err != nil {
			return false, fmt.Errorf("macOS did not approve the signed update: %w", err)
		}
	}
	return newTeam == "", nil
}

// macUpdateTrust decides whether an app signed by newTeam may replace one
// signed by oldTeam ("" is ad-hoc). The team must match; a Developer ID
// installation never downgrades to an ad-hoc build, and ad-hoc builds
// replace each other only where this build accepts an unsigned feed.
func macUpdateTrust(oldTeam, newTeam string, unsignedAllowed bool) error {
	switch {
	case oldTeam != "" && newTeam == "":
		return fmt.Errorf("the update is not signed, but this installation is signed by team %s; it was not installed", oldTeam)
	case oldTeam != newTeam && oldTeam != "":
		return fmt.Errorf("the update is signed by team %s, not this installation's team %s; it was not installed", newTeam, oldTeam)
	case oldTeam == "" && newTeam != "":
		return fmt.Errorf("the update is signed by team %s but this installation is not signed; "+
			"install the signed release from its disk image once, then later updates install themselves", newTeam)
	case oldTeam == "" && !unsignedAllowed:
		return errors.New("the update is not signed, and this build only installs signed updates")
	}
	return nil
}

func macSigningTeam(bundle string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), macUpdateStepTimeout)
	defer cancel()
	out, err := proc.CommandContext(ctx, "/usr/bin/codesign", "-dv", "--verbose=4", bundle).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("couldn't read the app's signing identity: %w", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if team, ok := strings.CutPrefix(line, "TeamIdentifier="); ok && team != "not set" {
			return team, nil
		}
	}
	return "", nil
}

// Pin the verified Mach-O, which seals Info.plist and the resource envelope.
// Recheck after copying into the private sibling, including when that copy is
// made by an administrator from the user's writable staging directory.
func macCopiedBundleVerification(staged string) (string, error) {
	executable, err := plistValue(staged, "CFBundleExecutable")
	if err != nil {
		return "", err
	}
	if executable == "" || executable == "." || filepath.Base(executable) != executable || strings.ContainsAny(executable, `/\`) {
		return "", fmt.Errorf("the update has an unsafe bundle executable name")
	}
	digest, err := fileSHA256(filepath.Join(staged, "Contents", "MacOS", executable))
	if err != nil {
		return "", err
	}
	return `test ! -L "$slot/new.app/Contents" && test ! -L "$slot/new.app/Contents/MacOS" && test ! -L "$slot/new.app/Contents/MacOS/"` +
		elevate.ShQuote(executable) + ` && test "$(/usr/bin/shasum -a 256 "$slot/new.app/Contents/MacOS/"` + elevate.ShQuote(executable) +
		` | /usr/bin/awk '{print $1}')" = ` + elevate.ShQuote(digest) +
		` && /usr/bin/codesign --verify --deep --strict "$slot/new.app"`, nil
}

func swapScript(target, staged string, verification ...string) string {
	t := elevate.ShQuote(target)
	// A private sibling gives rename its same-filesystem atomicity. Never remove
	// a pre-existing ".previous" directory: it may be somebody's recovery copy.
	s := elevate.ShQuote(staged)
	check := ":"
	if len(verification) > 0 {
		check = verification[0]
	}
	return strings.Join([]string{
		"set -e",
		"owner=$(/usr/bin/stat -f %u:%g " + t + ")",
		"slot=" + elevate.ShQuote(filepath.Join(filepath.Dir(target), ".care-update-"+filepath.Base(filepath.Dir(filepath.Dir(staged))))),
		"/bin/mkdir -m 700 \"$slot\"",
		"if ! /usr/bin/ditto " + s + " \"$slot/new.app\"; then /bin/rm -rf \"$slot\"; exit 1; fi",
		"if ! ( " + check + " ); then /bin/rm -rf \"$slot\"; exit 1; fi",
		"/usr/sbin/chown -R \"$owner\" \"$slot/new.app\"",
		"if ! /bin/mv " + t + " \"$slot/previous.app\"; then /bin/rm -rf \"$slot\"; exit 1; fi",
		"if ! /bin/mv \"$slot/new.app\" " + t + "; then /bin/mv \"$slot/previous.app\" " + t + "; exit 1; fi",
		"/bin/rm -rf \"$slot\"",
	}, "\n")
}

func plistValue(bundle, key string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), macUpdateStepTimeout)
	defer cancel()
	out, err := proc.CommandContext(ctx, "/usr/libexec/PlistBuddy", "-c", "Print :"+key,
		filepath.Join(bundle, "Contents", "Info.plist")).Output()
	if err != nil {
		return "", fmt.Errorf("couldn't read %s from %s: %w", key, bundle, err)
	}
	return strings.TrimSpace(string(out)), nil
}

func macStep(name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), macUpdateStepTimeout)
	defer cancel()
	out, err := proc.CommandContext(ctx, name, args...).CombinedOutput()
	if msg := strings.TrimSpace(string(out)); err != nil && msg != "" {
		return fmt.Errorf("%w: %s", err, msg)
	}
	return err
}

func macUpdateNeedsElevation(target string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), macUpdateStepTimeout)
	defer cancel()
	out, err := proc.CommandContext(ctx, "/usr/bin/stat", "-f", "%u:%g", target).Output()
	if err != nil {
		return false, fmt.Errorf("couldn't read the installed app's ownership: %w", err)
	}
	groups, err := os.Getgroups()
	if err != nil {
		return false, fmt.Errorf("couldn't read the current user's groups: %w", err)
	}
	groups = append(groups, os.Getegid())
	return macReplacementNeedsElevation(canWrite(filepath.Dir(target)), strings.TrimSpace(string(out)), os.Geteuid(), groups), nil
}

func macReplacementNeedsElevation(writable bool, owner string, uid int, groups []int) bool {
	if !writable {
		return true
	}
	if uid == 0 {
		return false
	}
	// Replacing in a writable folder still requires chown to preserve the
	// bundle's owner and group. Only root can assign another user's ownership.
	for _, group := range groups {
		if owner == fmt.Sprintf("%d:%d", uid, group) {
			return false
		}
	}
	return true
}

func canWrite(dir string) bool {
	f, err := os.CreateTemp(dir, ".care-write-check-"+strconv.Itoa(os.Getpid())+"-*")
	if err != nil {
		return false
	}
	_ = f.Close()
	_ = os.Remove(f.Name())
	return true
}
