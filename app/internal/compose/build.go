package compose

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"github.com/compose-spec/compose-go/v2/dotenv"
	"github.com/ohcnetwork/care_desktop/app/internal/careversions"
	"github.com/ohcnetwork/care_desktop/app/internal/envconfig"
	"github.com/ohcnetwork/care_desktop/app/internal/plugins"
	"github.com/ohcnetwork/care_desktop/app/internal/release"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/proc"
)

const (
	backendPluginsDockerfile  = "backend-plugins.Dockerfile"
	frontendRebuildDockerfile = "frontend-rebuild.Dockerfile"
)

type Builder struct {
	Log  func(string)
	Stop func() bool

	dir string // install dir: holds the lock, manifest cache and Dockerfiles
	set *release.Pins
	run proc.Runner

	pending bool
	staged  map[string]Release
}

func NewBuilder(run proc.Runner, dir string, set *release.Pins, log func(string)) *Builder {
	return &Builder{Log: log, dir: dir, set: set, run: run}
}

// Pending returns a builder for the "-next" images of a staged update.
func (b *Builder) Pending() *Builder {
	next := NewBuilder(b.run, b.dir, b.set, b.Log)
	next.pending = true
	next.Stop = b.Stop
	return next
}

// staging returns a Pending builder that builds the given releases.
func (b *Builder) staging(releases map[string]Release) *Builder {
	next := b.Pending()
	next.staged = releases
	return next
}

func (b *Builder) suffixed(name string) string {
	if b.pending {
		return name + "-next"
	}
	return name
}

func (b *Builder) stopped() bool { return b.Stop != nil && b.Stop() }

func (b *Builder) logln(s string) {
	if b.Log != nil {
		b.Log(s)
	}
}

// target picks the release a service should run, and the version it came from
// when it was not already recorded in the lock.
func (b *Builder) target(service string) (Release, string, error) {
	if rel, ok := b.staged[service]; ok {
		return rel, "", nil
	}
	c := ReadLock(b.dir).Get(service)
	if b.pending {
		if c.Next == nil {
			return Release{}, "", fmt.Errorf("no CARE %s update is staged", service)
		}
		return *c.Next, "", nil
	}
	if c.Current.Image != "" {
		return c.Current, "", nil
	}
	m, err := careversions.Load(b.dir, b.set)
	if err != nil {
		return Release{}, "", fmt.Errorf("couldn't load CARE image metadata: %w", err)
	}
	if service == Frontend {
		return m.Frontend, m.Version, nil
	}
	return m.Backend, m.Version, nil
}

func (b *Builder) recordCurrent(service string, rel Release, version string) error {
	if b.pending {
		return nil
	}
	err := b.updateLock(func(l *Lock) {
		c := l.Get(service)
		c.Current = rel
		if c.Next != nil && c.Next.Image == rel.Image {
			c.Next, c.Declined = nil, ""
		}
		l.Set(service, c)
		if l.Version == "" && version != "" {
			l.Version = version
		}
	})
	if err == nil {
		removeLegacySource(b.dir)
	}
	return err
}

// BackendRef is the CARE backend commit this builder targets.
func (b *Builder) BackendRef() string {
	rel, _, _ := b.target(Backend)
	return rel.Commit
}

// FrontendRef is the CARE frontend commit this builder targets.
func (b *Builder) FrontendRef() string {
	rel, _, _ := b.target(Frontend)
	return rel.Commit
}

type plan struct {
	service string
	rel     Release
	version string
	tag     string
	key     string
	build   func() error
}

func (b *Builder) backendPlan() (plan, error) {
	plugs, err := plugins.New(b.dir).AdditionalPlugs()
	if err != nil {
		return plan{}, err
	}
	rel, version, err := b.target(Backend)
	if err != nil {
		return plan{}, err
	}
	p := plan{service: Backend, rel: rel, version: version, tag: b.suffixed(b.set.BackendImage), key: rel.Image}
	if plugs == "" {
		p.build = func() error { return b.relabel(rel.Image, p.tag, p.key, "backend") }
		return p, nil
	}
	p.key += "+plugs@" + shortHash([]byte(plugs))
	p.build = func() error { return b.buildBackendPlugins(rel, plugs, p.tag, p.key) }
	return p, nil
}

func (b *Builder) buildBackendPlugins(rel Release, plugs, tag, key string) error {
	if err := b.pull(rel.Builder); err != nil {
		return err
	}
	if err := b.pull(rel.Image); err != nil {
		return err
	}
	ctx, cleanup, err := emptyBuildContext(b.dir)
	if err != nil {
		return err
	}
	defer cleanup()
	b.logln("Adding plugins to the backend image (" + tag + ")... (a few minutes)")
	return b.run.AgentBuild(filepath.Join(b.dir, backendPluginsDockerfile), ctx,
		"--build-arg", "BACKEND_BUILDER="+rel.Builder,
		"--build-arg", "BACKEND_IMAGE="+rel.Image,
		"--build-arg", "ADDITIONAL_PLUGS="+plugs,
		"--label", builtFromLabel+"="+key,
		"-t", tag)
}

