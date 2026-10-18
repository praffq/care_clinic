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
  install <tarball> <sha256>
  activate <version>        switch to an installed engine and boot it
  start | stop | restart    control the engine VM
  prune                     delete slots other than current and previous
  agent-status              agent status through the relay
  agent-op <name> [json]    run an agent op through the relay (frames on stderr)
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
	nargs := map[string][]int{
		"install": {2, 2}, "activate": {1, 1}, "agent-op": {1, 2},
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
		v, err = c.Install(ctx, args[0], args[1])
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
	err = c.Agent().Op(ctx, op, &res, func(f protocol.Frame) {
		b, _ := json.Marshal(f)
		_, _ = fmt.Fprintf(os.Stderr, "%s\n", b)
	})
	if res == nil {
		res = json.RawMessage("null")
	}
	return res, err
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
