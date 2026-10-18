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
	// Overrides are the clinic's frontend settings (nil: the release's).
	Overrides map[string]string
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
	var fe *Image
	if f := in.Frontend; f != nil {
		cur.Overrides = f.Overrides
		cur.Base = withImage(cur.Base, protocol.ImageRoleFrontend, &f.Base)
		fe = f.Image
	}
	if in.Plugins != nil {
		cur.Set = in.Plugins.Set
		cur.Base = withBackend(cur.Base, &in.Plugins.Base)
		cur.Images = withImage(withBackend(cur.Base, in.Plugins.Image), protocol.ImageRoleFrontend, fe)
		cur.Plugs, err = plugins.AdditionalPlugs(cur.Set)
		return cur, err
	}
	cur.Images = withImage(cur.Base, protocol.ImageRoleFrontend, fe)
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
	return withImage(imgs, protocol.ImageRoleBackend, b)
}

// withImage returns imgs with role's image replaced by b (none: imgs).
func withImage(imgs []Image, role protocol.ImageRole, b *Image) []Image {
	out := slices.Clone(imgs)
	if b == nil || len(out) == 0 {
		return out
	}
	for i := range out {
		if out[i].Role == role {
			out[i] = *b
			out[i].Role = role
		}
	}
	return out
}

func backendOf(imgs []Image) Image { return imageOf(imgs, protocol.ImageRoleBackend) }

func imageOf(imgs []Image, role protocol.ImageRole) Image {
	for _, i := range imgs {
		if i.Role == role {
			return Image{Role: i.Role, Repo: i.Repo, Digest: i.Digest}
		}
	}
	return Image{}
}

// builderOf is r's backend builder image, if it publishes one.
func builderOf(r *feed.CareRelease) *Image {
	return releaseBuilder(r, protocol.ImageRoleBackendBuilder)
}

// releaseBuilder is r's builder image of role, if it publishes one.
func releaseBuilder(r *feed.CareRelease, role protocol.ImageRole) *Image {
	if r == nil {
		return nil
	}
	ref, ok := r.Builders[role]
	if !ok {
		return nil
	}
	repo, _, _ := strings.Cut(ref.Ref, "@")
	return &Image{Role: role, Repo: repo, Digest: ref.Digest, URL: ref.Tar, Size: ref.Size}
}

