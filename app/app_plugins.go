package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/ohcnetwork/care_desktop/app/internal/feed"
	"github.com/ohcnetwork/care_desktop/app/internal/plugins"
	"github.com/ohcnetwork/care_desktop/app/internal/service/control"
	"github.com/ohcnetwork/care_desktop/app/internal/stack"
	"github.com/ohcnetwork/care_desktop/app/internal/updates"
)

// pluginsJob is the job label of a plugin change (care-done).
const pluginsJob = "plugins"

// PluginCatalog lists the plugin presets.
func (a *App) PluginCatalog() []plugins.Preset { return plugins.Catalog() }

// FrontendPlugins lists the catalog frontend plugins; required ones are
// registered in CARE at every clinic install.
func (a *App) FrontendPlugins() []plugins.FrontendPlugin { return plugins.FrontendCatalog() }

// ListPlugins returns the clinic's backend plugins, secrets redacted.
func (a *App) ListPlugins() (list []plugins.Plugin, err error) {
	defer a.logError(&err)
	err = a.withReadJob(func() error {
		if err := a.requireSetup(); err != nil {
			return err
		}
		u, err := a.pluginCoordinator(a.clientContext())
		if err != nil {
			return err
		}
		list, err = u.Plugins(a.clientContext())
		return err
	})
	return list, err
}

// EnablePlugin enables p (a preset ID with configs, or any plugin with its
// package) or changes an enabled plugin, as a background job (care-done
// "plugins"). A secret
// config sent back redacted keeps its stored value.
func (a *App) EnablePlugin(p plugins.Plugin, adminPassword string) (err error) {
	defer a.logError(&err)
	if err := a.requireAdmin(adminPassword); err != nil {
		return err
	}
	if err := checkPlugin(p); err != nil {
		return err
	}
	return a.run(func() error {
		return a.changePlugins(func(ctx context.Context, u *updates.Coordinator) error {
			a.logln("Enabling the CARE plugin " + p.ID + "...")
			return u.EnablePlugin(ctx, p)
		})
	}, false, pluginsJob)
}

// DisablePlugin disables the plugin id as a background job.
func (a *App) DisablePlugin(id, adminPassword string) (err error) {
	defer a.logError(&err)
	if err := a.requireAdmin(adminPassword); err != nil {
		return err
	}
	return a.run(func() error {
		return a.changePlugins(func(ctx context.Context, u *updates.Coordinator) error {
			a.logln("Disabling the CARE plugin " + id + "...")
			return u.DisablePlugin(ctx, id)
		})
	}, false, pluginsJob)
}

// checkPlugin rejects what the change would reject, before starting a job.
func checkPlugin(p plugins.Plugin) error {
	_, err := plugins.Enable(nil, p)
	return err
}

func (a *App) changePlugins(fn func(context.Context, *updates.Coordinator) error) error {
	if err := a.requireSetup(); err != nil {
		return err
	}
	ctx := a.clientContext()
	u, err := a.pluginCoordinator(ctx)
	if err != nil {
		return err
	}
	return fn(ctx, u)
}

// pluginCoordinator opens the update coordinator, which runs plugin changes
// as journaled operations (shared with ctl plugins and ctl update).
func (a *App) pluginCoordinator(ctx context.Context) (*updates.Coordinator, error) {
	c, ok := a.svc().(*control.Client)
	if !ok {
		return nil, errors.New("plugins need the CARE Clinic service")
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return nil, err
	}
	keys, err := feed.DefaultKeys()
	if err != nil {
		return nil, err
	}
	clinicState := a.stateDir
	if clinicState == "" {
		if clinicState, err = stack.DefaultStateDir(); err != nil {
			return nil, err
		}
	}
	var devBuilder *stack.Image
	if images := os.Getenv("CARE_CLINIC_DEV_IMAGES"); images != "" {
		b, err := stack.DevBuilders(images)
		if err != nil {
			return nil, err
		}
		devBuilder = updates.DevBuilder(b)
	}
	return updates.Open(updates.Options{
		Dir: filepath.Join(dir, "care-clinic", "updates"), Keys: keys, AllowInsecureURLs: feed.DevBuild,
		AllowUnsigned: feed.UnsignedFeedAllowed(),
		Feed:          updates.HTTPFetcher{URL: os.Getenv("CARE_UPDATES_URL")},
		Service:       c, Agent: updates.ClientAgent{Control: c, Log: a.logln},
		Stack: updates.RuntimeStack{
			Runtime: &stack.Runtime{Service: c, StateDir: clinicState, Log: a.logln},
			Kit:     os.DirFS(a.installDir()), Host: a.engine().Host(),
		},
		DesktopVersion: a.desktopVersion(), DevBuilder: devBuilder, Log: a.logln,
	})
}

func (a *App) desktopVersion() string {
	if a.pins != nil && a.pins.AppVersion != "" {
		return a.pins.AppVersion
	}
	return "0.0.0"
}
