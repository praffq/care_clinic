//go:build unix

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
	"github.com/ohcnetwork/care_desktop/app/internal/service/control"
	"github.com/ohcnetwork/care_desktop/app/internal/stack"
)

const clinicUsage = `usage: care-clinic-service ctl [--dev] clinic <command> [flags]

Installs and drives the CARE stack in the engine without the desktop app.

commands:
  install --images <dir> [--deployments dir] [--backend care-backend-B]
          [--origin https://host ...] [--admin user --admin-password pw]
                     load the dev images (scripts/dev-images.sh), upload the
                     config for <mdns_name>.local, migrate and start
  up | stop          start (waits for healthy) or stop the stack
  migrate            run database migrations
  status             per-service state
  logs <service> [lines]
  superuser <user> <password>
`

func clinicCtl(ctx context.Context, c *control.Client, args []string) (any, error) {
	if len(args) == 0 {
		return nil, errClinicUsage
	}
	state, err := stack.DefaultStateDir()
	if err != nil {
		return nil, err
	}
	rt := &stack.Runtime{Service: c, StateDir: state, Log: func(s string) { fmt.Fprintln(os.Stderr, s) }}
	cmd, args := args[0], args[1:]
	switch cmd {
	case "install":
		return nil, clinicInstall(ctx, c, rt, args)
	case "up":
		return nil, rt.Up(ctx)
	case "stop":
		return nil, rt.Stop(ctx)
	case "migrate":
		return nil, rt.Migrate(ctx)
	case "status":
		return rt.Status(ctx)
	case "logs":
		if len(args) < 1 || len(args) > 2 {
			return nil, errClinicUsage
		}
		lines := 200
		if len(args) == 2 {
			if _, err := fmt.Sscan(args[1], &lines); err != nil {
				return nil, errClinicUsage
			}
		}
		return nil, rt.Logs(ctx, protocol.Service(args[0]), lines, os.Stdout)
	case "superuser":
		if len(args) != 2 {
			return nil, errClinicUsage
		}
		return nil, rt.CreateSuperuser(ctx, args[0], args[1])
	}
	return nil, errClinicUsage
}

var errClinicUsage = errors.New("clinic usage")

type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

func clinicInstall(ctx context.Context, c *control.Client, rt *stack.Runtime, args []string) error {
	fs := flag.NewFlagSet("clinic install", flag.ContinueOnError)
	images := fs.String("images", "", "directory written by scripts/dev-images.sh")
	kit := fs.String("deployments", "deployments", "deployment kit directory")
	backend := fs.String("backend", "", "backend tarball name, e.g. care-backend-B (default care-backend-A)")
	admin := fs.String("admin", "", "create this superuser after starting")
	adminPW := fs.String("admin-password", "", "password for --admin")
	var origins listFlag
	fs.Var(&origins, "origin", "extra https origin the clinic is served on (repeatable)")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 || *images == "" || (*admin == "") != (*adminPW == "") {
		return errClinicUsage
	}
	if _, err := os.Stat(*kit + "/docker-compose.yml"); err != nil {
		return fmt.Errorf("deployment kit: %w (pass --deployments)", err)
	}
	imgs, err := stack.DevImages(*images, map[protocol.ImageRole]string{protocol.ImageRoleBackend: *backend})
	if err != nil {
		return err
	}
	cfg, err := c.Config(ctx)
	if err != nil {
		return err
	}
	host := cfg.MDNSName + ".local"
	if err := rt.Install(ctx, os.DirFS(*kit), host, origins, imgs); err != nil {
		return err
	}
	if *admin != "" {
		err := rt.CreateSuperuser(ctx, *admin, *adminPW)
		if errors.Is(err, stack.ErrUserExists) {
			rt.Log("Superuser " + *admin + " already exists")
		} else if err != nil {
			return err
		}
	}
	rt.Log("CARE is running at https://" + host)
	return nil
}
