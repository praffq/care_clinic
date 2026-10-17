package protocol

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Op is a validated operation request.
type Op interface {
	// OpName returns the registry name, e.g. OpStackUp.
	OpName() string
	// Validate returns a *ValidationError when any field is invalid.
	Validate() error
}

// Limits and fixed tokens.
const (
	MinTimeoutSec = 30
	MaxTimeoutSec = 900
	MaxTailLines  = 5000
	MaxPlugins    = 64
	MaxKeep       = 64
	MaxBuildEnv   = 128
	MaxEnvValue   = 4096

	// PurgeConfirmation must be sent in StackDown.ConfirmPurge to remove volumes.
	PurgeConfirmation = "PURGE-ALL-CLINIC-DATA"
)

var (
	// Image reference without tag: [host[:port]/]path, optional @sha256 digest.
	imageRefRE   = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*(?::[0-9]{1,5})?(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*(@sha256:[a-f0-9]{64})?$`)
	envKeyRE     = regexp.MustCompile(`^(?:REACT_[A-Z0-9_]+|CARE_[A-Z0-9_]+)$`)
	pluginNameRE = regexp.MustCompile(`^[a-z0-9_]+$`)
	pkgNameRE    = regexp.MustCompile(`^@?[A-Za-z0-9][A-Za-z0-9._-]*(?:/[A-Za-z0-9][A-Za-z0-9._-]*)?$`)
	pkgGitRE     = regexp.MustCompile(`^git\+https://[A-Za-z0-9.-]+(?:/[A-Za-z0-9._-]+)+(?:@[A-Za-z0-9._/-]+)?$`)
	versionRE    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.+_-]{0,63}$`)
	labelRE      = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)
	backupIDRE   = regexp.MustCompile(`^[A-Za-z0-9T:_-]{1,64}$`)
	usernameRE   = regexp.MustCompile(`^[A-Za-z0-9@.+_-]{1,150}$`)
	fileNameRE   = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,64}$`)
)

// StackUp starts services (all when Services is empty).
type StackUp struct {
	Services   []Service `json:"services,omitempty"`
	Wait       bool      `json:"wait,omitempty"`
	TimeoutSec int       `json:"timeout_sec,omitempty"` // 0 = agent default, else 30..900
}

func (StackUp) OpName() string { return OpStackUp }

func (o StackUp) Validate() error {
	var v validator
	checkServices(&v, o.Services)
	checkTimeout(&v, o.TimeoutSec)
	return v.err()
}

// StackStop stops services (all when Services is empty).
type StackStop struct {
	Services   []Service `json:"services,omitempty"`
	TimeoutSec int       `json:"timeout_sec,omitempty"`
}

func (StackStop) OpName() string { return OpStackStop }

func (o StackStop) Validate() error {
	var v validator
	checkServices(&v, o.Services)
	checkTimeout(&v, o.TimeoutSec)
	return v.err()
}

// StackDown removes the stack; RemoveVolumes destroys all clinic data and
// requires ConfirmPurge == PurgeConfirmation.
type StackDown struct {
	RemoveVolumes bool   `json:"remove_volumes,omitempty"`
	ConfirmPurge  string `json:"confirm_purge,omitempty"`
}

func (StackDown) OpName() string { return OpStackDown }

func (o StackDown) Validate() error {
	var v validator
	if o.RemoveVolumes {
		v.check(o.ConfirmPurge == PurgeConfirmation, "confirm_purge", "must be %q when remove_volumes is set", PurgeConfirmation)
	} else {
		v.check(o.ConfirmPurge == "", "confirm_purge", "must be empty unless remove_volumes is set")
	}
	return v.err()
}

// StackStatus reports per-service state.
type StackStatus struct{}

func (StackStatus) OpName() string  { return OpStackStatus }
func (StackStatus) Validate() error { return nil }

// StackLogs streams logs for one service.
type StackLogs struct {
	Service   Service    `json:"service"`
	TailLines int        `json:"tail_lines"` // 1..5000
	Since     *time.Time `json:"since,omitempty"`
}

func (StackLogs) OpName() string { return OpStackLogs }

