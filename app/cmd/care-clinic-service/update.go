//go:build unix

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ohcnetwork/care_desktop/app/internal/feed"
	"github.com/ohcnetwork/care_desktop/app/internal/service/control"
	"github.com/ohcnetwork/care_desktop/app/internal/stack"
	"github.com/ohcnetwork/care_desktop/app/internal/updates"
)

const updateUsage = `usage: care-clinic-service ctl [--dev] update <command> [flags]

Runs the update coordinator in this process (see docs/updates.md). Nothing is
applied by check; apply installs the plan of the last verified feed.

commands:
  status             installed versions, verified revision, in-flight journal
  history            past operations
  check              fetch and verify the feed, plan, pre-download
  apply              apply the plan (stops at a desktop update)
  resume             continue an interrupted operation
  rollback           undo an interrupted operation or retry a failed rollback

flags:
  --feed url               updates.json URL (default $CARE_UPDATES_URL)
  --state dir              state directory (default <config>/care-clinic/updates)
  --desktop-version v      the installed desktop app's version (required)
  --deployments dir        deployment kit (default deployments)
  --origin https://host    extra origin the clinic is served on (repeatable)
`

var errUpdateUsage = errors.New("update usage")

func updateCtl(ctx context.Context, c *control.Client, args []string) (any, error) {
	if len(args) == 0 {
		return nil, errUpdateUsage
	}
	cmd, args := args[0], args[1:]
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	feedURL := fs.String("feed", os.Getenv("CARE_UPDATES_URL"), "")
	stateDir := fs.String("state", "", "")
	desktop := fs.String("desktop-version", "", "")
	kit := fs.String("deployments", "deployments", "")
	var origins listFlag
	fs.Var(&origins, "origin", "")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 || *desktop == "" {
		return nil, errUpdateUsage
	}
	if *stateDir == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return nil, err
		}
		*stateDir = filepath.Join(dir, "care-clinic", "updates")
	}
	keys, err := feed.DefaultKeys()
	if err != nil {
		return nil, err
	}
	clinicState, err := stack.DefaultStateDir()
	if err != nil {
		return nil, err
	}
	logf := func(s string) { fmt.Fprintln(os.Stderr, s) }
	cfg, err := c.Config(ctx)
	if err != nil {
		return nil, err
	}
	u, err := updates.Open(updates.Options{
		Dir: *stateDir, Feed: updates.HTTPFetcher{URL: *feedURL}, Keys: keys, AllowInsecureURLs: feed.DevBuild,
		Service: c, Agent: updates.ClientAgent{Control: c, Log: logf},
		Stack: updates.RuntimeStack{
			Runtime: &stack.Runtime{Service: c, StateDir: clinicState, Log: logf},
			Kit:     os.DirFS(*kit), Host: cfg.MDNSName + ".local", Origins: origins,
		},
		DesktopVersion: *desktop, Log: logf,
	})
	if err != nil {
		return nil, err
	}
	switch cmd {
	case "status":
		return u.Status()
	case "history":
		return u.History()
	case "check":
		if *feedURL == "" {
			return nil, errors.New("no feed URL: pass --feed or set CARE_UPDATES_URL")
		}
		return u.Check(ctx)
	}
	// The rest may write the stack env.
	if _, err := os.Stat(filepath.Join(*kit, "docker-compose.yml")); err != nil {
		return nil, fmt.Errorf("deployment kit: %w (pass --deployments)", err)
	}
	switch cmd {
	case "apply":
		res, err := u.Apply(ctx)
		if errors.Is(err, updates.ErrDesktopUpdate) {
			logf(fmt.Sprintf("Install the desktop app %s from %s, then apply again", res.Desktop.Version, res.Desktop.Path))
			return res, nil
		}
		return res, err
	case "resume":
		return nil, u.Resume(ctx)
	case "rollback":
		return nil, u.Rollback(ctx)
	}
	return nil, errUpdateUsage
}
