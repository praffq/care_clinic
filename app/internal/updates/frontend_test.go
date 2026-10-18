package updates_test

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
	"github.com/ohcnetwork/care_desktop/app/internal/plugins"
	"github.com/ohcnetwork/care_desktop/app/internal/stack"
	"github.com/ohcnetwork/care_desktop/app/internal/updates"
)

// kitFrontendEnv is the frontend.env template the test releases were built
// with (the harness feed publishes no build_env).
var kitFrontendEnv = map[string]string{
	"REACT_CARE_API_URL":                            "",
	"REACT_DISABLE_PATIENT_LOGIN":                   "true",
	"REACT_MFE_REGISTERED_COMPONENTS":               "*",
	"REACT_ENABLED_APPS":                            "",
	"REACT_PATIENT_REG_MIN_GEO_ORG_LEVELS_REQUIRED": "0",
}

func withKitEnv(o *updates.Options) { o.FrontendEnv = kitFrontendEnv }

// settings is the whole frontend.env with kv applied.
func settings(kv ...string) map[string]string {
	s := maps.Clone(kitFrontendEnv)
	for i := 0; i < len(kv); i += 2 {
		s[kv[i]] = kv[i+1]
	}
	return s
}

func setFE(h *harness, _ context.Context, s map[string]string) error {
	_, err := h.c.SetFrontendSettings(context.Background(), s)
	return err
}

func (h *harness) frontend() protocol.Digest {
	h.w.Lock()
	defer h.w.Unlock()
	return h.w.Env[protocol.ImageRoleFrontend]
}

func (h *harness) lastBuild() string {
	calls := h.w.CallLog()
	for i := len(calls) - 1; i >= 0; i-- {
		if strings.HasPrefix(calls[i], "op image.build") {
			return calls[i]
		}
	}
	return ""
}

func TestFrontendSettingsRebuildThenCareUpdateThenReset(t *testing.T) {
	h := newHarness(t, withKitEnv)
	h.installWithBuilders()
	ctx := context.Background()

	// The release's own values rebuild nothing.
	if err := setFE(h, ctx, settings()); err != nil {
		t.Fatal(err)
	}
	if h.w.Count("op image.build") != 0 || h.frontend() != Digest("frontend") || h.status().Installed.Frontend != nil {
		t.Fatalf("rebuilt for release settings: %q", h.w.CallLog())
	}

	before := len(h.w.CallLog())
	if err := setFE(h, ctx, settings("REACT_APP_TITLE", "Sunrise Clinic", "REACT_ENABLED_APPS", "")); err != nil {
		t.Fatal(err)
	}
	h.wantOn("0.1.0", "A")
	fe := h.frontend()
	if fe == Digest("frontend") || h.w.Builds != 1 {
		t.Fatalf("frontend %s, %d builds", fe, h.w.Builds)
	}
	calls := h.w.CallLog()[before:]
	wantOrder(t, calls, "op image.build", "WriteEnv", "op stack.up", "Health")
	for _, c := range calls {
		if strings.HasPrefix(c, "op db.dump") || strings.HasPrefix(c, "op migrations.run") || strings.HasPrefix(c, "op stack.stop") {
			t.Fatalf("frontend settings ran %s", c)
		}
	}
	in := h.status().Installed
	if f := in.Frontend; f == nil || !maps.Equal(f.Overrides, map[string]string{"REACT_APP_TITLE": "Sunrise Clinic"}) ||
		f.Image == nil || f.Image.Digest != fe || f.Image.Repo != protocol.BuildRepoFrontend || f.Base.Digest != Digest("frontend") {
		t.Fatalf("installed frontend = %+v", in.Frontend)
	}
	if e := h.lastHistory(); e.Component != updates.ComponentFrontend || e.To != "REACT_APP_TITLE" || e.BackupID != "" {
		t.Fatalf("history = %+v", e)
	}
	got, err := h.c.FrontendSettings(ctx)
	if err != nil || !got.Rebuilt || got.Overrides["REACT_APP_TITLE"] != "Sunrise Clinic" || got.Release["REACT_DISABLE_PATIENT_LOGIN"] != "true" ||
		slices.Contains(got.Keys, "REACT_CARE_API_URL") || !slices.Contains(got.Keys, "REACT_APP_TITLE") {
		t.Fatalf("FrontendSettings = %+v, %v", got, err)
	}

	// The same settings again: nothing to do.
	if err := setFE(h, ctx, settings("REACT_APP_TITLE", "Sunrise Clinic")); err != nil {
		t.Fatal(err)
	}
	if h.w.Count("op image.build") != 1 {
		t.Fatal("rebuilt unchanged settings")
	}

	// A CARE update rebuilds the settings with the new release's builder.
	h.publish(spec{rev: 2, engines: []eng{{"0.1.0", 1}}, cares: []string{"A", "B"}, builders: true})
	h.check()
	h.mustApply()
	h.wantOn("0.1.0", "B")
	fe2 := h.frontend()
	if fe2 == fe || fe2 == Digest("frontend") || h.w.Builds != 2 || !strings.Contains(h.lastBuild(), "image.build") {
		t.Fatalf("after update: frontend %s (was %s), %d builds", fe2, fe, h.w.Builds)
	}
	if f := h.status().Installed.Frontend; f == nil || f.Image.Digest != fe2 || f.Overrides["REACT_APP_TITLE"] != "Sunrise Clinic" {
		t.Fatalf("installed frontend = %+v", f)
	}

	// Back to the release's settings: the published frontend.
	if err := setFE(h, ctx, settings()); err != nil {
		t.Fatal(err)
	}
	h.wantOn("0.1.0", "B")
	if h.frontend() != Digest("frontend") || h.status().Installed.Frontend != nil {
		t.Fatalf("frontend %s, installed %+v", h.frontend(), h.status().Installed.Frontend)
	}
}

