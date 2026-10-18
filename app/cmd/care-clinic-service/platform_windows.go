package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"path/filepath"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
)

// systemRoot is %ProgramData%\CARE Clinic; `service install` restricts it
// to SYSTEM and Administrators.
var systemRoot = filepath.Join(programData(), "CARE Clinic")

// devSocket is a per-user pipe name: pipes live in their own namespace.
func devSocket(string) string { return `\\.\pipe\care-clinic-dev-` + os.Getenv("USERNAME") }

func programData() string {
	if d := os.Getenv("ProgramData"); filepath.IsAbs(d) {
		return d
	}
	return `C:\ProgramData`
}

func requireAdmin() error {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return errors.New("must run as the CareClinic service or an elevated administrator (use --dev for local testing)")
	}
	return nil
}

// platformMain handles `service <command>`.
func platformMain(args []string) (int, bool) {
	if len(args) > 0 && args[0] == "service" {
		return serviceCmd(args[1:]), true
	}
	return 0, false
}

// run serves under the service control manager when started by it, and
// until Ctrl+C otherwise.
func run(args []string) error {
	isService, err := svc.IsWindowsService()
	if err != nil {
		return err
	}
	if isService {
		h := &handler{serve: func(ctx context.Context) error { return serve(ctx, args) }}
		if err := svc.Run(serviceName, h); err != nil {
			return err
		}
		return h.err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return serve(ctx, args)
}
