package release

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/compose-spec/compose-go/v2/dotenv"
	"github.com/pelletier/go-toml/v2"
)

// ConfigFile is the repository-root release file. It is the only
// human-edited source of release versions, image tags and tool pins.
const ConfigFile = "versions.toml"

// ConfigSchema is the versions.toml schema this build understands.
const ConfigSchema = 1

type Config struct {
	Schema        int               `toml:"schema"`
	Desktop       DesktopConfig     `toml:"desktop"`
	Engine        EngineConfig      `toml:"engine"`
	Care          CareConfig        `toml:"care"`
	SupportImages map[string]string `toml:"support_images"`
	Caddy         CaddyConfig       `toml:"caddy"`
	Tools         map[string]Tool   `toml:"tools"`
}

type DesktopConfig struct {
	Version        string `toml:"version"`
	RequiresEngine string `toml:"requires_engine"`
	// UpdatesURL is where the signed release manifest is published.
	UpdatesURL string `toml:"updates_url"`
}

type EngineConfig struct {
	Version         string `toml:"version"`
	RequiresDesktop string `toml:"requires_desktop"`
}

type CareConfig struct {
	Version         string `toml:"version"`
	RequiresDesktop string `toml:"requires_desktop"`
	RequiresEngine  string `toml:"requires_engine"`
	Backend         Source `toml:"backend"`
	Frontend        Source `toml:"frontend"`
}

// Source is a GitHub repository ("owner/name") and the branch CI builds.
type Source struct {
	Repo   string `toml:"repo"`
	Branch string `toml:"branch"`
}

type CaddyConfig struct {
	// Coraza is the coraza-caddy module version built into the proxy.
	Coraza string `toml:"coraza"`
}

// Tool is one pinned download. Version is required for tools whose version
// the desktop app derives download names from, and must be omitted otherwise.
type Tool struct {
	Version string `toml:"version"`
	URL     string `toml:"url"`
	SHA256  string `toml:"sha256"`
}

// Support image keys (the compose variable each one stages into).
var supportImageEnv = map[string]string{
	"postgres": "POSTGRES_IMAGE",
	"redis":    "REDIS_IMAGE",
	"caddy":    "CADDY_IMAGE",
	"silo":     "MINIO_IMAGE",
}

const (
	dockerStatic = "https://download.docker.com/"
)

type toolSpec struct {
	shaEnv string
	// group names tools that must share one version; "" means unversioned.
	group string
	// url derives the exact download URL from the version, matching how the
	// desktop app builds it. nil means any pinned HTTPS URL.
	url func(version string) string
}

var toolSpecs = map[string]toolSpec{
	"docker_linux_x86_64":  {"DOCKER_LINUX_X86_64_SHA256", "docker_linux", staticDocker("linux", "x86_64", ".tgz")},
	"docker_linux_aarch64": {"DOCKER_LINUX_AARCH64_SHA256", "docker_linux", staticDocker("linux", "aarch64", ".tgz")},

	// The engine image URL is checked by stageEngine: its directory becomes
	// CARE_VM_BASE_URL and its file name is derived from the engine version.
	"care_engine_aarch64": {"CARE_VM_AARCH64_SHA256", "engine", nil},
	"care_engine_x86_64":  {"CARE_VM_X86_64_SHA256", "engine", nil},

	"qemu_windows_x86_64":               {"QEMU_WINDOWS_SHA256", "qemu", nil},
	"sevenzip_bootstrap_windows_x86_64": {"SEVENZIP_BOOTSTRAP_SHA256", "", nil},
	"sevenzip_windows_x86_64":           {"SEVENZIP_WINDOWS_SHA256", "", nil},
}

var toolURLEnv = map[string]string{
	"qemu_windows_x86_64":               "QEMU_WINDOWS_URL",
	"sevenzip_bootstrap_windows_x86_64": "SEVENZIP_BOOTSTRAP_URL",
	"sevenzip_windows_x86_64":           "SEVENZIP_WINDOWS_URL",
}

var groupVersionEnv = map[string]string{
	"docker_linux": "DOCKER_LINUX_VERSION",
	"engine":       "CARE_VM_VERSION",
	"qemu":         "QEMU_WINDOWS_VERSION",
}

var engineArch = map[string]string{"care_engine_aarch64": "arm64", "care_engine_x86_64": "amd64"}

