package main

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/sys/proc"
	"github.com/ohcnetwork/care_desktop/app/internal/vm"
)

// useEngine points every docker command this process starts at the CARE
// engine. On Windows the engine cannot see this computer's folders, so docker
// commands that mount them first bring the engine's copy up to date, and
// bring back whatever the containers wrote once they finish.
func useEngine() {
	vm.ApplyEnv()
	if runtime.GOOS != "windows" {
		return
	}
	proc.DockerHook = engineDockerHook(engineSync{
		sensitive: vm.MountSensitive,
		rewrite:   vm.RewriteDockerArgs,
		sync:      vm.Sync,
		timeout:   10 * time.Minute,
	})
}

type engineSync struct {
	sensitive func(args []string) bool
	rewrite   func(args []string) (out []string, paths []string)
	sync      func(ctx context.Context, paths []string) error
	timeout   time.Duration
}

// engineDockerHook fails a mount-sensitive docker command when either sync
// fails, so stale or missing data is never reported as success.
func engineDockerHook(e engineSync) func([]string) ([]string, func() error, error) {
	run := func(paths []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), e.timeout)
		defer cancel()
		return e.sync(ctx, paths)
	}
	return func(args []string) ([]string, func() error, error) {
		if !e.sensitive(args) {
			return args, func() error { return nil }, nil
		}
		out, paths := e.rewrite(args)
		if err := run(paths); err != nil {
			return nil, nil, fmt.Errorf("copy folders into the CARE engine: %w", err)
		}
		return out, func() error {
			if err := run(nil); err != nil {
				return fmt.Errorf("copy results back from the CARE engine: %w", err)
			}
			return nil
		}, nil
	}
}

// stopEngine runs at the end of an uninstall. With the images removed there is
// nothing left worth keeping in the engine, so its disk goes too.
func (a *App) stopEngine(removeImages bool) {
	if !vm.Supported() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := vm.Stop(ctx); err != nil {
		a.logln("Could not stop the CARE engine: " + err.Error())
		return
	}
	if !removeImages {
		return
	}
	if err := os.RemoveAll(vm.StateDir()); err != nil {
		a.logln("CARE was removed, but the CARE engine's files could not be deleted: " + err.Error())
		return
	}
	a.logln("The CARE engine and its disk were removed.")
}
