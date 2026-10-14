package main

import (
	"context"
	"os"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/vm"
)

func useEngine() { vm.ConfigureAgent() }

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
