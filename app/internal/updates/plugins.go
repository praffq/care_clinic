package updates

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
	"github.com/ohcnetwork/care_desktop/app/internal/feed"
	"github.com/ohcnetwork/care_desktop/app/internal/plugins"
	"github.com/ohcnetwork/care_desktop/app/internal/stack"
)

// ComponentPlugins is the journal component of a plugin change.
const ComponentPlugins = "plugins"

// StepBuild builds the derived backend image with the plugins the CARE
// backend image lacks (image.build). It runs in CARE updates too, so the
// clinic's plugins are rebuilt on every new CARE base.
const StepBuild = "build"

// pluginSteps change the plugin set of the installed CARE: the CARE steps
// without image downloads.
var pluginSteps = []string{StepPreflight, StepBackup, StepBuild, StepStopApp, StepWriteEnv, StepMigrate, StepStartApp, StepHealth}

// ErrNoBuilder means custom plugins need the CARE backend builder image and
// neither the feed nor the dev images provide one for the installed CARE.
var ErrNoBuilder = errors.New("custom plugins need the CARE plugin builder image, which this CARE release does not provide; only bundled plugins can be enabled")

// ErrNotInstalled means there is no CARE stack to change.
var ErrNotInstalled = errors.New("CARE is not installed")

// current is what the stack runs now.
type current struct {
	// Base is every stack image with the CARE backend (not a derived one);
	// nil when no clinic is configured.
	Base []Image
	// Images is what the stack env pins (Base with the derived backend).
	Images []Image
	Set    []plugins.Plugin
	Plugs  string
}

// current reads the plugin set and images from installed.json, or from the
// engine's stack env for a clinic the coordinator has not changed yet.
func (c *Coordinator) current(ctx context.Context, in Installed) (current, error) {
	var cur current
	imgs, plugs, err := c.o.Stack.Configured(ctx)
	if err != nil {
		return cur, fmt.Errorf("read the clinic configuration: %w", err)
	}
	switch {
	case in.Care != nil:
		cur.Base = slices.Clone(in.Care.Images)
	case len(imgs) > 0:
		for _, i := range imgs {
			cur.Base = append(cur.Base, Image{Role: i.Role, Repo: i.Repo, Digest: i.Digest})
		}
	}
	if in.Plugins != nil {
		cur.Set = in.Plugins.Set
		cur.Base = withBackend(cur.Base, &in.Plugins.Base)
		cur.Images = withBackend(cur.Base, in.Plugins.Image)
		cur.Plugs, err = plugins.AdditionalPlugs(cur.Set)
		return cur, err
	}
	cur.Images = cur.Base
	cur.Plugs = plugs
	if plugs == "" {
		cur.Set = plugins.Defaults()
		return cur, nil
	}
	cur.Set, err = plugins.ParseAdditionalPlugs(plugs)
	return cur, err
}

// withBackend returns imgs with the backend image replaced by b (none: imgs).
func withBackend(imgs []Image, b *Image) []Image {
	out := slices.Clone(imgs)
	if b == nil || len(out) == 0 {
		return out
	}
	for i := range out {
		if out[i].Role == protocol.ImageRoleBackend {
			out[i] = *b
			out[i].Role = protocol.ImageRoleBackend
		}
	}
	return out
}

func backendOf(imgs []Image) Image {
	for _, i := range imgs {
		if i.Role == protocol.ImageRoleBackend {
			return Image{Role: i.Role, Repo: i.Repo, Digest: i.Digest}
		}
	}
	return Image{}
}

// builderOf is r's backend builder image, if it publishes one.
func builderOf(r *feed.CareRelease) *Image {
	if r == nil {
		return nil
	}
	ref, ok := r.Builders[protocol.ImageRoleBackendBuilder]
	if !ok {
		return nil
	}
	repo, _, _ := strings.Cut(ref.Ref, "@")
	return &Image{Role: protocol.ImageRoleBackendBuilder, Repo: repo, Digest: ref.Digest, URL: ref.Tar}
}

