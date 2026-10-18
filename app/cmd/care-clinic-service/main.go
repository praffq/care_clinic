// Command care-clinic-service is the root host service that owns the CARE
// engine VM: it keeps the active engine slot running (restarting it after
// crashes) and serves the control API the app uses, on a Unix socket
// (macOS, a LaunchDaemon) or a named pipe (Windows, the CareClinic service).
//
//	care-clinic-service [flags]                 run the service
//	care-clinic-service ctl [flags] <command>   talk to a running service
//	care-clinic-service service <command>       (Windows) install, start, stop or remove the service
//
// --dev runs as the current user with state under the user's config
// directory and a socket only that user may open, for local testing.
//
// Besides the VM the service owns the host side of the network: it forwards
// the host's ports 80/443 (and 127.0.0.1:8480) into the guest and advertises
// <name>.local over mDNS, so CARE is reachable after a reboot without anyone
// opening the app. It also copies finished backup sets out of the engine
// into backup_dir (config.json) every few minutes. Only one process per host may advertise the name; run
// with --mdns=false while a desktop app build that still advertises in-process
// is in use.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/client"
	"github.com/ohcnetwork/care_desktop/app/internal/engine/vm"
	"github.com/ohcnetwork/care_desktop/app/internal/service/advertise"
	"github.com/ohcnetwork/care_desktop/app/internal/service/api"
	"github.com/ohcnetwork/care_desktop/app/internal/service/backups"
	"github.com/ohcnetwork/care_desktop/app/internal/service/control"
	"github.com/ohcnetwork/care_desktop/app/internal/service/hostfwd"
	"github.com/ohcnetwork/care_desktop/app/internal/service/hostlogs"
	"github.com/ohcnetwork/care_desktop/app/internal/service/logfile"
	"github.com/ohcnetwork/care_desktop/app/internal/service/settings"
	"github.com/ohcnetwork/care_desktop/app/internal/service/slots"
	"github.com/ohcnetwork/care_desktop/app/internal/service/supervisor"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/logdir"
)

// version is set at build time with -ldflags "-X main.version=<version>".
var version = "dev"

const (
	systemSocket = control.SystemSocket
	devRootName  = "care-clinic-dev"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "ctl" {
		os.Exit(ctl(os.Args[2:]))
	}
	if code, ok := platformMain(os.Args[1:]); ok {
		os.Exit(code)
	}
	if err := run(os.Args[1:]); err != nil {
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
			socket = devSocket(root)
		}
	}
	return root, socket, nil
}