func (b *Builder) rebuild(p plan, err error) error {
	if err != nil {
		return err
	}
	if err := p.build(); err != nil {
		return err
	}
	return b.recordCurrent(p.service, p.rel, p.version)
}

func (b *Builder) ensurePlan(p plan, err error) error {
	if err != nil {
		return err
	}
	if err := b.ensure(p.tag, p.key, p.service, p.build); err != nil {
		return err
	}
	return b.recordCurrent(p.service, p.rel, p.version)
}

func (b *Builder) current(p plan, err error) (bool, error) {
	if err != nil {
		return false, err
	}
	got, ok, err := b.builtFrom(p.tag)
	if err != nil {
		return false, err
	}
	return ok && got == p.key, nil
}

// BuildBackend recreates the backend image even when it looks current.
func (b *Builder) BuildBackend() error { return b.rebuild(b.backendPlan()) }

func (b *Builder) EnsureBackendImage() error { return b.ensurePlan(b.backendPlan()) }

func (b *Builder) BackendImageCurrent() (bool, error) { return b.current(b.backendPlan()) }

func (b *Builder) frontendPlan() (plan, error) {
	data, err := os.ReadFile(filepath.Join(b.dir, "frontend.env"))
	if err != nil {
		return plan{}, err
	}
	env, err := dotenv.Parse(bytes.NewReader(data))
	if err != nil {
		return plan{}, fmt.Errorf("frontend.env: %w", err)
	}
	rel, version, err := b.target(Frontend)
	if err != nil {
		return plan{}, err
	}
	p := plan{service: Frontend, rel: rel, version: version, tag: b.suffixed(b.set.FrontendImage), key: rel.Image}
	merged, changed := BuildSettings(rel, env)
	if !changed {
		p.build = func() error { return b.relabel(rel.Image, p.tag, p.key, "frontend") }
		return p, nil
	}
	data, err = json.Marshal(merged)
	if err != nil {
		return plan{}, err
	}
	p.key += "+env@" + shortHash(data)
	p.build = func() error { return b.buildFrontend(rel, merged, p.tag, p.key) }
	return p, nil
}

// BuildSettings compares the clinic's effective compiled settings with the
// release's, reporting whether the bundle must be rebuilt.
// Only the clinic API URL is substituted at startup. Empty compiled values are
// explicit values, not absent overrides.
func BuildSettings(rel Release, env map[string]string) (map[string]string, bool) {
	merged := make(map[string]string, len(env))
	for k, v := range env {
		if k == "REACT_CARE_API_URL" {
			continue
		}
		merged[k] = v
	}
	return merged, !maps.Equal(merged, rel.BuildEnv)
}

func (b *Builder) buildFrontend(rel Release, merged map[string]string, tag, key string) error {
	if err := b.pull(rel.Builder); err != nil {
		return err
	}
	if err := b.pull(rel.Image); err != nil {
		return err
	}
	ctx, cleanup, err := emptyBuildContext(b.dir)
	if err != nil {
		return err
	}
	defer cleanup()
	env := make(map[string]string, len(merged)+1)
	for k, v := range merged {
		env[k] = v
	}
	env["REACT_CARE_API_URL"] = envconfig.APIPlaceholder
	data, err := json.Marshal(env)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(ctx, "clinic.json"), data, 0o600); err != nil {
		return err
	}
	b.logln("Rebuilding the frontend with this clinic's build settings (" + tag + ")... (a few minutes)")
	return b.run.AgentBuild(filepath.Join(b.dir, frontendRebuildDockerfile), ctx,
		"--build-arg", "FRONTEND_BUILDER="+rel.Builder,
		"--build-arg", "FRONTEND_IMAGE="+rel.Image,
		"--label", builtFromLabel+"="+key,
		"-t", tag)
}

// BuildFrontend recreates the frontend image even when it looks current.
func (b *Builder) BuildFrontend() error { return b.rebuild(b.frontendPlan()) }

func (b *Builder) EnsureFrontendImage() error { return b.ensurePlan(b.frontendPlan()) }

func (b *Builder) FrontendImageCurrent() (bool, error) { return b.current(b.frontendPlan()) }