func staticDocker(os, arch, ext string) func(string) string {
	return func(v string) string { return dockerStatic + os + "/static/stable/" + arch + "/docker-" + v + ext }
}

var (
	repoPattern   = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})/[A-Za-z0-9._-]{1,100}$`)
	branchPattern = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,200}$`)
	imageName     = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*(?::[0-9]+)?(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*$`)
	imageTag      = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`)
	goModVersion  = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
)

// ReadConfig strictly parses and validates versions.toml: unknown or missing
// fields, malformed versions and requirements, unpinned images and unpinned or
// non-HTTPS downloads are all errors.
func ReadConfig(data []byte) (Config, error) {
	var c Config
	dec := toml.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		var strict *toml.StrictMissingError
		if errors.As(err, &strict) {
			return Config{}, fmt.Errorf("%s has unknown fields:\n%s", ConfigFile, strict.String())
		}
		return Config{}, fmt.Errorf("%s is malformed: %w", ConfigFile, err)
	}
	if err := c.validate(); err != nil {
		return Config{}, fmt.Errorf("%s: %w", ConfigFile, err)
	}
	return c, nil
}

func (c *Config) validate() error {
	if c.Schema != ConfigSchema {
		return fmt.Errorf("schema must be %d", ConfigSchema)
	}
	desktop, err := releaseVersion("desktop.version", c.Desktop.Version, true)
	if err != nil {
		return err
	}
	engine, err := releaseVersion("engine.version", c.Engine.Version, false)
	if err != nil {
		return err
	}
	if _, err := releaseVersion("care.version", c.Care.Version, false); err != nil {
		return err
	}
	if err := httpsURL("desktop.updates_url", c.Desktop.UpdatesURL); err != nil {
		return err
	}
	for _, r := range []struct {
		field, value string
		target       *semver.Version
		name         string
	}{
		{"desktop.requires_engine", c.Desktop.RequiresEngine, engine, "engine.version"},
		{"engine.requires_desktop", c.Engine.RequiresDesktop, desktop, "desktop.version"},
		{"care.requires_desktop", c.Care.RequiresDesktop, desktop, "desktop.version"},
		{"care.requires_engine", c.Care.RequiresEngine, engine, "engine.version"},
	} {
		if err := requirement(r.field, r.value, r.target, r.name); err != nil {
			return err
		}
	}
	for field, s := range map[string]Source{"care.backend": c.Care.Backend, "care.frontend": c.Care.Frontend} {
		if !repoPattern.MatchString(s.Repo) {
			return fmt.Errorf("%s.repo must be a GitHub owner/name", field)
		}
		if !validBranch(s.Branch) {
			return fmt.Errorf("%s.branch is not a valid branch name", field)
		}
	}
	if err := exactKeys("support_images", keys(c.SupportImages), keys(supportImageEnv)); err != nil {
		return err
	}
	for _, k := range keys(c.SupportImages) {
		if err := pinnedImage(c.SupportImages[k]); err != nil {
			return fmt.Errorf("support_images.%s %w", k, err)
		}
	}
	if !goModVersion.MatchString(c.Caddy.Coraza) {
		return fmt.Errorf("caddy.coraza must be vX.Y.Z")
	}
	if err := exactKeys("tools", keys(c.Tools), keys(toolSpecs)); err != nil {
		return err
	}
	groups := map[string]string{"engine": c.Engine.Version}
	for _, k := range keys(c.Tools) {
		t, spec := c.Tools[k], toolSpecs[k]
		field := "tools." + k
		if !sha256Pattern.MatchString(t.SHA256) {
			return fmt.Errorf("%s.sha256 must be 64 lowercase hex characters", field)
		}
		if err := httpsURL(field+".url", t.URL); err != nil {
			return err
		}
		if spec.group == "" {
			if t.Version != "" {
				return fmt.Errorf("%s.version is not used; remove it", field)
			}
			continue
		}
		if _, err := releaseVersion(field+".version", t.Version, false); err != nil {
			return err
		}
		if want, ok := groups[spec.group]; ok && want != t.Version {
			return fmt.Errorf("%s.version %s must match %s", field, t.Version, want)
		}
		groups[spec.group] = t.Version
		if spec.url != nil && t.URL != spec.url(t.Version) {
			return fmt.Errorf("%s.url must be %s", field, spec.url(t.Version))
		}
		if arch, ok := engineArch[k]; ok && path.Base(t.URL) != "care-engine-"+t.Version+"-linux-"+arch+".tar.gz" {
			return fmt.Errorf("%s.url must name care-engine-%s-linux-%s.tar.gz", field, t.Version, arch)
		}
	}
	if engineBase(c.Tools["care_engine_aarch64"].URL) != engineBase(c.Tools["care_engine_x86_64"].URL) {
		return fmt.Errorf("tools.care_engine_* must be published under the same URL")
	}
	for k := range toolURLEnv {
		if !strings.HasSuffix(strings.ToLower(c.Tools[k].URL), ".exe") {
			return fmt.Errorf("tools.%s.url must name an .exe", k)
		}
	}
	return nil
}

