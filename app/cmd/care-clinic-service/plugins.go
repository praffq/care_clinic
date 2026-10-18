//go:build unix

package main

import (
	"context"
	"errors"
	"maps"

	"github.com/ohcnetwork/care_desktop/app/internal/plugins"
	"github.com/ohcnetwork/care_desktop/app/internal/service/control"
)

const pluginsUsage = `usage: care-clinic-service ctl [--dev] plugins <command> [flags] [args]

Changes the clinic's CARE backend plugins (see docs/plugins.md). Every
change is one journaled operation: back up, build the backend image when a
plugin is not bundled, write ADDITIONAL_PLUGS, migrate and health-check,
rolling back on failure. Flags come before the arguments.

commands:
  list                              enabled plugins (secrets redacted)
  catalog                           the presets
  enable <id> [KEY=value ...]       enable a preset, or change an enabled
                                    plugin's configs (merged with its current
                                    ones). Values are JSON, else strings.
  add <id> <package> [version] [KEY=value ...]
                                    enable any plugin: <id> is its Django app
                                    name, <package> a pip name or
                                    git+https://... URL, version e.g. ==1.2 or
                                    @main
  disable <id>                      disable a plugin

flags (as for ctl update):
  --feed url --state dir --deployments dir --force --origin url --images dir
  --desktop-version v       only checked by ctl update (default 0.0.0)
`

var errPluginsUsage = errors.New("plugins usage")

func pluginsCtl(ctx context.Context, c *control.Client, args []string) (any, error) {
	if len(args) == 0 {
		return nil, errPluginsUsage
	}
	cmd, args := args[0], args[1:]
	if cmd == "catalog" {
		if len(args) > 0 {
			return nil, errPluginsUsage
		}
		return plugins.Catalog(), nil
	}
	f := newCoordFlags("plugins")
	if err := f.fs.Parse(args); err != nil {
		return nil, errPluginsUsage
	}
	if *f.desktop == "" {
		*f.desktop = "0.0.0"
	}
	pos := f.fs.Args()
	if cmd != "list" {
		if err := f.needKit(); err != nil {
			return nil, err
		}
	}
	u, err := f.open(ctx, c)
	if err != nil {
		return nil, err
	}
	switch cmd {
	case "list":
		if len(pos) > 0 {
			return nil, errPluginsUsage
		}
		return u.Plugins(ctx)
	case "disable":
		if len(pos) != 1 {
			return nil, errPluginsUsage
		}
		return nil, u.DisablePlugin(ctx, pos[0])
	case "enable":
		if len(pos) < 1 {
			return nil, errPluginsUsage
		}
		p := plugins.Plugin{ID: pos[0]}
		cfg, err := parseConfigs(pos[1:])
		if err != nil {
			return nil, err
		}
		set, err := u.UnredactedPlugins(ctx)
		if err != nil {
			return nil, err
		}
		for _, q := range set {
			if q.ID == p.ID {
				p = q
				p.Configs = maps.Clone(q.Configs)
			}
		}
		if p.Configs == nil {
			p.Configs = map[string]any{}
		}
		maps.Copy(p.Configs, cfg)
		return nil, u.EnablePlugin(ctx, p)
	case "add":
		if len(pos) < 2 {
			return nil, errPluginsUsage
		}
		p := plugins.Plugin{ID: pos[0], Package: pos[1]}
		rest := pos[2:]
		if len(rest) > 0 && !isConfigArg(rest[0]) {
			p.Version, rest = rest[0], rest[1:]
		}
		cfg, err := parseConfigs(rest)
		if err != nil {
			return nil, err
		}
		p.Configs = cfg
		return nil, u.EnablePlugin(ctx, p)
	}
	return nil, errPluginsUsage
}

func isConfigArg(s string) bool {
	_, _, err := plugins.ParseConfig(s)
	return err == nil
}

func parseConfigs(args []string) (map[string]any, error) {
	out := map[string]any{}
	for _, a := range args {
		k, v, err := plugins.ParseConfig(a)
		if err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, nil
}
