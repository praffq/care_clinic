//go:build unix

package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"

	"github.com/ohcnetwork/care_desktop/app/internal/service/control"
)

const frontendUsage = `usage: care-clinic-service ctl [--dev] frontend <command> [flags] [args]

Changes the clinic's CARE frontend settings (REACT_*, see docs/plugins.md).
CARE's frontend has its settings built in: when the clinic's differ from the
release's, CARE Clinic rebuilds the frontend from the release's builder image
as one journaled operation, rolling back on failure. Flags come before the
arguments.

commands:
  show                       the release's settings, the clinic's overrides
                             and whether the frontend is rebuilt
  keys                       the settings a clinic may override
  set KEY=value ...          override settings (merged with the current ones)
  unset KEY ...              drop overrides (back to the release's values)
  reset                      drop every override (the published frontend)

flags (as for ctl update):
  --feed url --state dir --deployments dir --force --origin url --images dir
  --desktop-version v       only checked by ctl update (default 0.0.0)
`

var errFrontendUsage = errors.New("frontend usage")

func frontendCtl(ctx context.Context, c *control.Client, args []string) (any, error) {
	if len(args) == 0 {
		return nil, errFrontendUsage
	}
	cmd, args := args[0], args[1:]
	f := newCoordFlags("frontend")
	if err := f.fs.Parse(args); err != nil {
		return nil, errFrontendUsage
	}
	if *f.desktop == "" {
		*f.desktop = "0.0.0"
	}
	pos := f.fs.Args()
	switch cmd {
	case "show", "keys", "reset":
		if len(pos) > 0 {
			return nil, errFrontendUsage
		}
	case "set", "unset":
		if len(pos) == 0 {
			return nil, errFrontendUsage
		}
	default:
		return nil, errFrontendUsage
	}
	if cmd != "show" && cmd != "keys" {
		if err := f.needKit(); err != nil {
			return nil, err
		}
	}
	u, err := f.open(ctx, c)
	if err != nil {
		return nil, err
	}
	cur, err := u.FrontendSettings(ctx)
	if err != nil {
		return nil, err
	}
	switch cmd {
	case "show":
		return cur, nil
	case "keys":
		return cur.Keys, nil
	}
	next := maps.Clone(cur.Overrides)
	if next == nil {
		next = map[string]string{}
	}
	switch cmd {
	case "set":
		for _, a := range pos {
			k, v, ok := strings.Cut(a, "=")
			if !ok || k == "" {
				return nil, fmt.Errorf("%q: want KEY=value", a)
			}
			next[k] = v
		}
	case "unset":
		for _, k := range pos {
			if _, ok := next[k]; !ok {
				return nil, fmt.Errorf("%s is not overridden", k)
			}
			delete(next, k)
		}
	case "reset":
		clear(next)
	}
	changed, err := u.SetFrontendSettings(ctx, next)
	if err != nil {
		return nil, err
	}
	return map[string]bool{"changed": changed}, nil
}
