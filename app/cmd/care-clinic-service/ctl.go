//go:build unix

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
	"github.com/ohcnetwork/care_desktop/app/internal/service/api"
	"github.com/ohcnetwork/care_desktop/app/internal/service/control"
)

const ctlUsage = `usage: care-clinic-service ctl [--dev] [--root dir] [--socket path] <command>

commands:
  status                    service and engine status
  watch                     stream status changes
  slots                     installed engine slots
  install <tarball> <sha256> upload and install an engine bundle
  activate <version>        switch to an installed engine and boot it
  start | stop | restart    control the engine VM
  prune                     delete slots other than current and previous
  engine prune [flags]      remove unused docker images in the engine VM
                            (ctl engine for help)
  config [key=value ...]    show or change the service configuration (config.json):
                              mdns_name=<name>         advertise <name>.local
                              proxy_protocol=true|false PROXY v2 to the guest
                              backup_dir=<abs path>    where backups are copied out to
                              backup_copy_dir=<abs path> extra copy (USB/share); empty turns it off
  agent-status              agent status through the relay
  agent-op <name> [json]    run an agent op through the relay (frames on stderr)
  clinic <command>          install and drive the CARE stack (ctl clinic for help)
  backup <command>          run | list | status | copy | restore (ctl backup for help)
  update <command>          check for and apply updates (ctl update for help)
  plugins <command>         list, enable, add or disable CARE backend plugins
                            (ctl plugins for help)
  frontend <command>        show, set, unset or reset CARE frontend (REACT_*)
                            settings (ctl frontend for help)
`

func ctl(args []string) int {
	fs := flag.NewFlagSet("ctl", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, ctlUsage) }
	dev := fs.Bool("dev", false, "use the --dev service's socket")
	root := fs.String("root", "", "the service's --root")
	socket := fs.String("socket", "", "control socket")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() == 0 {
		fs.Usage()
		return 2
	}
	_, sock, err := paths(*dev, *root, *socket)
	if err == nil {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		err = runCtl(ctx, control.NewClient(sock), fs.Args())
	}
	if errors.Is(err, errClinicUsage) {
		fmt.Fprint(os.Stderr, clinicUsage)
		return 2
	}
	if errors.Is(err, errBackupUsage) {
		fmt.Fprint(os.Stderr, backupUsage)
		return 2
	}
	if errors.Is(err, errUpdateUsage) {
		fmt.Fprint(os.Stderr, updateUsage)
		return 2
	}
	if errors.Is(err, errFrontendUsage) {
		fmt.Fprint(os.Stderr, frontendUsage)
		return 2
	}
	if errors.Is(err, errEngineUsage) {
		fmt.Fprint(os.Stderr, engineUsage)
		return 2
	}
	if errors.Is(err, errPluginsUsage) {
		fmt.Fprint(os.Stderr, pluginsUsage)
		return 2
	}
	if errors.Is(err, errUsage) {
		fs.Usage()
		return 2
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ctl:", err)
		return 1
	}
	return 0
}

var errUsage = errors.New("usage")

func runCtl(ctx context.Context, c *control.Client, args []string) error {
	cmd, args := args[0], args[1:]
	if cmd == "clinic" || cmd == "backup" || cmd == "update" || cmd == "plugins" || cmd == "frontend" || cmd == "engine" {
		sub := clinicCtl
		switch cmd {
		case "engine":
			sub = engineCtl
		case "backup":
			sub = backupCtl
		case "update":
			sub = updateCtl
		case "plugins":
			sub = pluginsCtl
		case "frontend":
			sub = frontendCtl
		}
		v, err := sub(ctx, c, args)
		if err != nil || v == nil {
			return err
		}
		return printJSON(v)
	}
	nargs := map[string][]int{
		"install": {2, 2}, "activate": {1, 1}, "agent-op": {1, 2}, "config": {0, 4},
	}[cmd]
	if nargs == nil {
		nargs = []int{0, 0}
	}
	if len(args) < nargs[0] || len(args) > nargs[1] {
		return errUsage
	}
	var v any
	var err error
	switch cmd {
	case "status":
		v, err = c.Status(ctx)
	case "watch":
		return c.Watch(ctx, func(st api.ServiceStatus) { _ = printJSON(st) })
	case "slots":
		v, err = c.Slots(ctx)
	case "install":
		v, err = install(ctx, c, args[0], args[1])
	case "activate":
		v, err = c.Activate(ctx, args[0])
	case "start":
		v, err = c.Start(ctx)
	case "stop":
		v, err = c.Stop(ctx)
	case "restart":
		v, err = c.Restart(ctx)
	case "prune":
		v, err = c.Prune(ctx)
	case "config":
		if len(args) == 0 {
			v, err = c.Config(ctx)
			break
		}
		var u api.ServiceConfigUpdate
		if u, err = configUpdate(args); err == nil {
			v, err = c.UpdateConfig(ctx, u)
		}
	case "agent-status":
		v, err = c.Agent().Status(ctx)
	case "agent-op":
		v, err = agentOp(ctx, c, args)
	default:
		return errUsage
	}
	if err != nil {
		return err
	}
	return printJSON(v)
}

func configUpdate(args []string) (api.ServiceConfigUpdate, error) {
	var u api.ServiceConfigUpdate
	for _, a := range args {
		key, val, _ := strings.Cut(a, "=")
		switch key {
		case "mdns_name":
			u.MDNSName = &val
		case "proxy_protocol":
			on, err := strconv.ParseBool(val)
			if err != nil {
				return u, fmt.Errorf("proxy_protocol: %w", err)
			}
			u.ProxyProtocol = &on
		case "backup_dir":
			u.BackupDir = &val
		case "backup_copy_dir":
			u.BackupCopyDir = &val
		default:
			return u, fmt.Errorf("unknown config key %q", key)
		}
	}
	return u, nil
}

func agentOp(ctx context.Context, c *control.Client, args []string) (any, error) {
	body := "{}"
	if len(args) > 1 {
		body = args[1]
	}
	op, err := protocol.DecodeOp(args[0], strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	var res json.RawMessage
	err = c.Agent().Op(ctx, op, &res, printFrame)
	if res == nil {
		res = json.RawMessage("null")
	}
	return res, err
}

func install(ctx context.Context, c *control.Client, path, sha256 string) (any, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	return c.InstallEngine(ctx, f, st.Size(), sha256)
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