func TestFrontendSettingsSurvivePluginChanges(t *testing.T) {
	h := newHarness(t, withKitEnv)
	h.installWithBuilders()
	ctx := context.Background()
	if err := h.c.SetPlugins(ctx, h.enable(custom)); err != nil {
		t.Fatal(err)
	}
	be, plugs := h.env()
	if err := setFE(h, ctx, settings("REACT_DEFAULT_ENCOUNTER_TYPE", "amb")); err != nil {
		t.Fatal(err)
	}
	fe := h.frontend()
	if be2, plugs2 := h.env(); be2 != be || plugs2 != plugs || fe == Digest("frontend") {
		t.Fatalf("frontend change touched the backend: %s %s (was %s %s)", be2, plugs2, be, plugs)
	}
	if p := h.status().Installed.Plugins; p.Image == nil || p.Image.Digest != be {
		t.Fatalf("installed plugins = %+v", p)
	}
	builds := h.w.Builds
	set, _ := plugins.Disable(h.enable(), "care_abdm")
	if err := h.c.SetPlugins(ctx, set); err != nil {
		t.Fatal(err)
	}
	if h.frontend() != fe || h.w.Builds != builds {
		t.Fatalf("plugin change rebuilt the frontend: %s (was %s)", h.frontend(), fe)
	}
	if f := h.status().Installed.Frontend; f == nil || f.Image.Digest != fe {
		t.Fatalf("installed frontend = %+v", f)
	}
}

func TestFrontendSettingsFailureRollsBack(t *testing.T) {
	for _, tc := range []struct {
		name, step string
		setup      func(h *harness)
		builds     int
	}{
		// A deterministic build failure is not retried.
		{name: "build failed", step: updates.StepBuildFrontend, setup: func(h *harness) { h.w.Unbuildable["REACT_APP_TITLE"] = true }, builds: 1},
		{name: "build transient", step: updates.StepBuildFrontend, setup: func(h *harness) { h.w.FailOn("op image.build", 3) }, builds: 3},
		{name: "health", step: updates.StepHealth, setup: func(h *harness) { h.w.BadSettings["REACT_APP_TITLE=Second"] = true }, builds: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, withKitEnv)
			h.installWithBuilders()
			ctx := context.Background()
			if err := setFE(h, ctx, settings("REACT_APP_TITLE", "First")); err != nil {
				t.Fatal(err)
			}
			fe := h.frontend()
			before := h.w.Count("op image.build")
			tc.setup(h)
			err := setFE(h, ctx, settings("REACT_APP_TITLE", "Second"))
			oe, ok := errors.AsType[*updates.OpError](err)
			if !ok || oe.Result != updates.ResultRolledBack || oe.Step != tc.step || oe.Component != updates.ComponentFrontend {
				t.Fatalf("err = %v, want rolled back at %s", err, tc.step)
			}
			if n := h.w.Count("op image.build") - before; n != tc.builds {
				t.Fatalf("%d build attempts, want %d", n, tc.builds)
			}
			h.wantOn("0.1.0", "A")
			if h.frontend() != fe {
				t.Fatalf("frontend %s, want %s", h.frontend(), fe)
			}
			if f := h.status().Installed.Frontend; f == nil || f.Overrides["REACT_APP_TITLE"] != "First" {
				t.Fatalf("installed frontend = %+v", f)
			}
		})
	}
}

func TestFrontendSettingsRejected(t *testing.T) {
	h := newHarness(t, withKitEnv)
	h.installWithBuilders()
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		in   map[string]string
		want string
	}{
		{"managed", settings("REACT_CARE_API_URL", "https://evil.example"), "managed"},
		{"unknown", settings("REACT_"+strings.Repeat("X", 61), "x"), "not a supported"},
		{"generic shell", settings("REACT_SENTRY_DSN", "$(id)"), "invalid value"},
		{"bad bool", settings("REACT_ENABLE_MINIMAL_PATIENT_REGISTRATION", "yes"), "invalid value"},
		{"injection", settings("REACT_APP_TITLE", "x\nRUN id"), "invalid value"},
		{"markup", settings("REACT_APP_TITLE", "<script>"), "invalid value"},
	} {
		err := setFE(h, ctx, tc.in)
		if se, ok := errors.AsType[*updates.SettingError](err); !ok || !strings.Contains(se.Error(), tc.want) {
			t.Errorf("%s: err = %v", tc.name, err)
		}
	}
	if h.w.Count("op image.build") != 0 || h.status().Journal != nil {
		t.Fatal("rejected settings ran")
	}
}

