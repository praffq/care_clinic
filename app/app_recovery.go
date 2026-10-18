package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/stack"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/crypto/bcrypt"
)

// AdminRecoveryFile holds the admin recovery code hashes in the clinic
// state directory, next to the backup recovery key.
const AdminRecoveryFile = "admin-recovery.json"

const (
	recoveryCodeCount   = 6
	recoveryFreeTries   = 5
	recoveryMaxFailures = 15
)

// adminRecovery is AdminRecoveryFile. A used code's hash is blanked.
type adminRecovery struct {
	Hashes     [recoveryCodeCount]string `json:"hashes"`
	Path       string                    `json:"path,omitempty"`
	IssuedAt   time.Time                 `json:"issued_at,omitzero"`
	Failures   int                       `json:"failures,omitempty"`
	RetryAfter int64                     `json:"retry_after,omitempty"`
}

func (r adminRecovery) count() int {
	n := 0
	for _, h := range r.Hashes {
		if h != "" {
			n++
		}
	}
	return n
}

func (a *App) clinicStateDir() (string, error) {
	if a.stateDir != "" {
		return a.stateDir, nil
	}
	return stack.DefaultStateDir()
}

func (a *App) recoveryFile() (string, error) {
	dir, err := a.clinicStateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, AdminRecoveryFile), nil
}

// loadRecovery reads AdminRecoveryFile; a missing file means no codes.
func (a *App) loadRecovery() (adminRecovery, error) {
	var r adminRecovery
	path, err := a.recoveryFile()
	if err != nil {
		return r, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return adminRecovery{}, fmt.Errorf("%s is damaged: %w", path, err)
	}
	return r, nil
}

// saveRecovery replaces AdminRecoveryFile atomically.
func (a *App) saveRecovery(r adminRecovery) error {
	path, err := a.recoveryFile()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".admin-recovery-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	_, werr := tmp.Write(append(data, '\n'))
	if werr == nil {
		werr = tmp.Sync()
	}
	if err := errors.Join(werr, tmp.Close()); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (a *App) removeRecovery() error {
	path, err := a.recoveryFile()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// GetAdminRecoveryCodeCount is how many unused admin recovery codes remain.
func (a *App) GetAdminRecoveryCodeCount() (count int, err error) {
	defer a.logError(&err)
	if err := a.requireServer(); err != nil {
		return 0, err
	}
	r, err := a.loadRecovery()
	return r.count(), err
}

func recoveryHash(code string) string {
	normal := strings.ToUpper(strings.Join(strings.Fields(strings.ReplaceAll(code, "-", "")), ""))
	hash := sha256.Sum256([]byte(normal))
	return hex.EncodeToString(hash[:])
}

func generateAdminRecoveryCodes(clinic string) ([]byte, [recoveryCodeCount]string, error) {
	var text strings.Builder
	fmt.Fprintf(&text, "CARE Clinic admin recovery codes\nClinic: %s\nIssued: %s\n\n", clinic, time.Now().Format(time.RFC3339))
	text.WriteString("Keep this sheet secure, outside the clinic computer. You may print it.\nEach code works once. Mark a code used after resetting your password.\n\n")
	var hashes [recoveryCodeCount]string
	for i := range hashes {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, hashes, err
		}
		raw := strings.ToUpper(hex.EncodeToString(random[:]))
		groups := make([]string, 0, 8)
		for j := 0; j < len(raw); j += 4 {
			groups = append(groups, raw[j:j+4])
		}
		code := strings.Join(groups, "-")
		fmt.Fprintf(&text, "[ ] %s\n", code)
		hashes[i] = recoveryHash(code)
	}
	text.WriteString("\nIn CARE Clinic, open Advanced > Forgot CARE Clinic password?\n" +
		"Enter one unused code and choose a new CARE Clinic admin password.\n" +
		"This does NOT change your CARE web login or unlock backups.\n" +
		"To reset a CARE web login, unlock Advanced and choose Reset a CARE web password.\n" +
		"A replacement set invalidates every code on this sheet.\n")
	return []byte(text.String()), hashes, nil
}

// saveRecoveryFile never overwrites a file, even one the native save dialog
// approved.
func saveRecoveryFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("could not save the recovery file; choose a new filename: %w", err)
	}
	_, writeErr := f.Write(data)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	if err := errors.Join(writeErr, f.Close()); err != nil {
		return errors.Join(err, os.Remove(path))
	}
	return nil
}

var unsafeRecoveryFilename = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func recoveryFilename(clinic, kind, extension string) (string, error) {
	var nonce [6]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", fmt.Errorf("could not create a unique recovery filename: %w", err)
	}
	clinic = strings.Trim(unsafeRecoveryFilename.ReplaceAllString(clinic, "-"), ".-")
	if clinic == "" {
		clinic = "clinic"
	}
	return fmt.Sprintf("CARE-%s-%s-%s-%x.%s", clinic, kind,
		time.Now().UTC().Format("20060102-150405.000000000Z"), nonce, extension), nil
}