// serve runs the service until ctx is cancelled or the control server fails.
func serve(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("care-clinic-service", flag.ContinueOnError)
	dev := fs.Bool("dev", false, "run as the current user (state in the user's config directory, socket only for this user)")
	root := fs.String("root", "", "state directory (default "+systemRoot+", or <user config dir>/"+devRootName+" with --dev)")
	socket := fs.String("socket", "", "control socket (default "+systemSocket+", or "+devSocket("<root>")+" with --dev)")
	group := fs.String("group", "care-clinic", "group whose members may use the control socket (ignored with --dev)")
	mem := fs.Uint64("mem", 0, "VM memory in MiB (default engine default)")
	cpus := fs.Uint("cpus", 0, "VM CPUs (default engine default)")
	logFile := fs.String("log-file", "", "write the service log to this size-rotated file, which also becomes stderr (default: service.log in the log folder, or stderr with --dev)")
	logDir := fs.String("log-dir", "", "log folder used when the installed user's log folder can't be used (default <root>/logs)")
	advertiseMDNS := fs.Bool("mdns", true, "advertise <mdns_name>.local on the LAN")
	fwd := map[string]*string{}
	for _, name := range []string{"http", "https", "home"} {
		fwd[name] = fs.String("forward-"+name, "", "with --dev: listen address for the "+name+" forward, or \"off\"")
	}
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
	} else if err := requireAdmin(); err != nil {
		return err
	}
	overrides := map[string]string{}
	for name, v := range fwd {
		if *v != "" {
			if !*dev {
				return fmt.Errorf("--forward-%s needs --dev", name)
			}
			overrides[name] = *v
		}
	}
	rules, err := hostfwd.StandardRules(overrides)
	if err != nil {
		return err
	}

	for _, d := range []string{rootDir, filepath.Join(rootDir, "slots")} {
		if err := os.MkdirAll(d, dirMode); err != nil {
			return err
		}
	}
	fallbackLogs := *logDir
	if fallbackLogs == "" {
		fallbackLogs = filepath.Join(rootDir, "logs")
	}
	logs, logNotes, err := hostlogs.Resolve(rootDir, fallbackLogs)
	if err != nil {
		return fmt.Errorf("log folder: %w", err)
	}
	var logOut io.Writer = os.Stderr
	switch {
	case *logFile != "":
		if err := os.MkdirAll(filepath.Dir(*logFile), 0o755); err != nil {
			return err
		}
		f, err := logfile.Open(*logFile, logdir.MaxFileBytes, logdir.KeepRotated)
		if err != nil {
			return err
		}
		logOut = f
	case !*dev:
		f, err := logfile.OpenIn(logs, hostlogs.ServiceLog, logdir.MaxFileBytes, logdir.KeepRotated)
		if err != nil {
			return err
		}
		logOut = f
	}
	log := slog.New(slog.NewTextHandler(logOut, nil))
	var sup *supervisor.Supervisor
	collector := hostlogs.New(hostlogs.Config{
		Dir:           logs,
		Agent:         client.New(func(ctx context.Context) (net.Conn, error) { return sup.DialAgent(ctx) }),
		Ready:         func() bool { return sup.Status().AgentReachable() },
		EngineVersion: func() string { return sup.Status().EngineVersion },
		StatePath:     filepath.Join(rootDir, hostlogs.StateFile),
		Notes:         logNotes,
		Log:           log,
	})
	console := collector.ConsoleWriter()
	defer func() { _ = console.Close() }()
	store := slots.New(filepath.Join(rootDir, "slots"), "")
	sup = supervisor.New(supervisor.Config{
		Slots: store,
		VM: vm.Config{
			DataDisk:   filepath.Join(rootDir, "data.img"),
			ConsoleLog: filepath.Join(logs.Path(), hostlogs.ConsoleLog),
			Console:    console,
			MemoryMiB:  *mem,
			CPUs:       *cpus,
		},
		Log: log,
	})
	var (
		fw  *hostfwd.Forwarder
		adv *advertise.Service
		exp *backups.Exporter
	)
	defaults := api.ServiceConfig{BackupDir: filepath.Join(rootDir, "backups")}
	cfg, err := settings.Open(filepath.Join(rootDir, "config.json"), defaults, func(c api.ServiceConfig) {
		fw.SetProxyProtocol(c.ProxyProtocol)
		adv.SetName(c.MDNSName)
		// A new backup or copy folder is tried now, not at the next pass.
		exp.Kick()
	})
	if err != nil {
		// A broken config must not keep CARE down after a reboot; Open
		// returned the defaults, and saving a new config replaces the file.
		log.Error("config.json unreadable, using defaults", "err", err)
	}
	fw = hostfwd.New(hostfwd.Config{Rules: rules, Dial: sup.DialGuest, ProxyProtocol: cfg.Get().ProxyProtocol, Log: log})
	adv = advertise.New(advertise.Config{Name: cfg.Get().MDNSName, Disabled: !*advertiseMDNS, Log: log})

	exp = backups.New(backups.Config{
		Agent:   client.New(sup.DialAgent),
		Ready:   func() bool { return sup.Status().AgentReachable() },
		Dir:     func() string { return cfg.Get().BackupDir },
		CopyDir: func() string { return cfg.Get().BackupCopyDir },
		Log:     log,
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
		Forward:    fw.Status,
		MDNS:       adv.Status,
		Settings:   cfg,
		Backups:    exp,
		Version:    version,
	}), ReadHeaderTimeout: 10 * time.Second}

	ctx, stop := context.WithCancel(ctx)
	defer stop()
	supDone := make(chan struct{})
	go func() { sup.Run(ctx); close(supDone) }()
	netDone := make(chan struct{}, 2)
	go func() { fw.Run(ctx); netDone <- struct{}{} }()
	go func() { adv.Run(ctx); netDone <- struct{}{} }()
	expDone := make(chan struct{})
	go func() { exp.Run(ctx); close(expDone) }()
	logsDone := make(chan struct{})
	go func() { collector.Run(ctx); close(logsDone) }()
	srvErr := make(chan error, 1)
	go func() { srvErr <- srv.Serve(ln) }()
	log.Info("care-clinic-service started", "root", rootDir, "socket", sock, "dev", *dev, "logs", logs.Path())
	for _, n := range logNotes {
		log.Warn(n)
	}

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
	<-netDone
	<-netDone // the advertiser says goodbye so clients drop the name
	<-expDone
	<-logsDone
	<-supDone // Run stops the VM gracefully before returning
	_ = srv.Close()
	log.Info("stopped")
	return err
}