// targetImages is what the stack env pins once j applies.
func targetImages(j *Journal) []Image {
	if j.Plugins == nil {
		return j.Care.Images
	}
	return withBackend(j.Care.Images, j.Plugins.Image)
}

func targetPlugs(j *Journal) (string, error) {
	if j.Plugins == nil {
		return "", nil
	}
	return plugins.AdditionalPlugs(j.Plugins.Set)
}

// build is StepBuild.
func (c *Coordinator) build(ctx context.Context, j *Journal) error {
	p := j.Plugins
	if p == nil {
		return nil
	}
	specs := plugins.BuildSpecs(p.Set)
	if len(specs) == 0 {
		p.Image = nil
		return c.saveJournal(j)
	}
	if p.Builder == nil {
		return ErrNoBuilder
	}
	b := []Image{*p.Builder}
	if err := c.downloadImages(ctx, b); err != nil {
		return err
	}
	p.Builder = &b[0]
	if err := c.saveJournal(j); err != nil {
		return err
	}
	if err := c.o.Stack.EnsureImages(ctx, StackImages(b)); err != nil {
		return fmt.Errorf("plugin builder image: %w", err)
	}
	c.logf("Building the CARE server with %d plugin(s): %s", len(specs), plugins.Summary(p.Set))
	op := protocol.ImageBuild{
		Kind: protocol.BuildBackendPlugins, Plugins: specs,
		Base:    p.Base.Repo + "@" + string(p.Base.Digest),
		Builder: p.Builder.Repo + "@" + string(p.Builder.Digest),
	}
	var res protocol.ImageBuildResult
	if err := c.o.Agent.Op(ctx, op, &res); err != nil {
		return fmt.Errorf("build the plugins: %w", err)
	}
	if res.Ref == "" || !res.Digest.Valid() {
		return fmt.Errorf("image.build returned %q@%q", res.Ref, res.Digest)
	}
	p.Image = &Image{Role: protocol.ImageRoleBackend, Repo: res.Ref, Digest: res.Digest}
	return c.saveJournal(j)
}

func installedPlugins(j *Journal) *InstalledPlugins {
	if j.Plugins == nil {
		return nil
	}
	return &InstalledPlugins{Set: j.Plugins.Set, Base: j.Plugins.Base, Image: j.Plugins.Image}
}

// Plugins returns the clinic's backend plugins, secrets redacted.
func (c *Coordinator) Plugins(ctx context.Context) ([]plugins.Plugin, error) {
	in, err := c.installed()
	if err != nil {
		return nil, err
	}
	cur, err := c.current(ctx, in)
	if err != nil {
		return nil, err
	}
	return plugins.RedactAll(cur.Set), nil
}

// UnredactedPlugins is Plugins with secrets, for building a changed set.
func (c *Coordinator) UnredactedPlugins(ctx context.Context) ([]plugins.Plugin, error) {
	in, err := c.installed()
	if err != nil {
		return nil, err
	}
	cur, err := c.current(ctx, in)
	return cur.Set, err
}

// SetPlugins changes the backend plugin set as one journaled operation:
// back up the database, build the derived backend image when a plugin is
// not bundled, write ADDITIONAL_PLUGS and the backend image into the stack
// env, migrate, start and health-check. A failure rolls back to the
// previous env (and restores the database once migrations ran). It returns
// an *OpError for a failed operation.
func (c *Coordinator) SetPlugins(ctx context.Context, set []plugins.Plugin) error {
	if err := plugins.ValidateSet(set); err != nil {
		return err
	}
	unlock, err := c.acquire()
	if err != nil {
		return err
	}
	defer unlock()
	if j, err := c.st.journal(); err != nil {
		return err
	} else if j != nil {
		return fmt.Errorf("%w (%s update to %s is %s)", ErrInFlight, j.Component, j.To, j.State)
	}
	in, err := c.installed()
	if err != nil {
		return err
	}
	cur, err := c.current(ctx, in)
	if err != nil {
		return err
	}
	if cur.Base == nil {
		return ErrNotInstalled
	}
	var builder *Image
	var rev uint64
	if len(plugins.BuildSpecs(set)) > 0 {
		builder, rev, err = c.installedBuilder(in)
		if err != nil {
			return err
		}
	}
	now := c.now()
	j := &Journal{
		ID:        fmt.Sprintf("%s-%s-%d", ComponentPlugins, now.Format("20060102T150405Z"), now.Nanosecond()),
		Component: ComponentPlugins, From: plugins.Summary(cur.Set), To: plugins.Summary(set), Revision: rev,
		StartedAt: now,
		Care:      &CareTarget{Images: cur.Base},
		Plugins:   &PluginsTarget{Set: set, Base: backendOf(cur.Base), Builder: builder},
		Rollback:  Rollback{PreviousImages: cur.Images, PreviousPlugs: cur.Plugs},
	}
	if in.Care != nil {
		j.Care.Version, j.Care.ReleasedAt = in.Care.Version, in.Care.ReleasedAt
	}
	for _, n := range pluginSteps {
		j.Steps = append(j.Steps, StepRecord{Name: n, Status: StepPending})
	}
	c.logf("Changing the CARE plugins from %s to %s", j.From, j.To)
	return c.run(ctx, j)
}