// insideDir reports whether path is dir or below it.
func insideDir(path, dir string) bool {
	path, dir = filepath.Clean(path), filepath.Clean(dir)
	if r, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
		path = filepath.Join(r, filepath.Base(path))
	}
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		dir = r
	}
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		rel, err = filepath.Rel(strings.ToLower(dir), strings.ToLower(path))
		if err != nil {
			return false
		}
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// recoveryLocation refuses folders that CARE Clinic itself manages or
// that a backup copy would take along with the clinic's data.
func (a *App) recoveryLocation(path string) error {
	protected := []string{a.installDir(), filepath.Dir(a.configPath())}
	if dir, err := a.clinicStateDir(); err == nil {
		protected = append(protected, dir)
	}
	if a.log != nil {
		protected = append(protected, a.log.Folder())
	}
	ctx, cancel := context.WithTimeout(a.clientContext(), serviceCallTimeout)
	defer cancel()
	if st, err := a.svc().Status(ctx); err == nil && st.Backups != nil && st.Backups.Dir != "" {
		protected = append(protected, st.Backups.Dir)
	}
	for _, dir := range protected {
		if dir != "" && insideDir(path, dir) {
			return fmt.Errorf("keep recovery codes outside CARE's settings, installation, logs and backup folder (%s)", dir)
		}
	}
	return nil
}

// recoverySaveDirectory is the Desktop on Windows, whose save dialog
// otherwise opens in the last folder used; macOS remembers sensibly.
func recoverySaveDirectory() string {
	if runtime.GOOS != "windows" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	if d := filepath.Join(home, "Desktop"); isDir(d) {
		return d
	}
	return home
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// SaveAdminRecoveryCodes saves six new admin recovery codes where the
// operator chooses and returns the file; "" means the dialog was cancelled.
// Every earlier code stops working once the new sheet is safely written.
func (a *App) SaveAdminRecoveryCodes(adminPassword string) (string, error) {
	return a.saveAdminRecoveryCodesWith(adminPassword, func(opts wruntime.SaveDialogOptions) (string, error) {
		return wruntime.SaveFileDialog(a.ctx, opts)
	})
}

func (a *App) saveAdminRecoveryCodesWith(adminPassword string, choose func(wruntime.SaveDialogOptions) (string, error)) (string, error) {
	var saved string
	err := a.withServerJob(func() error {
		if err := a.requireAdmin(adminPassword); err != nil {
			return err
		}
		filename, err := recoveryFilename(a.loadConfig().MDNSName, "clinic-admin-codes", "txt")
		if err != nil {
			return err
		}
		path, err := choose(wruntime.SaveDialogOptions{
			DefaultDirectory: recoverySaveDirectory(),
			Title:            "Save six CARE Clinic admin recovery codes",
			DefaultFilename:  filename,
		})
		if err != nil || path == "" {
			return err
		}
		if err := a.recoveryLocation(path); err != nil {
			return err
		}
		if err := a.saveAdminRecoveryCodes(path); err != nil {
			return err
		}
		saved = path
		return nil
	})
	return saved, err
}

func (a *App) saveAdminRecoveryCodes(path string) error {
	text, hashes, err := generateAdminRecoveryCodes(a.loadConfig().MDNSName)
	if err != nil {
		return err
	}
	if err := saveRecoveryFile(path, text); err != nil {
		return err
	}
	r := adminRecovery{Hashes: hashes, Path: path, IssuedAt: time.Now().UTC()}
	if err := a.saveRecovery(r); err != nil {
		return fmt.Errorf("could not activate the new recovery codes; keep the previous sheet and retry: %w", err)
	}
	a.emit("admin-recovery-codes-changed", r.count())
	return nil
}

// ResetAdminPassword sets a new CARE Clinic admin password with an unused
// recovery code. Wrong codes are throttled; the code is spent before the
// password changes, so a failure never leaves a code reusable.
func (a *App) ResetAdminPassword(code, newPassword string) error {
	return a.withServerJob(func() error {
		cfg := a.loadConfig()
		if !cfg.SetupDone || cfg.Removing {
			return errors.New("CARE Clinic password recovery is available only for an installed clinic")
		}
		r, err := a.loadRecovery()
		if err != nil {
			return err
		}
		now := time.Now().Unix()
		if r.RetryAfter > now {
			return fmt.Errorf("too many recovery attempts; try again in %d seconds", r.RetryAfter-now)
		}
		if err := ValidatePassword(newPassword); err != nil {
			return err
		}
		hash := recoveryHash(code)
		match := -1
		for i, candidate := range r.Hashes {
			if candidate != "" && subtle.ConstantTimeCompare([]byte(candidate), []byte(hash)) == 1 {
				match = i
			}
		}
		if match == -1 {
			if r.Failures < recoveryMaxFailures {
				r.Failures++
			}
			if r.Failures >= recoveryFreeTries {
				r.RetryAfter = now + int64(r.Failures-recoveryFreeTries+1)*60
			}
			if err := a.saveRecovery(r); err != nil {
				return err
			}
			return errors.New("that recovery code is invalid or already used; use an unused code from the latest set")
		}
		passwordHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		r.Hashes[match] = ""
		r.Failures, r.RetryAfter = 0, 0
		if err := a.saveRecovery(r); err != nil {
			return err
		}
		a.emit("admin-recovery-codes-changed", r.count())
		cfg.AdminPwHash = string(passwordHash)
		if err := a.saveConfig(cfg); err != nil {
			return fmt.Errorf("the recovery code was used but the new password wasn't saved; use another code: %w", err)
		}
		a.logln("Reset the CARE Clinic admin password with a recovery code.")
		return nil
	})
}