func TestFrontendSettingsNeedBuilderAndEnv(t *testing.T) {
	h := newHarness(t, withKitEnv)
	h.installCare("A") // no builders
	if err := setFE(h, context.Background(), settings("REACT_APP_TITLE", "x")); !errors.Is(err, updates.ErrNoFrontendBuilder) {
		t.Fatalf("err = %v", err)
	}
	h2 := newHarness(t)
	h2.installWithBuilders()
	if err := setFE(h2, context.Background(), settings("REACT_APP_TITLE", "x")); !errors.Is(err, updates.ErrFrontendEnvUnknown) {
		t.Fatalf("err = %v", err)
	}
	if h.status().Journal != nil || h2.status().Journal != nil {
		t.Fatal("journal left")
	}
}

func TestReinstallRebuildsFrontendSettings(t *testing.T) {
	h := newHarness(t, withKitEnv)
	h.installWithBuilders()
	ctx := context.Background()
	if err := setFE(h, ctx, settings("REACT_APP_TITLE", "Sunrise")); err != nil {
		t.Fatal(err)
	}
	fe := h.frontend()
	// A reinstall writes the published frontend.
	plugs := h.reinstall("")
	h.w.Lock()
	imgs := slices.Clone(h.w.EnvImages)
	h.w.Unlock()
	for i := range imgs {
		if imgs[i].Role == protocol.ImageRoleFrontend {
			imgs[i].Repo, imgs[i].Digest = "care-frontend", Digest("frontend")
		}
	}
	if err := h.w.WriteEnv(ctx, imgs, plugs); err != nil {
		t.Fatal(err)
	}
	if err := h.c.Reinstalled(ctx); err != nil {
		t.Fatal(err)
	}
	h.wantOn("0.1.0", "A")
	if h.frontend() != fe {
		t.Fatalf("frontend %s, want %s", h.frontend(), fe)
	}
	if f := h.status().Installed.Frontend; f == nil || f.Image.Digest != fe || h.status().Installed.Care == nil {
		t.Fatalf("installed = %+v", h.status().Installed)
	}
}

func TestPluginBuildFailsFast(t *testing.T) {
	h := newHarness(t)
	h.installWithBuilders()
	bad := plugins.Plugin{ID: "care_nope", Package: "git+https://github.com/example/care_nope.git", Version: "@main"}
	h.w.Unbuildable["care_nope"] = true
	err := h.c.SetPlugins(context.Background(), h.enable(bad))
	oe, ok := errors.AsType[*updates.OpError](err)
	if !ok || oe.Result != updates.ResultRolledBack || oe.Step != updates.StepBuild || !strings.Contains(err.Error(), "No matching distribution") {
		t.Fatalf("err = %v", err)
	}
	if !updates.Permanent(oe.Err) {
		t.Fatalf("not permanent: %v", oe.Err)
	}
	if n := h.w.Count("op image.build"); n != 1 {
		t.Fatalf("%d build attempts", n)
	}
	h.wantOn("0.1.0", "A")
	if e := h.lastHistory(); !strings.Contains(e.Error, "No matching distribution") {
		t.Fatalf("history = %+v", e)
	}
}

func TestStaleKitRefusedAtPreflight(t *testing.T) {
	h := newHarness(t, withKitEnv)
	h.installWithBuilders()
	h.w.KitErr = &stack.DroppedServicesError{Services: []protocol.Service{protocol.ServiceBackup}}
	before := len(h.w.Calls)
	_, err := h.c.SetFrontendSettings(context.Background(), settings("REACT_APP_TITLE", "Clinic"))
	oe, ok := errors.AsType[*updates.OpError](err)
	if !ok || oe.Result != updates.ResultRolledBack || oe.Step != updates.StepPreflight || !strings.Contains(err.Error(), "no backup service") {
		t.Fatalf("err = %v", err)
	}
	if !updates.Permanent(oe.Err) {
		t.Fatalf("not permanent: %v", oe.Err)
	}
	for _, c := range h.w.Calls[before:] {
		if strings.HasPrefix(c, "op ") || c == "WriteEnv" || c == "Backup" {
			t.Errorf("stale kit: %s was run", c)
		}
	}
	err = h.c.SetPlugins(context.Background(), h.enable(plugins.Plugin{ID: "care_x", Package: "care-x", Version: "==1.0"}))
	if oe, ok := errors.AsType[*updates.OpError](err); !ok || oe.Step != updates.StepPreflight {
		t.Fatalf("plugins err = %v", err)
	}
	if n := h.w.Count("Backup"); n != 0 {
		t.Fatalf("%d backups with a stale kit", n)
	}
	h.wantOn("0.1.0", "A")

	h.w.KitErr = nil
	if _, err := h.c.SetFrontendSettings(context.Background(), settings("REACT_APP_TITLE", "Clinic")); err != nil {
		t.Fatal(err)
	}
}
