//go:build unix

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
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
  --images dir             scripts/dev-images.sh output with care-backend-builder,
                           used to build custom plugins when the feed has no
                           builder (default $CARE_CLINIC_DEV_IMAGES)
`

var errUpdateUsage = errors.New("update usage")

// coordFlags are the flags every command that opens the coordinator takes.
type coordFlags struct {
	fs                         *flag.FlagSet
	feedURL, stateDir, desktop *string
	kit, images                *string
	origins                    listFlag
}

func newCoordFlags(name string) *coordFlags {
	f := &coordFlags{fs: flag.NewFlagSet(name, flag.ContinueOnError)}
	f.feedURL = f.fs.String("feed", os.Getenv("CARE_UPDATES_URL"), "")
	f.stateDir = f.fs.String("state", "", "")
	f.desktop = f.fs.String("desktop-version", "", "")
	f.kit = f.fs.String("deployments", "deployments", "")
	f.images = f.fs.String("images", os.Getenv("CARE_CLINIC_DEV_IMAGES"), "")
	f.fs.Var(&f.origins, "origin", "")
	return f
}

// open opens the update coordinator over the service's control client.
func (f *coordFlags) open(ctx context.Context, c *control.Client) (*updates.Coordinator, error) {
	if *f.stateDir == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return nil, err
		}
		*f.stateDir = filepath.Join(dir, "care-clinic", "updates")
	}
	keys, err := feed.DefaultKeys()
	if err != nil {
		return nil, err
	}
	clinicState, err := stack.DefaultStateDir()
	if err != nil {
		return nil, err
	}
	var devBuilders []stack.Image
	if *f.images != "" {
		if devBuilders, err = stack.DevBuilders(*f.images); err != nil {
			return nil, err
		}
	}
	feEnv, err := kitFrontendEnv(os.DirFS(*f.kit))
	if err != nil {
		return nil, err
	}
	logf := func(s string) { fmt.Fprintln(os.Stderr, s) }
	cfg, err := c.Config(ctx)
	if err != nil {
		return nil, err
	}
	return updates.Open(updates.Options{
		Dir: *f.stateDir, Feed: updates.HTTPFetcher{URL: *f.feedURL}, Keys: keys, AllowInsecureURLs: feed.DevBuild,
		AllowUnsigned: feed.UnsignedFeedAllowed(),
		Service:       c, Agent: updates.ClientAgent{Control: c, Log: logf},
		Stack: updates.RuntimeStack{
			Runtime: &stack.Runtime{Service: c, StateDir: clinicState, Log: logf},
			Kit:     os.DirFS(*f.kit), Host: cfg.MDNSName + ".local", Origins: f.origins,
		},
		DesktopVersion: *f.desktop, DevBuilders: devBuilders, FrontendEnv: feEnv, Log: logf,
	})
}

// kitFrontendEnv reads the kit's frontend.env (none: nil).
func kitFrontendEnv(kit fs.FS) (map[string]string, error) {
	data, err := fs.ReadFile(kit, "frontend.env")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	env, err := updates.ParseFrontendEnv(data)
	if err != nil {
		return nil, fmt.Errorf("frontend.env: %w", err)
	}
	return env, nil
}

// needKit fails unless the deployment kit is there (commands that write
// the stack env need it).
func (f *coordFlags) needKit() error {
	if _, err := os.Stat(filepath.Join(*f.kit, "docker-compose.yml")); err != nil {
		return fmt.Errorf("deployment kit: %w (pass --deployments)", err)
	}
	return nil
}

func updateCtl(ctx context.Context, c *control.Client, args []string) (any, error) {
	if len(args) == 0 {
		return nil, errUpdateUsage
	}
	cmd, args := args[0], args[1:]
	f := newCoordFlags("update")
	if err := f.fs.Parse(args); err != nil || f.fs.NArg() > 0 || *f.desktop == "" {
		return nil, errUpdateUsage
	}
	feedURL := f.feedURL
	u, err := f.open(ctx, c)
	if err != nil {
		return nil, err
	}
	logf := func(s string) { fmt.Fprintln(os.Stderr, s) }
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
	if err := f.needKit(); err != nil {
		return nil, err
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
