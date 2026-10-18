package updates_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
	"github.com/ohcnetwork/care_desktop/app/internal/plugins"
	"github.com/ohcnetwork/care_desktop/app/internal/stack"
	"github.com/ohcnetwork/care_desktop/app/internal/updates"
	"github.com/ohcnetwork/care_desktop/app/internal/updates/updatestest"
)

var (
	filly  = plugins.Plugin{ID: "care_filly", Configs: map[string]any{"MEDISPEAK_API_KEY": "sk-secret", "MEDISPEAK_BASE_URL": "https://m"}}
	custom = plugins.Plugin{ID: "care_abdm", Package: "git+https://github.com/ohcnetwork/care_abdm.git", Version: "@main"}
)

// installWithBuilders installs engine 0.1.0 and CARE A from a feed whose
// releases publish backend builders.
func (h *harness) installWithBuilders() {
	h.t.Helper()
	h.publish(spec{rev: 1, engines: []eng{{"0.1.0", 1}}, cares: []string{"A"}, builders: true})
	h.check()
	h.mustApply()
	h.wantOn("0.1.0", "A")
}

func (h *harness) enable(ps ...plugins.Plugin) []plugins.Plugin {
	h.t.Helper()
	set, err := h.c.UnredactedPlugins(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	for _, p := range ps {
		if set, err = plugins.Enable(set, p); err != nil {
			h.t.Fatal(err)
		}
	}
	return set
}

func (h *harness) env() (protocol.Digest, string) {
	h.w.Lock()
	defer h.w.Unlock()
	return h.w.Env[protocol.ImageRoleBackend], h.w.Plugs
}

func TestPluginsDefaultAtInstall(t *testing.T) {
	h := newHarness(t)
	h.installCare("A")
	_, plugs := h.env()
	if !strings.Contains(plugs, `"name":"care_multihost_files_be"`) {
		t.Fatalf("plugs = %s", plugs)
	}
	p := h.status().Installed.Plugins
	if p == nil || plugins.Summary(p.Set) != "care_multihost_files_be" || p.Image != nil || p.Base.Digest != Digest("backend-A") {
		t.Fatalf("installed plugins = %+v", p)
	}
}

func TestPluginsBundledNeedNoBuild(t *testing.T) {
	h := newHarness(t)
	h.installCare("A")
	before := len(h.w.CallLog())
	if err := h.c.SetPlugins(context.Background(), h.enable(filly)); err != nil {
		t.Fatal(err)
	}
	h.wantOn("0.1.0", "A")
	calls := h.w.CallLog()[before:]
	if h.w.Count("op image.build") != 0 {
		t.Fatalf("built for bundled plugins: %q", calls)
	}
	wantOrder(t, calls, "op db.dump", "op stack.stop", "WriteEnv", "op migrations.run", "op stack.up", "Health")
	if _, plugs := h.env(); !strings.Contains(plugs, "sk-secret") || !strings.Contains(plugs, `"name":"care_filly"`) {
		t.Fatalf("plugs = %s", plugs)
	}
	e := h.lastHistory()
	if e.Component != updates.ComponentPlugins || e.To != "care_filly,care_multihost_files_be" || e.BackupID == "" {
		t.Fatalf("history = %+v", e)
	}
	// Secrets stay in installed.json (0600) but never in Status.
	st := h.status()
	if got := st.Installed.Plugins.Set[1].Configs["MEDISPEAK_API_KEY"]; got != "********" {
		t.Fatalf("status shows %v", got)
	}
	if b, _ := os.ReadFile(filepath.Join(h.dir, updates.InstalledFile)); !strings.Contains(string(b), "sk-secret") {
		t.Fatal("installed.json lost the secret")
	}
	if ps, _ := h.c.Plugins(context.Background()); ps[1].Configs["MEDISPEAK_API_KEY"] != "********" {
		t.Fatalf("Plugins = %+v", ps)
	}
}

func TestPluginsCustomBuildThenRebuildOnCareUpdate(t *testing.T) {
	h := newHarness(t)
	h.installWithBuilders()
	if err := h.c.SetPlugins(context.Background(), h.enable(custom)); err != nil {
		t.Fatal(err)
	}
	be, plugs := h.env()
	if be == Digest("backend-A") || h.w.Builds != 1 || !strings.Contains(plugs, "care_abdm") {
		t.Fatalf("backend %s, %d builds, plugs %s", be, h.w.Builds, plugs)
	}
	if err := h.w.Healthy(); err != nil {
		t.Fatal(err)
	}
	p := h.status().Installed.Plugins
	if p.Image == nil || p.Image.Digest != be || p.Image.Repo != protocol.BuildRepoBackend || p.Base.Digest != Digest("backend-A") {
		t.Fatalf("installed plugins = %+v", p)
	}

	// A CARE update rebuilds the plugins on the new backend.
	h.publish(spec{rev: 2, engines: []eng{{"0.1.0", 1}}, cares: []string{"A", "B"}, builders: true})
	h.check()
	h.mustApply()
	be2, plugs2 := h.env()
	if be2 == be || be2 == Digest("backend-B") || h.w.Builds != 2 || plugs2 != plugs {
		t.Fatalf("after update: backend %s (was %s), %d builds", be2, be, h.w.Builds)
	}
	if err := h.w.Healthy(); err != nil {
		t.Fatal(err)
	}
	st := h.status()
	if st.Installed.Care.Version != careVersion("B") || st.Installed.Plugins.Base.Digest != Digest("backend-B") || st.Installed.Plugins.Image.Digest != be2 {
		t.Fatalf("installed = %+v %+v", st.Installed.Care, st.Installed.Plugins)
	}
	// The previous derived image is kept for rollback.
	if !h.w.Images[be] {
		t.Fatal("previous derived image removed")
	}

	// Disabling the custom plugin goes back to the release backend.
	set, _ := plugins.Disable(h.enable(), "care_abdm")
	if err := h.c.SetPlugins(context.Background(), set); err != nil {
		t.Fatal(err)
	}
	h.wantOn("0.1.0", "B")
}

func TestPluginsCustomNeedBuilder(t *testing.T) {
	h := newHarness(t)
	h.installCare("A")
	before := len(h.w.CallLog())
	err := h.c.SetPlugins(context.Background(), h.enable(custom))
	if !errors.Is(err, updates.ErrNoBuilder) {
		t.Fatalf("err = %v", err)
	}
	if calls := h.w.CallLog()[before:]; slices.ContainsFunc(calls, func(c string) bool { return strings.HasPrefix(c, "op ") }) {
		t.Fatalf("ran ops: %q", calls)
	}
	if h.status().Journal != nil {
		t.Fatal("journal left")
	}
}

func TestPluginsDevBuilder(t *testing.T) {
	tar := filepath.Join(t.TempDir(), "care-backend-builder.tar")
	if err := os.WriteFile(tar, updatestest.ImageTar("care-clinic-dev/care-backend-builder"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, func(o *updates.Options) {
		o.DevBuilders = []stack.Image{{Role: protocol.ImageRoleBackendBuilder, Repo: "care-clinic-dev/care-backend-builder", Digest: Digest("dev-builder"), Tarball: tar}}
	})
	h.installCare("A")
	if err := h.c.SetPlugins(context.Background(), h.enable(custom)); err != nil {
		t.Fatal(err)
	}
	if h.w.Builds != 1 || !h.w.Images[Digest("dev-builder")] {
		t.Fatalf("builds %d", h.w.Builds)
	}
}

func TestPluginsFailureRollsBack(t *testing.T) {
	bad := plugins.Plugin{ID: "care_bad", Package: "care-bad", Version: "==1.0"}
	for _, tc := range []struct {
		name             string
		step             string
		setup            func(h *harness)
		stopped, restore bool
	}{
		{name: "backup", step: updates.StepBackup, setup: func(h *harness) { h.w.FailOn("op db.dump", 3) }},
		{name: "build", step: updates.StepBuild, setup: func(h *harness) { h.w.FailOn("op image.build", 3) }},
		{name: "migrate", step: updates.StepMigrate, setup: func(h *harness) { h.w.FailOn("op migrations.run", 3) }, stopped: true, restore: true},
		{name: "health", step: updates.StepHealth, setup: func(h *harness) { h.w.BadPlugins["care_bad"] = true }, stopped: true, restore: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			h.installWithBuilders()
			_, plugsBefore := h.env()
			before := len(h.w.CallLog())
			tc.setup(h)
			err := h.c.SetPlugins(context.Background(), h.enable(bad))
			oe, ok := errors.AsType[*updates.OpError](err)
			if !ok || oe.Result != updates.ResultRolledBack || oe.Step != tc.step || oe.Component != updates.ComponentPlugins {
				t.Fatalf("err = %v, want rolled back at %s", err, tc.step)
			}
			h.wantOn("0.1.0", "A")
			if _, plugs := h.env(); plugs != plugsBefore {
				t.Fatalf("plugs = %s, want %s", plugs, plugsBefore)
			}
			if p := h.status().Installed.Plugins; plugins.Summary(p.Set) != "care_multihost_files_be" || p.Image != nil {
				t.Fatalf("installed plugins = %+v", p)
			}
			calls := h.w.CallLog()[before:]
			has := func(p string) bool {
				return slices.ContainsFunc(calls, func(c string) bool { return strings.HasPrefix(c, p) })
			}
			if has("op db.restore") != tc.restore || has("op stack.stop") != tc.stopped {
				t.Fatalf("calls %q", calls)
			}
		})
	}
}

func TestPluginsInvalidSetRejected(t *testing.T) {
	h := newHarness(t)
	h.installCare("A")
	err := h.c.SetPlugins(context.Background(), []plugins.Plugin{{ID: "care_filly", Package: "care-filly"}})
	if err == nil || !strings.Contains(err.Error(), "MEDISPEAK_API_KEY") {
		t.Fatalf("err = %v", err)
	}
	if err := h.c.SetPlugins(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if _, plugs := h.env(); plugs != "[]" {
		t.Fatalf("plugs = %s", plugs)
	}
}

func TestPluginsNeedInstalledCare(t *testing.T) {
	h := newHarness(t)
	if err := h.c.SetPlugins(context.Background(), nil); !errors.Is(err, updates.ErrNotInstalled) {
		t.Fatalf("err = %v", err)
	}
}

// reinstall simulates ctl clinic install / onboarding: the stack env is
// rewritten with images renamed by suffix (none: the same images) and the
// plugins InstallPlugs returns, then migrated and started.
func (h *harness) reinstall(suffix string) string {
	h.t.Helper()
	plugs, err := h.c.InstallPlugs()
	if err != nil {
		h.t.Fatal(err)
	}
	h.w.Lock()
	imgs := slices.Clone(h.w.EnvImages)
	h.w.Unlock()
	var be protocol.Digest
	for i := range imgs {
		switch imgs[i].Role {
		case protocol.ImageRoleBackend:
			if suffix != "" {
				imgs[i].Digest = Digest("backend-" + suffix)
			} else if p := h.status().Installed.Plugins; p != nil {
				imgs[i].Repo, imgs[i].Digest = p.Base.Repo, p.Base.Digest
			}
			be = imgs[i].Digest
		case protocol.ImageRoleFrontend:
			if suffix != "" {
				imgs[i].Digest = Digest("frontend-" + suffix)
			}
		}
	}
	if err := h.w.WriteEnv(context.Background(), imgs, plugs); err != nil {
		h.t.Fatal(err)
	}
	h.w.Lock()
	for _, i := range imgs {
		h.w.Images[i.Digest] = true
	}
	h.w.DBSchema = be
	h.w.Unlock()
	return plugs
}

func TestReinstallRebuildsCustomPlugins(t *testing.T) {
	h := newHarness(t)
	h.installWithBuilders()
	if err := h.c.SetPlugins(context.Background(), h.enable(custom)); err != nil {
		t.Fatal(err)
	}
	if plugs := h.reinstall(""); strings.Contains(plugs, "care_abdm") || !strings.Contains(plugs, "care_multihost_files_be") {
		t.Fatalf("install plugs = %s", plugs)
	}
	if err := h.c.Reinstalled(context.Background()); err != nil {
		t.Fatal(err)
	}
	be, plugs := h.env()
	if be == Digest("backend-A") || !strings.Contains(plugs, "care_abdm") {
		t.Fatalf("backend %s, plugs %s", be, plugs)
	}
	if err := h.w.Healthy(); err != nil {
		t.Fatal(err)
	}
	st := h.status()
	if p := st.Installed.Plugins; p.Image == nil || p.Image.Digest != be || p.Base.Digest != Digest("backend-A") || st.Installed.Care == nil {
		t.Fatalf("installed = %+v", st.Installed)
	}
}

func TestReinstallFromOtherImagesKeepsBundledPlugins(t *testing.T) {
	h := newHarness(t)
	h.installCare("A")
	if err := h.c.SetPlugins(context.Background(), h.enable(filly)); err != nil {
		t.Fatal(err)
	}
	history := h.lastHistory().ID
	if plugs := h.reinstall("dev"); !strings.Contains(plugs, "sk-secret") {
		t.Fatalf("install plugs = %s", plugs)
	}
	if err := h.c.Reinstalled(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := h.status()
	if st.Installed.Care != nil || st.Installed.Plugins.Base.Digest != Digest("backend-dev") || st.Installed.Plugins.Image != nil {
		t.Fatalf("installed = %+v", st.Installed)
	}
	if plugins.Summary(st.Installed.Plugins.Set) != "care_filly,care_multihost_files_be" || h.lastHistory().ID != history {
		t.Fatalf("plugins %s, history %s", plugins.Summary(st.Installed.Plugins.Set), h.lastHistory().ID)
	}
	// Later changes run on the reinstalled images.
	set, _ := plugins.Disable(h.enable(), "care_filly")
	if err := h.c.SetPlugins(context.Background(), set); err != nil {
		t.Fatal(err)
	}
	if be, _ := h.env(); be != Digest("backend-dev") {
		t.Fatalf("backend %s", be)
	}
}

func TestReinstalledInPlaceIsNoop(t *testing.T) {
	h := newHarness(t)
	h.installCare("A")
	before := h.status().Installed
	if err := h.c.Reinstalled(context.Background()); err != nil {
		t.Fatal(err)
	}
	if after := h.status().Installed; !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("installed.json rewritten: %+v", after)
	}
}
