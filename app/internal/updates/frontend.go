package updates

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/compose-spec/compose-go/v2/dotenv"
	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
	"github.com/ohcnetwork/care_desktop/app/internal/feed"
)

// ComponentFrontend is the journal component of a frontend settings change.
const ComponentFrontend = "frontend"

// StepBuildFrontend rebuilds the CARE frontend with the clinic's settings
// (image.build frontend-rebuild). It runs in CARE updates too, so the
// settings are rebuilt on every new CARE base.
const StepBuildFrontend = "build-frontend"

// frontendSteps change the clinic's frontend settings: no backup or
// migrations, the database is untouched.
var frontendSteps = []string{StepPreflight, StepBuildFrontend, StepWriteEnv, StepStartApp, StepHealth}

// ErrNoFrontendBuilder means frontend settings need the CARE frontend
// builder image and neither the feed nor the dev images provide one.
var ErrNoFrontendBuilder = errors.New("frontend settings need the CARE frontend builder image, which this CARE release does not provide")

// ErrFrontendEnvUnknown means the CARE release's frontend settings are not
// known: its feed entry has no build_env and Options.FrontendEnv is empty.
var ErrFrontendEnvUnknown = errors.New("the CARE release's frontend settings are unknown")

// SettingError rejects a frontend setting.
type SettingError struct {
	Key, Msg string
}

func (e *SettingError) Error() string { return e.Key + ": " + e.Msg }

// managedFrontendKeys are frontend settings CARE Clinic sets: a clinic
// cannot override them.
var managedFrontendKeys = map[string]bool{
	"REACT_CARE_API_URL":              true,
	"REACT_MFE_REGISTERED_COMPONENTS": true,
	"REACT_ENABLED_APPS":              true,
	"REACT_DISABLE_PATIENT_LOGIN":     true,
	"REACT_RECAPTCHA_SITE_KEY":        true,
}

// ManagedFrontendKey reports whether CARE Clinic sets key (a clinic cannot
// override it).
func ManagedFrontendKey(key string) bool { return managedFrontendKeys[key] }

// FrontendKeys are the REACT_* settings a clinic may override.
func FrontendKeys() []string {
	var out []string
	for _, k := range protocol.FrontendBuildKeys() {
		if !managedFrontendKeys[k] {
			out = append(out, k)
		}
	}
	return out
}

// ParseFrontendEnv reads a frontend.env (dotenv syntax): its REACT_*
// assignments. Other keys are ignored.
func ParseFrontendEnv(data []byte) (map[string]string, error) {
	env, err := dotenv.UnmarshalBytesWithLookup(data, func(string) (string, bool) { return "", false })
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for k, v := range env {
		if strings.HasPrefix(k, "REACT_") {
			out[k] = v
		}
	}
	return out, nil
}

// CheckFrontendSettings rejects unsupported keys and values (without the
// release's build_env: managed keys are not checked against it).
func CheckFrontendSettings(settings map[string]string) error {
	for _, k := range slices.Sorted(maps.Keys(settings)) {
		if managedFrontendKeys[k] || settings[k] == "" && slices.Contains(protocol.FrontendBuildKeys(), k) {
			continue
		}
		if err := checkFrontendSetting(k, settings[k]); err != nil {
			return err
		}
	}
	return nil
}

func checkFrontendSetting(k, v string) error {
	if protocol.ValidFrontendBuildValue(k, v) {
		return nil
	}
	if !protocol.ValidFrontendBuildKey(k) {
		return &SettingError{k, "is not a supported frontend setting (want REACT_ and up to 60 of A-Z, 0-9, _)"}
	}
	return &SettingError{k, fmt.Sprintf("invalid value %q", v)}
}