// releaseVersion accepts strict X.Y.Z (no "v", no leading zeros, no build
// metadata); dev additionally allows the X.Y.Z-dev development marker.
func releaseVersion(field, value string, dev bool) (*semver.Version, error) {
	v, err := semver.StrictNewVersion(value)
	if err != nil || v.Metadata() != "" || (v.Prerelease() != "" && (!dev || v.Prerelease() != "dev")) {
		if dev {
			return nil, fmt.Errorf("%s must be X.Y.Z or X.Y.Z-dev", field)
		}
		return nil, fmt.Errorf("%s must be X.Y.Z", field)
	}
	core, _ := v.SetPrerelease("")
	return &core, nil
}

// requirement parses a semver constraint whose bounds are all strict X.Y.Z
// and checks it admits the version pinned alongside it.
func requirement(field, value string, target *semver.Version, name string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", field)
	}
	if strings.Contains(value, "||") {
		return fmt.Errorf("%s must be a single range (no ||)", field)
	}
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		bound := strings.TrimLeft(part, "<>=!")
		op := part[:len(part)-len(bound)]
		switch op {
		case ">=", ">", "<", "<=", "=":
		default:
			return fmt.Errorf("%s: %q must use one of >=, >, <, <=, = with an X.Y.Z bound", field, part)
		}
		if _, err := releaseVersion(field, strings.TrimSpace(bound), false); err != nil {
			return fmt.Errorf("%s: bound %q must be X.Y.Z", field, bound)
		}
	}
	constraint, err := semver.NewConstraint(value)
	if err != nil {
		return fmt.Errorf("%s is not a valid requirement: %w", field, err)
	}
	if !constraint.Check(target) {
		return fmt.Errorf("%s %q excludes %s %s", field, value, name, target)
	}
	return nil
}

func validBranch(b string) bool {
	if !branchPattern.MatchString(b) || strings.HasPrefix(b, "-") || strings.HasPrefix(b, "/") ||
		strings.HasSuffix(b, "/") || strings.HasSuffix(b, ".") || strings.HasSuffix(b, ".lock") ||
		strings.Contains(b, "..") || strings.Contains(b, "//") || strings.Contains(b, "/.") || strings.HasPrefix(b, ".") {
		return false
	}
	return true
}

// pinnedImage requires an explicit, non-floating tag; a digest may follow.
func pinnedImage(ref string) error {
	name, digest, hasDigest := strings.Cut(ref, "@")
	if hasDigest && !strings.HasPrefix(digest, "sha256:") || hasDigest && !sha256Pattern.MatchString(strings.TrimPrefix(digest, "sha256:")) {
		return fmt.Errorf("has an invalid digest")
	}
	i := strings.LastIndexByte(name, ':')
	if i < 0 || strings.Contains(name[i:], "/") {
		return fmt.Errorf("must be pinned to an explicit tag")
	}
	repo, tag := name[:i], name[i+1:]
	if !imageName.MatchString(repo) || !imageTag.MatchString(tag) {
		return fmt.Errorf("is not a valid image reference")
	}
	if tag == "latest" {
		return fmt.Errorf("must not use the floating latest tag")
	}
	return nil
}

func httpsURL(field, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" ||
		u.RawQuery != "" || u.Path == "" || u.Path == "/" {
		return fmt.Errorf("%s must be an https URL", field)
	}
	return nil
}

func engineBase(raw string) string { return raw[:max(strings.LastIndexByte(raw, '/'), 0)] }

