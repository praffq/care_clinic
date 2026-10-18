package clinic

import (
	"context"
	"errors"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/client"
	"github.com/ohcnetwork/care_desktop/app/internal/stack"
)

// Setup steps, in the order Setup and Start report them.
const (
	StepImages    = stack.StepImages
	StepConfigure = "configure"
	StepDatabase  = "database"
	StepStart     = "start"
	StepAdmin     = "admin"
	StepComputer  = "computer"
)

type Release struct {
	Version string
	Images  []stack.Image
	Pull    []stack.Image
}

type ReleaseFunc func(ctx context.Context) (*Release, error)

// Setup loads the stack's images into the engine and uploads its config.
// Start then migrates and brings it up. Without StackImages it reuses the
// images the engine's clinic already runs (see images), or else installs
// the release FromFeed returns.
func (e *Clinic) Setup() error {
	r, err := e.runtime()
	if err != nil {
		return err
	}
	r.Progress = e.Progress
	pull := e.StackImages
	if len(pull) == 0 {
		_, err := r.InstalledImages(e.context())
		switch {
		case err == nil:
			e.logln("Using the CARE images already in the engine.")
			e.Progress.Skip(StepImages, "Already in the clinic engine")
		case !errors.Is(err, client.ErrNotFound):
			return err
		case e.FromFeed == nil:
			return ErrNoImages
		default:
			e.Progress.Start(StepImages, "Finding the newest CARE release")
			rel, err := e.FromFeed(e.context())
			if err != nil {
				return err
			}
			e.logln("Installing CARE " + rel.Version + " from the update feed.")
			e.StackImages, pull = rel.Images, rel.Pull
		}
	}
	if len(pull) > 0 {
		e.Progress.Start(StepImages, "")
		if err := r.Images(e.context(), pull); err != nil {
			return err
		}
		e.Progress.Finish(StepImages, "")
	}
	if err := e.configure(); err != nil {
		return err
	}
	e.logln("Setup done.")
	return nil
}

func (e *Clinic) configure() error {
	e.Progress.Start(StepConfigure, "")
	if err := e.Configure(); err != nil {
		return err
	}
	e.configured = true
	e.Progress.Finish(StepConfigure, "")
	return nil
}