func (o StackLogs) Validate() error {
	var v validator
	v.check(o.Service.Valid(), "service", "unknown service %q", o.Service)
	v.check(o.TailLines >= 1 && o.TailLines <= MaxTailLines, "tail_lines", "must be between 1 and %d", MaxTailLines)
	return v.err()
}

// RunMigrations runs the migrate service to completion.
type RunMigrations struct{}

func (RunMigrations) OpName() string  { return OpRunMigrations }
func (RunMigrations) Validate() error { return nil }

// ImagePull pulls Ref pinned to Digest. Ref carries no tag; if it embeds
// "@sha256:..." that digest must equal Digest.
type ImagePull struct {
	Role   ImageRole `json:"role"`
	Ref    string    `json:"ref"`
	Digest Digest    `json:"digest"`
}

func (ImagePull) OpName() string { return OpImagePull }

func (o ImagePull) Validate() error {
	var v validator
	checkRole(&v, o.Role)
	checkDigest(&v, "digest", o.Digest)
	if m := imageRefRE.FindStringSubmatch(o.Ref); m == nil || len(o.Ref) > 255 {
		v.add("ref", "must be a lowercase registry reference without tag, optionally @sha256 digest")
	} else if m[1] != "" && Digest(m[1][1:]) != o.Digest {
		v.add("ref", "embedded digest does not match digest")
	}
	return v.err()
}

// ImageLoad loads an image tarball (sent as the request body by the
// transport) and verifies it resolves to Digest.
type ImageLoad struct {
	Role   ImageRole `json:"role"`
	Digest Digest    `json:"digest"`
}

func (ImageLoad) OpName() string { return OpImageLoad }

func (o ImageLoad) Validate() error {
	var v validator
	checkRole(&v, o.Role)
	checkDigest(&v, "digest", o.Digest)
	return v.err()
}

// PluginSpec is one plugin to bake into a derived image.
type PluginSpec struct {
	Name    string `json:"name"`              // ^[a-z0-9_]+$
	Package string `json:"package"`           // package name or git+https URL
	Version string `json:"version,omitempty"` // optional version/ref
}

// ImageBuild builds a derived image of Kind from Base.
type ImageBuild struct {
	Kind     BuildKind         `json:"kind"`
	Base     Digest            `json:"base"`
	Plugins  []PluginSpec      `json:"plugins,omitempty"`
	BuildEnv map[string]string `json:"build_env,omitempty"` // keys ^REACT_…$ or ^CARE_…$
}

func (ImageBuild) OpName() string { return OpImageBuild }

func (o ImageBuild) Validate() error {
	var v validator
	v.check(o.Kind.Valid(), "kind", "unknown build kind %q", o.Kind)
	checkDigest(&v, "base", o.Base)
	v.check(len(o.Plugins) <= MaxPlugins, "plugins", "at most %d plugins", MaxPlugins)
	seen := map[string]bool{}
	for i, p := range o.Plugins {
		f := fmt.Sprintf("plugins[%d]", i)
		if !pluginNameRE.MatchString(p.Name) || len(p.Name) > 64 {
			v.add(f+".name", "must match ^[a-z0-9_]+$ (max 64)")
		} else if seen[p.Name] {
			v.add(f+".name", "duplicate plugin %q", p.Name)
		}
		seen[p.Name] = true
		v.check(validPackage(p.Package), f+".package", "must be a package name or git+https URL")
		v.check(p.Version == "" || versionRE.MatchString(p.Version), f+".version", "invalid version")
	}
	v.check(len(o.BuildEnv) <= MaxBuildEnv, "build_env", "at most %d entries", MaxBuildEnv)
	for _, k := range slices.Sorted(maps.Keys(o.BuildEnv)) {
		val := o.BuildEnv[k]
		f := "build_env." + k
		v.check(envKeyRE.MatchString(k), f, "key not allowed; must match ^REACT_[A-Z0-9_]+$ or ^CARE_[A-Z0-9_]+$")
		v.check(len(val) <= MaxEnvValue && !strings.ContainsAny(val, "\x00\r\n"), f, "value too long or contains control characters")
	}
	return v.err()
}