func exactKeys(field string, got, want []string) error {
	wanted := map[string]bool{}
	for _, k := range want {
		wanted[k] = true
	}
	for _, k := range got {
		if !wanted[k] {
			return fmt.Errorf("%s.%s is not a known entry", field, k)
		}
		delete(wanted, k)
	}
	if len(wanted) > 0 {
		return fmt.Errorf("%s is missing: %s", field, strings.Join(keys(wanted), ", "))
	}
	return nil
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ReleaseEnv renders the release pins as the .env variables the desktop app
// and Compose read. Keys are sorted for a stable, reviewable output.
func (c Config) ReleaseEnv() map[string]string {
	env := map[string]string{
		"CARE_CLINIC_VERSION": c.Desktop.Version,
		"CARE_UPDATES_URL":    c.Desktop.UpdatesURL,
		"CARE_VERSION":        c.Care.Version,
		"CORAZA_VERSION":      c.Caddy.Coraza,
		"CARE_VM_BASE_URL":    engineBase(c.Tools["care_engine_aarch64"].URL),
	}
	for k, key := range supportImageEnv {
		env[key] = c.SupportImages[k]
	}
	for k, spec := range toolSpecs {
		t := c.Tools[k]
		env[spec.shaEnv] = t.SHA256
		if key, ok := groupVersionEnv[spec.group]; ok {
			env[key] = t.Version
		}
		if key, ok := toolURLEnv[k]; ok {
			env[key] = t.URL
		}
	}
	return env
}

// releaseEnvKeys lists every variable that must come from versions.toml, plus
// retired release variables, none of which deployments/.env may define.
func releaseEnvKeys() map[string]bool {
	out := map[string]bool{
		"CARE_LINES_URL": true, "CARE_DEFAULT_LINE": true,
		"CADDY_WAF_PREBUILT": true, "BACKUP_PREBUILT": true,
		"DOCKER_CLI_VERSION":              true,
		"DOCKER_CLI_MACOS_AARCH64_SHA256": true, "DOCKER_CLI_MACOS_X86_64_SHA256": true,
		"DOCKER_CLI_WINDOWS_X86_64_SHA256": true,
		"COMPOSE_MACOS_AARCH64_SHA256":     true, "COMPOSE_MACOS_X86_64_SHA256": true,
		"COMPOSE_WINDOWS_X86_64_SHA256": true, "COMPOSE_LINUX_VERSION": true,
		"COMPOSE_LINUX_X86_64_SHA256": true, "COMPOSE_LINUX_AARCH64_SHA256": true,
	}
	for k := range (Config{Tools: map[string]Tool{}, SupportImages: map[string]string{}}).ReleaseEnv() {
		out[k] = true
	}
	return out
}

// StageEnv validates versions.toml and appends its pins to the repository's
// non-release defaults (deployments/.env), producing the .env embedded in the
// app and loaded by Compose. A release value left in deployments/.env is an
// error rather than a silent override or fallback.
func StageEnv(config, baseEnv []byte) ([]byte, error) {
	c, err := ReadConfig(config)
	if err != nil {
		return nil, err
	}
	base, err := dotenv.Parse(bytes.NewReader(baseEnv))
	if err != nil {
		return nil, fmt.Errorf("deployments/%s is malformed: %w", EnvFile, err)
	}
	reserved := releaseEnvKeys()
	var stray []string
	for k := range base {
		if reserved[k] {
			stray = append(stray, k)
		}
	}
	if len(stray) > 0 {
		sort.Strings(stray)
		return nil, fmt.Errorf("deployments/%s must not define release values (they belong in %s): %s",
			EnvFile, ConfigFile, strings.Join(stray, ", "))
	}
	env := c.ReleaseEnv()
	var out bytes.Buffer
	out.Write(baseEnv)
	if len(baseEnv) > 0 && !bytes.HasSuffix(baseEnv, []byte("\n")) {
		out.WriteByte('\n')
	}
	out.WriteString("\n# --- Release pins: generated from " + ConfigFile + "; do not edit ---\n")
	for _, k := range keys(env) {
		out.WriteString(k + "=" + env[k] + "\n")
	}
	return out.Bytes(), nil
}

// LoadSource loads the pins a build would embed, straight from the repository's
// versions.toml and deployments/.env.
func LoadSource(config, baseEnv []byte) (*Pins, error) {
	env, err := StageEnv(config, baseEnv)
	if err != nil {
		return nil, err
	}
	return Load(env)
}