// EnablePlugin adds p (or replaces the plugin with its ID) and applies the
// set like SetPlugins. A secret config sent back redacted keeps its value.
func (c *Coordinator) EnablePlugin(ctx context.Context, p plugins.Plugin) error {
	set, err := c.UnredactedPlugins(ctx)
	if err != nil {
		return err
	}
	if i := slices.IndexFunc(set, func(q plugins.Plugin) bool { return q.ID == p.ID }); i >= 0 {
		for k, v := range p.Configs {
			if v == plugins.RedactedValue {
				if old, ok := set[i].Configs[k]; ok {
					p.Configs[k] = old
				}
			}
		}
	}
	for k, v := range p.Configs {
		if v == plugins.RedactedValue {
			return fmt.Errorf("plugin %s: %s needs a value", p.ID, k)
		}
	}
	next, err := plugins.Enable(set, p)
	if err != nil {
		return err
	}
	return c.SetPlugins(ctx, next)
}

// DisablePlugin removes the plugin id and applies the set like SetPlugins.
func (c *Coordinator) DisablePlugin(ctx context.Context, id string) error {
	set, err := c.UnredactedPlugins(ctx)
	if err != nil {
		return err
	}
	next, err := plugins.Disable(set, id)
	if err != nil {
		return err
	}
	return c.SetPlugins(ctx, next)
}

// installedBuilder finds the backend builder for the installed CARE: the
// verified feed's release, else Options.DevBuilder.
func (c *Coordinator) installedBuilder(in Installed) (*Image, uint64, error) {
	if in.Care != nil {
		m, ver, err := c.manifest()
		if err != nil && !errors.Is(err, ErrNotVerified) {
			return nil, 0, err
		}
		if m != nil {
			for i := range m.Care {
				r := &m.Care[i]
				if r.Version.String() == in.Care.Version && r.ReleasedAt.Equal(in.Care.ReleasedAt) {
					if b := builderOf(r); b != nil {
						return b, ver.HighestRevision, nil
					}
				}
			}
		}
	}
	if b := c.o.DevBuilder; b != nil {
		img := Image{Role: protocol.ImageRoleBackendBuilder, Repo: b.Repo, Digest: b.Digest, Tarball: b.Tarball}
		return &img, 0, nil
	}
	return nil, 0, ErrNoBuilder
}

// redact masks plugin secrets in a copy of s.
func (s Status) redact() Status {
	if p := s.Installed.Plugins; p != nil {
		cp := *p
		cp.Set = plugins.RedactAll(p.Set)
		s.Installed.Plugins = &cp
	}
	if j := s.Journal; j != nil {
		cp := *j
		if j.Plugins != nil {
			pt := *j.Plugins
			pt.Set = plugins.RedactAll(pt.Set)
			cp.Plugins = &pt
		}
		if cp.Rollback.PreviousPlugs != "" {
			cp.Rollback.PreviousPlugs = "(redacted)"
		}
		s.Journal = &cp
	}
	return s
}

