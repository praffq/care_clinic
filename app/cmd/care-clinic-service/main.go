//go:build unix

// Command care-clinic-service is the root host service that owns the CARE
// engine VM: it keeps the active engine slot running (restarting it after
// crashes) and serves the control API the app uses, on a Unix socket.
//
//	care-clinic-service [flags]                 run the service
//	care-clinic-service ctl [flags] <command>   talk to a running service
//
// --dev runs as the current user with state under the user's Library and a
// socket only that user may open, for local testing.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/vm"
	"github.com/ohcnetwork/care_desktop/app/internal/service/control"
	"github.com/ohcnetwork/care_desktop/app/internal/service/slots"
	"github.com/ohcnetwork/care_desktop/app/internal/service/supervisor"
)

const (
	systemRoot   = "/Library/Application Support/care-clinic"
	systemSocket = "/var/run/care-clinic/control.sock"
	devRootName  = "care-clinic-dev"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "ctl" {
		os.Exit(ctl(os.Args[2:]))
	}
	if err := serve(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "care-clinic-service:", err)
		os.Exit(1)
	}
}

// paths returns the state root and control socket for the given flags.
func paths(dev bool, root, socket string) (string, string, error) {
	if root == "" {
		root = systemRoot
		if dev {
			dir, err := os.UserConfigDir()
			if err != nil {
				return "", "", err
			}
			root = filepath.Join(dir, devRootName)
		}
	}
	if socket == "" {
		socket = systemSocket
		if dev {
			socket = filepath.Join(root, "control.sock")
		}
	}
	return root, socket, nil
}

func serve(args []string) error {
	fs := flag.NewFlagSet("care-clinic-service", flag.ContinueOnError)
	dev := fs.Bool("dev", false, "run as the current user (state under ~/Library, socket only for this user)")
	root := fs.String("root", "", "state directory (default "+systemRoot+", or ~/Library/Application Support/"+devRootName+" with --dev)")
	socket := fs.String("socket", "", "control socket (default "+systemSocket+", or <root>/control.sock with --dev)")
	group := fs.String("group", "care-clinic", "group whose members may use the control socket (ignored with --dev)")
	mem := fs.Uint64("mem", 0, "VM memory in MiB (default engine default)")
	cpus := fs.Uint("cpus", 0, "VM CPUs (default engine default)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	rootDir, sock, err := paths(*dev, *root, *socket)
	if err != nil {
		return err
	}
	dirMode := os.FileMode(0o755)
	if *dev {
		*group = ""
		dirMode = 0o700
	} else if os.Geteuid() != 0 {
		return errors.New("must run as root (use --dev for local testing)")
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	for _, d := range []string{rootDir, filepath.Join(rootDir, "slots"), filepath.Join(rootDir, "logs")} {
		if err := os.MkdirAll(d, dirMode); err != nil {
			return err
		}
	}
	store := slots.New(filepath.Join(rootDir, "slots"), "")
	sup := supervisor.New(supervisor.Config{
		Slots: store,
		VM: vm.Config{
			DataDisk:   filepath.Join(rootDir, "data.img"),
			ConsoleLog: filepath.Join(rootDir, "logs", "console.log"),
			MemoryMiB:  *mem,
			CPUs:       *cpus,
		},
		Log: log,
	})

	ln, err := control.Listen(control.ListenConfig{Path: sock, Group: *group, Log: log})
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: control.NewServer(control.Config{
		Engine:     sup,
		Slots:      store,
		StagingDir: filepath.Join(rootDir, "staging"),
		Log:        log,
	}), ReadHeaderTimeout: 10 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	supDone := make(chan struct{})
	go func() { sup.Run(ctx); close(supDone) }()
	srvErr := make(chan error, 1)
	go func() { srvErr <- srv.Serve(ln) }()
	log.Info("care-clinic-service started", "root", rootDir, "socket", sock, "dev", *dev)

	if err := sup.Start(ctx); err != nil && !errors.Is(err, supervisor.ErrNoEngine) {
		log.Error("engine start failed", "err", err)
	}

	select {
	case <-ctx.Done():
		err = nil
	case err = <-srvErr:
		stop()
	}
	log.Info("shutting down")
	<-supDone // Run stops the VM gracefully before returning
	_ = srv.Close()
	log.Info("stopped")
	return err
}