// ImagePrune removes unused images except those in Keep.
type ImagePrune struct {
	Keep []Digest `json:"keep,omitempty"`
}

func (ImagePrune) OpName() string { return OpImagePrune }

func (o ImagePrune) Validate() error {
	var v validator
	v.check(len(o.Keep) <= MaxKeep, "keep", "at most %d digests", MaxKeep)
	for i, d := range o.Keep {
		checkDigest(&v, fmt.Sprintf("keep[%d]", i), d)
	}
	return v.err()
}

// DBDump writes a database backup tagged with Label.
type DBDump struct {
	Label string `json:"label"` // ^[a-z0-9-]{1,40}$
}

func (DBDump) OpName() string { return OpDBDump }

func (o DBDump) Validate() error {
	var v validator
	v.check(labelRE.MatchString(o.Label), "label", "must match ^[a-z0-9-]{1,40}$")
	return v.err()
}

// DBRestore restores BackupID, optionally to a point in time.
type DBRestore struct {
	BackupID   string     `json:"backup_id"`
	TargetTime *time.Time `json:"target_time,omitempty"`
}

func (DBRestore) OpName() string { return OpDBRestore }

func (o DBRestore) Validate() error {
	var v validator
	v.check(backupIDRE.MatchString(o.BackupID), "backup_id", "must match ^[A-Za-z0-9T:_-]{1,64}$")
	return v.err()
}

// ManageCmd runs an allow-listed Django management command. Secrets are never
// inline: PasswordFile names a file previously uploaded to FileAreaConfig.
type ManageCmd struct {
	Command      ManageCommand `json:"command"`
	Username     string        `json:"username,omitempty"`
	PasswordFile string        `json:"password_file,omitempty"`
}

func (ManageCmd) OpName() string { return OpManage }

func (o ManageCmd) Validate() error {
	var v validator
	if !o.Command.Valid() {
		v.add("command", "unknown command %q", o.Command)
		return v.err()
	}
	if o.Command.needsCredentials() {
		v.check(usernameRE.MatchString(o.Username), "username", "must match ^[A-Za-z0-9@.+_-]{1,150}$")
		v.check(ValidFileName(o.PasswordFile), "password_file", "must be a plain file name in area %q", FileAreaConfig)
	} else {
		v.check(o.Username == "", "username", "not accepted for %s", o.Command)
		v.check(o.PasswordFile == "", "password_file", "not accepted for %s", o.Command)
	}
	return v.err()
}

// BackupRun triggers the backup service immediately.
type BackupRun struct{}

func (BackupRun) OpName() string  { return OpBackupRun }
func (BackupRun) Validate() error { return nil }

// ValidFileName reports whether name is a safe single path component for a
// FileArea: ^[a-zA-Z0-9._-]{1,64}$, excluding "." , ".." and hidden files.
func ValidFileName(name string) bool {
	return fileNameRE.MatchString(name) && !strings.HasPrefix(name, ".")
}

func validPackage(p string) bool {
	if len(p) == 0 || len(p) > 255 || strings.Contains(p, "..") {
		return false
	}
	return pkgNameRE.MatchString(p) || pkgGitRE.MatchString(p)
}

func checkServices(v *validator, svcs []Service) {
	seen := map[Service]bool{}
	for i, s := range svcs {
		f := fmt.Sprintf("services[%d]", i)
		if !s.Valid() {
			v.add(f, "unknown service %q", s)
		} else if seen[s] {
			v.add(f, "duplicate service %q", s)
		}
		seen[s] = true
	}
}

func checkTimeout(v *validator, t int) {
	v.check(t == 0 || (t >= MinTimeoutSec && t <= MaxTimeoutSec), "timeout_sec", "must be 0 (default) or between %d and %d", MinTimeoutSec, MaxTimeoutSec)
}

func checkRole(v *validator, r ImageRole) {
	v.check(r.Valid(), "role", "unknown image role %q", r)
}

func checkDigest(v *validator, field string, d Digest) {
	v.check(d.Valid(), field, "must match ^sha256:[a-f0-9]{64}$")
}