// DevBuilder picks the backend builder from stack.DevBuilders, for
// Options.DevBuilder; nil when there is none.
func DevBuilder(imgs []stack.Image) *stack.Image {
	for i := range imgs {
		if imgs[i].Role == protocol.ImageRoleBackendBuilder {
			return &imgs[i]
		}
	}
	return nil
}

// InstallPlugs is the ADDITIONAL_PLUGS a clinic install from CARE release
// images starts with: the recorded set's bundled plugins, which those
// images already have. "" (nothing recorded) means the catalog defaults.
// Reinstalled then builds the recorded custom plugins.
func (c *Coordinator) InstallPlugs() (string, error) {
	in, err := c.installed()
	if err != nil || in.Plugins == nil {
		return "", err
	}
	return plugins.AdditionalPlugs(plugins.BundledOnly(in.Plugins.Set))
}

// Reinstalled reconciles installed.json with a clinic (re)install (ctl
// clinic install, onboarding) and re-applies the recorded plugin set. An
// install from other images than the recorded CARE release forgets that
// release; the recorded custom plugins are then rebuilt on the installed
// backend with SetPlugins (journaled, rolled back on failure).
func (c *Coordinator) Reinstalled(ctx context.Context) error {
	recorded, err := c.syncInstall(ctx)
	if err != nil || recorded == nil {
		return err
	}
	c.logf("Re-applying the clinic's plugins: %s", plugins.Summary(recorded))
	return c.SetPlugins(ctx, recorded)
}

// syncInstall records the installed images and plugins; it returns the
// recorded set when it still needs applying.
func (c *Coordinator) syncInstall(ctx context.Context) ([]plugins.Plugin, error) {
	unlock, err := c.acquire()
	if err != nil {
		return nil, err
	}
	defer unlock()
	if j, err := c.st.journal(); err != nil {
		return nil, err
	} else if j != nil {
		return nil, fmt.Errorf("%w (%s update to %s is %s)", ErrInFlight, j.Component, j.To, j.State)
	}
	in, err := c.installed()
	if err != nil {
		return nil, err
	}
	imgs, plugs, err := c.o.Stack.Configured(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the clinic configuration: %w", err)
	}
	if len(imgs) == 0 {
		return nil, ErrNotInstalled
	}
	running := backendOf(toImages(imgs))
	rec := in.Plugins
	if rec != nil && (running.Digest == rec.Base.Digest || rec.Image != nil && running.Digest == rec.Image.Digest) {
		if want, err := plugins.AdditionalPlugs(rec.Set); err == nil && want == plugs {
			return nil, nil // reconfigured in place: nothing changed
		}
	}
	changed := false
	if in.Care != nil && !sameDigests(in.Care.Images, toImages(imgs), protocol.ImageRoleBackend, protocol.ImageRoleFrontend) {
		c.logf("The clinic was installed from other images than CARE %s; it is no longer recorded", in.Care.Version)
		in.Care, changed = nil, true
	}
	if rec != nil {
		applied := plugins.Defaults()
		if plugs != "" {
			if applied, err = plugins.ParseAdditionalPlugs(plugs); err != nil {
				return nil, err
			}
		}
		in.Plugins, changed = &InstalledPlugins{Set: applied, Base: running}, true
	}
	if changed {
		in.UpdatedAt = c.now()
		if err := c.st.save(InstalledFile, in); err != nil {
			return nil, err
		}
	}
	if rec == nil || plugins.Equal(rec.Set, in.Plugins.Set) {
		return nil, nil
	}
	return rec.Set, nil
}

func toImages(imgs []stack.Image) []Image {
	out := make([]Image, 0, len(imgs))
	for _, i := range imgs {
		out = append(out, Image{Role: i.Role, Repo: i.Repo, Digest: i.Digest})
	}
	return out
}

func sameDigests(a, b []Image, roles ...protocol.ImageRole) bool {
	for _, r := range roles {
		var da, db protocol.Digest
		for _, i := range a {
			if i.Role == r {
				da = i.Digest
			}
		}
		for _, i := range b {
			if i.Role == r {
				db = i.Digest
			}
		}
		if da != db {
			return false
		}
	}
	return true
}