func emptyBuildContext(parent string) (dir string, cleanup func(), err error) {
	dir, err = os.MkdirTemp(parent, ".care-buildctx-")
	if err != nil {
		return "", nil, err
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}

const relabelDockerfile = "ARG IMAGE\nFROM ${IMAGE}\n"

// relabel tags a published image locally, recording where it came from.
func (b *Builder) relabel(src, tag, key, label string) error {
	if err := b.pull(src); err != nil {
		return err
	}
	ctx, cleanup, err := emptyBuildContext(b.dir)
	if err != nil {
		return err
	}
	defer cleanup()
	df := filepath.Join(ctx, "Dockerfile")
	if err := os.WriteFile(df, []byte(relabelDockerfile), 0o644); err != nil {
		return err
	}
	b.logln("Preparing the " + label + " image (" + tag + ")...")
	return b.run.AgentBuild(df, ctx,
		"--build-arg", "IMAGE="+src,
		"--label", builtFromLabel+"="+key,
		"-t", tag)
}

// pull downloads a published image unless it is already on this computer.
func (b *Builder) pull(ref string) error {
	if _, err := b.run.AgentCapture("image", "inspect", "--format", "{{.Id}}", ref); err == nil {
		return nil
	}
	b.logln("Downloading " + ref + "...")
	if err := b.run.AgentRun("pull", ref); err != nil {
		return fmt.Errorf("couldn't download %s; check the network connection: %w", ref, err)
	}
	return nil
}

func (b *Builder) BuildBackup() error {
	if src := b.set.BackupPrebuilt; src != "" {
		return b.relabel(src, b.set.BackupImage, src, "backup")
	}
	key, err := b.backupBuiltFrom()
	if err != nil {
		return err
	}
	b.logln("Building the backup image (" + b.set.BackupImage + ")...")
	df := filepath.Join(b.dir, "backup.Dockerfile")
	buildCtx, cleanup, err := emptyBuildContext(b.dir)
	if err != nil {
		return err
	}
	defer cleanup()
	return b.run.AgentBuild(df, buildCtx,
		"--build-arg", "POSTGRES_IMAGE="+b.set.PostgresImage,
		"--label", builtFromLabel+"="+key,
		"-t", b.set.BackupImage)
}

func (b *Builder) EnsureBackupImage() error {
	key, err := b.backupBuiltFrom()
	if err != nil {
		return err
	}
	return b.ensure(b.set.BackupImage, key, "backup", b.BuildBackup)
}

func (b *Builder) backupBuiltFrom() (string, error) {
	if b.set.BackupPrebuilt != "" {
		return b.set.BackupPrebuilt, nil
	}
	df, err := os.ReadFile(filepath.Join(b.dir, "backup.Dockerfile"))
	if err != nil {
		return "", err
	}
	return b.set.PostgresImage + "+dockerfile@" + shortHash(df), nil
}

func (b *Builder) BuildCaddy() error {
	if src := b.set.CaddyWafPrebuilt; src != "" {
		return b.relabel(src, b.set.CaddyWafImage, src, "Caddy + WAF")
	}
	key, err := b.caddyBuiltFrom()
	if err != nil {
		return err
	}
	b.logln("Building the Caddy + WAF image (" + b.set.CaddyWafImage + ")... (compiles Caddy; a few minutes)")
	df := filepath.Join(b.dir, "caddy.Dockerfile")
	buildCtx, cleanup, err := emptyBuildContext(b.dir)
	if err != nil {
		return err
	}
	defer cleanup()
	return b.run.AgentBuild(df, buildCtx,
		"--build-arg", "CADDY_IMAGE="+b.set.CaddyImage,
		"--build-arg", "CORAZA_VERSION="+b.set.CorazaVersion,
		"--label", builtFromLabel+"="+key,
		"-t", b.set.CaddyWafImage)
}

func (b *Builder) caddyBuiltFrom() (string, error) {
	if b.set.CaddyWafPrebuilt != "" {
		return b.set.CaddyWafPrebuilt, nil
	}
	df, err := os.ReadFile(filepath.Join(b.dir, "caddy.Dockerfile"))
	if err != nil {
		return "", err
	}
	return b.set.CaddyImage + "+coraza@" + b.set.CorazaVersion + "+dockerfile@" + shortHash(df), nil
}

func (b *Builder) EnsureCaddyImage() error {
	key, err := b.caddyBuiltFrom()
	if err != nil {
		return err
	}
	return b.ensure(b.set.CaddyWafImage, key, "caddy", b.BuildCaddy)
}

func shortHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:12]
}

const builtFromLabel = "org.opencontainers.image.base.name"

func (b *Builder) ensure(tag, want, label string, build func() error) error {
	got, ok, err := b.builtFrom(tag)
	if err != nil {
		return err
	}
	switch {
	case !ok:
		return build()
	case got == want:
		return nil
	}
	b.logln("The " + label + " image came from " + quoteOrUnknown(got) +
		", but this release expects " + want + " - replacing it.")
	return build()
}

func (b *Builder) builtFrom(tag string) (value string, ok bool, err error) {
	ids, err := b.run.AgentCapture("image", "ls", "--quiet", "--filter", "reference="+tag)
	if err != nil {
		return "", false, fmt.Errorf("inspect local images: %w", err)
	}
	if ids == "" {
		return "", false, nil
	}
	out, err := b.run.AgentCapture("image", "inspect", "-f",
		"{{index .Config.Labels \""+builtFromLabel+"\"}}", tag)
	if err != nil {
		return "", false, fmt.Errorf("inspect image %s: %w", tag, err)
	}
	v := strings.TrimSpace(out)
	if v == "<no value>" {
		v = ""
	}
	return v, true, nil
}

func quoteOrUnknown(s string) string {
	if s == "" {
		return "an unrecorded version"
	}
	return s
}