// targetImages is what the stack env pins once j applies.
func targetImages(j *Journal) []Image {
	imgs := j.Care.Images
	if j.Plugins != nil {
		imgs = withBackend(imgs, j.Plugins.Image)
	}
	if j.Frontend != nil {
		imgs = withImage(imgs, protocol.ImageRoleFrontend, j.Frontend.Image)
	}
	return imgs
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
	if p == nil || p.Carried {
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
		builder, rev, err = c.installedBuilder(in, protocol.ImageRoleBackendBuilder)
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
		Frontend:  carriedFrontend(in),
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

// installedRelease is the verified feed's release of the installed CARE
// (nil when there is none) and the feed revision.
func (c *Coordinator) installedRelease(in Installed) (*feed.CareRelease, uint64, error) {
	if in.Care == nil {
		return nil, 0, nil
	}
	m, ver, err := c.manifest()
	if err != nil && !errors.Is(err, ErrNotVerified) {
		return nil, 0, err
	}
	if m == nil {
		return nil, 0, nil
	}
	for i := range m.Care {
		r := &m.Care[i]
		if r.Version.String() == in.Care.Version && r.ReleasedAt.Equal(in.Care.ReleasedAt) {
			return r, ver.HighestRevision, nil
		}
	}
	return nil, 0, nil
}

// installedBuilder finds the builder of role for the installed CARE: the
// verified feed's release, else Options.DevBuilders.
func (c *Coordinator) installedBuilder(in Installed, role protocol.ImageRole) (*Image, uint64, error) {
	r, rev, err := c.installedRelease(in)
	if err != nil {
		return nil, 0, err
	}
	if b := releaseBuilder(r, role); b != nil {
		return b, rev, nil
	}
	if b := c.devBuilder(role); b != nil {
		return b, 0, nil
	}
	if role == protocol.ImageRoleFrontendBuilder {
		return nil, 0, ErrNoFrontendBuilder
	}
	return nil, 0, ErrNoBuilder
}

func (c *Coordinator) devBuilder(role protocol.ImageRole) *Image {
	for _, b := range c.o.DevBuilders {
		if b.Role == role {
			return &Image{Role: role, Repo: b.Repo, Digest: b.Digest, Tarball: b.Tarball}
		}
	}
	return nil
}

// carriedFrontend keeps the installed frontend as it is in an operation
// that does not change it.
func carriedFrontend(in Installed) *FrontendTarget {
	f := in.Frontend
	if f == nil {
		return nil
	}
	return &FrontendTarget{Overrides: f.Overrides, Base: f.Base, Image: f.Image, Carried: true}
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
// clinic install, onboarding) and re-applies the recorded plugin set and
// frontend settings. An install from other images than the recorded CARE
// release forgets that release; the recorded custom plugins are then rebuilt
// on the installed backend with SetPlugins, and the recorded frontend
// settings on the installed frontend with SetFrontendSettings (each
// journaled, rolled back on failure).
func (c *Coordinator) Reinstalled(ctx context.Context) error {
	set, overrides, err := c.syncInstall(ctx)
	if err != nil {
		return err
	}
	if set != nil {
		c.logf("Re-applying the clinic's plugins: %s", plugins.Summary(set))
		if err := c.SetPlugins(ctx, set); err != nil {
			return err
		}
	}
	if overrides != nil {
		c.logf("Re-applying the clinic's frontend settings: %s", frontendSummary(overrides))
		_, err := c.SetFrontendSettings(ctx, overrides)
		return err
	}
	return nil
}

// syncInstall records the installed images, plugins and frontend; it
// returns the recorded plugin set and frontend overrides that still need
// applying (nil: nothing).
func (c *Coordinator) syncInstall(ctx context.Context) ([]plugins.Plugin, map[string]string, error) {
	unlock, err := c.acquire()
	if err != nil {
		return nil, nil, err
	}
	defer unlock()
	if j, err := c.st.journal(); err != nil {
		return nil, nil, err
	} else if j != nil {
		return nil, nil, fmt.Errorf("%w (%s update to %s is %s)", ErrInFlight, j.Component, j.To, j.State)
	}
	in, err := c.installed()
	if err != nil {
		return nil, nil, err
	}
	imgs, plugs, err := c.o.Stack.Configured(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("read the clinic configuration: %w", err)
	}
	if len(imgs) == 0 {
		return nil, nil, ErrNotInstalled
	}
	installed := toImages(imgs)
	running := backendOf(installed)
	runningFE := imageOf(installed, protocol.ImageRoleFrontend)
	fe := in.Frontend
	feKept := fe == nil || fe.Image == nil && runningFE.Digest == fe.Base.Digest || fe.Image != nil && runningFE.Digest == fe.Image.Digest
	if fe != nil && fe.Image != nil && feKept {
		// The derived frontend still runs: compare its base with the release.
		installed = withImage(installed, protocol.ImageRoleFrontend, &fe.Base)
	}
	rec := in.Plugins
	if rec != nil && feKept && (running.Digest == rec.Base.Digest || rec.Image != nil && running.Digest == rec.Image.Digest) {
		if want, err := plugins.AdditionalPlugs(rec.Set); err == nil && want == plugs {
			return nil, nil, nil // reconfigured in place: nothing changed
		}
	}
	changed := false
	if in.Care != nil && !sameDigests(in.Care.Images, installed, protocol.ImageRoleBackend, protocol.ImageRoleFrontend) {
		c.logf("The clinic was installed from other images than CARE %s; it is no longer recorded", in.Care.Version)
		in.Care, changed = nil, true
	}
	if rec != nil {
		applied := plugins.Defaults()
		if plugs != "" {
			if applied, err = plugins.ParseAdditionalPlugs(plugs); err != nil {
				return nil, nil, err
			}
		}
		in.Plugins, changed = &InstalledPlugins{Set: applied, Base: running}, true
	}
	var overrides map[string]string
	if !feKept {
		// The install runs the published frontend; the settings are rebuilt.
		overrides, in.Frontend, changed = fe.Overrides, nil, true
	}
	if changed {
		in.UpdatedAt = c.now()
		if err := c.st.save(InstalledFile, in); err != nil {
			return nil, nil, err
		}
	}
	var set []plugins.Plugin
	if rec != nil && !plugins.Equal(rec.Set, in.Plugins.Set) {
		set = rec.Set
	}
	return set, overrides, nil
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