// frontendOverrides checks settings against base (the release's
// build_env) and returns those that differ from it. A managed key must keep
// base's value.
func frontendOverrides(base, settings map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for _, k := range slices.Sorted(maps.Keys(settings)) {
		v := settings[k]
		if bv, ok := base[k]; ok && bv == v || !ok && v == "" {
			continue // the release's value; an empty value is unset
		}
		if managedFrontendKeys[k] {
			return nil, &SettingError{k, "is managed by CARE Clinic"}
		}
		if err := checkFrontendSetting(k, v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, nil
}

// frontendBuildEnv is what the frontend is rebuilt with: base with
// overrides applied.
func frontendBuildEnv(base, overrides map[string]string) (map[string]string, error) {
	env := maps.Clone(base)
	if env == nil {
		env = map[string]string{}
	}
	maps.Copy(env, overrides)
	for _, k := range slices.Sorted(maps.Keys(env)) {
		if !protocol.ValidFrontendBuildValue(k, env[k]) {
			return nil, &SettingError{k, "not a supported frontend setting or value"}
		}
	}
	return env, nil
}

func frontendSummary(overrides map[string]string) string {
	if len(overrides) == 0 {
		return "release defaults"
	}
	return strings.Join(slices.Sorted(maps.Keys(overrides)), ", ")
}

// releaseEnv is the frontend settings of a CARE release.
func (c *Coordinator) releaseEnv(r *feed.CareRelease) map[string]string {
	if r != nil && len(r.BuildEnv) > 0 {
		return r.BuildEnv
	}
	return c.o.FrontendEnv
}

// buildFrontend is StepBuildFrontend.
func (c *Coordinator) buildFrontend(ctx context.Context, j *Journal) error {
	f := j.Frontend
	if f == nil || f.Carried {
		return nil
	}
	if len(f.Overrides) == 0 {
		f.Image = nil
		return c.saveJournal(j)
	}
	if len(f.BaseEnv) == 0 {
		return ErrFrontendEnvUnknown
	}
	env, err := frontendBuildEnv(f.BaseEnv, f.Overrides)
	if err != nil {
		return err
	}
	if f.Builder == nil {
		return ErrNoFrontendBuilder
	}
	b := []Image{*f.Builder}
	if err := c.downloadImages(ctx, b); err != nil {
		return err
	}
	f.Builder = &b[0]
	if err := c.saveJournal(j); err != nil {
		return err
	}
	if err := c.o.Stack.EnsureImages(ctx, StackImages(b)); err != nil {
		return fmt.Errorf("frontend builder image: %w", err)
	}
	c.logf("Rebuilding the CARE frontend with the clinic's settings: %s", frontendSummary(f.Overrides))
	op := protocol.ImageBuild{
		Kind: protocol.BuildFrontendRebuild, BuildEnv: env,
		Base:    f.Base.Repo + "@" + string(f.Base.Digest),
		Builder: f.Builder.Repo + "@" + string(f.Builder.Digest),
	}
	var res protocol.ImageBuildResult
	if err := c.o.Agent.Op(ctx, op, &res); err != nil {
		return fmt.Errorf("rebuild the frontend: %w", err)
	}
	if res.Ref == "" || !res.Digest.Valid() {
		return fmt.Errorf("image.build returned %q@%q", res.Ref, res.Digest)
	}
	f.Image = &Image{Role: protocol.ImageRoleFrontend, Repo: res.Ref, Digest: res.Digest}
	return c.saveJournal(j)
}

func installedFrontend(j *Journal) *InstalledFrontend {
	f := j.Frontend
	if f == nil || len(f.Overrides) == 0 {
		return nil
	}
	return &InstalledFrontend{Overrides: f.Overrides, Base: f.Base, Image: f.Image}
}

// FrontendSettings is the clinic's frontend settings.
type FrontendSettings struct {
	// Release is the installed CARE release's settings (its build_env).
	Release map[string]string `json:"release"`
	// Overrides are the clinic's settings that differ from Release.
	Overrides map[string]string `json:"overrides"`
	// Keys are the settings a clinic may override.
	Keys []string `json:"keys"`
	// Rebuilt: the stack runs a frontend rebuilt with Overrides.
	Rebuilt bool `json:"rebuilt"`
}

// FrontendSettings returns the clinic's frontend settings.
func (c *Coordinator) FrontendSettings(ctx context.Context) (FrontendSettings, error) {
	in, err := c.installed()
	if err != nil {
		return FrontendSettings{}, err
	}
	r, _, err := c.installedRelease(in)
	if err != nil {
		return FrontendSettings{}, err
	}
	out := FrontendSettings{Release: maps.Clone(c.releaseEnv(r)), Overrides: map[string]string{}, Keys: FrontendKeys()}
	if f := in.Frontend; f != nil {
		out.Overrides = maps.Clone(f.Overrides)
		out.Rebuilt = f.Image != nil
	}
	if out.Release == nil {
		out.Release = map[string]string{}
	}
	return out, nil
}

// SetFrontendSettings changes the clinic's frontend settings as one
// journaled operation: rebuild the CARE frontend from the release's builder
// image with the release's build_env plus settings (the published frontend
// when no setting differs from the release), write it into the stack env,
// start and health-check, rolling back to the previous image on failure.
// settings may hold the whole frontend.env: values equal to the release's
// are not recorded. It reports whether the frontend changed (false: the
// settings were already applied), and returns an *OpError for a failed
// operation.
func (c *Coordinator) SetFrontendSettings(ctx context.Context, settings map[string]string) (bool, error) {
	unlock, err := c.acquire()
	if err != nil {
		return false, err
	}
	defer unlock()
	if j, err := c.st.journal(); err != nil {
		return false, err
	} else if j != nil {
		return false, fmt.Errorf("%w (%s update to %s is %s)", ErrInFlight, j.Component, j.To, j.State)
	}
	in, err := c.installed()
	if err != nil {
		return false, err
	}
	cur, err := c.current(ctx, in)
	if err != nil {
		return false, err
	}
	if cur.Base == nil {
		return false, ErrNotInstalled
	}
	r, rev, err := c.installedRelease(in)
	if err != nil {
		return false, err
	}
	baseEnv := c.releaseEnv(r)
	if len(baseEnv) == 0 {
		return false, ErrFrontendEnvUnknown
	}
	overrides, err := frontendOverrides(baseEnv, settings)
	if err != nil {
		return false, err
	}
	if maps.Equal(overrides, cur.Overrides) || len(overrides) == 0 && cur.Overrides == nil {
		c.logf("The frontend settings are unchanged (%s)", frontendSummary(overrides))
		return false, nil
	}
	var builder *Image
	if len(overrides) > 0 {
		if builder, _, err = c.installedBuilder(in, protocol.ImageRoleFrontendBuilder); err != nil {
			return false, err
		}
	}
	now := c.now()
	j := &Journal{
		ID:        fmt.Sprintf("%s-%s-%d", ComponentFrontend, now.Format("20060102T150405Z"), now.Nanosecond()),
		Component: ComponentFrontend, From: frontendSummary(cur.Overrides), To: frontendSummary(overrides), Revision: rev,
		StartedAt: now,
		Care:      &CareTarget{Images: cur.Base},
		Plugins:   &PluginsTarget{Set: cur.Set, Base: backendOf(cur.Base), Carried: true},
		Frontend: &FrontendTarget{
			Overrides: overrides, BaseEnv: baseEnv,
			Base: imageOf(cur.Base, protocol.ImageRoleFrontend), Builder: builder,
		},
		Rollback: Rollback{PreviousImages: cur.Images, PreviousPlugs: cur.Plugs},
	}
	if be := backendOf(cur.Images); be.Digest != j.Plugins.Base.Digest {
		j.Plugins.Image = &be
	}
	if in.Care != nil {
		j.Care.Version, j.Care.ReleasedAt = in.Care.Version, in.Care.ReleasedAt
	}
	for _, n := range frontendSteps {
		j.Steps = append(j.Steps, StepRecord{Name: n, Status: StepPending})
	}
	c.logf("Changing the CARE frontend settings from %s to %s", j.From, j.To)
	return true, c.run(ctx, j)
}
