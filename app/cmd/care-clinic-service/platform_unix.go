//go:build unix

package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
)

const systemRoot = "/Library/Application Support/care-clinic"

func devSocket(root string) string { return filepath.Join(root, "control.sock") }

func requireAdmin() error {
	if os.Geteuid() != 0 {
		return errors.New("must run as root (use --dev for local testing)")
	}
	return nil
}

// platformMain handles OS-specific subcommands; Unix has none.
func platformMain([]string) (int, bool) { return 0, false }

// run serves until SIGINT or SIGTERM (launchd stops the daemon with SIGTERM).
func run(args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serve(ctx, args)
}
