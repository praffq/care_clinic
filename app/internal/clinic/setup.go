package clinic

import "github.com/ohcnetwork/care_desktop/app/internal/stack"

// Setup steps, in the order Setup and Start report them.
const (
	StepImages    = stack.StepImages
	StepConfigure = "configure"
	StepDatabase  = "database"
	StepStart     = "start"
	StepAdmin     = "admin"
	StepComputer  = "computer"
)

// Setup loads the stack's images into the engine and uploads its config.
// Start then migrates and brings it up. Without StackImages it reuses the
// images the engine's clinic already runs (see images).
func (e *Clinic) Setup() error {
	r, err := e.runtime()
	if err != nil {
		return err
	}
	r.Progress = e.Progress
	if len(e.StackImages) > 0 {
		e.Progress.Start(StepImages, "")
		if err := r.Images(e.context(), e.StackImages); err != nil {
			return err
		}
		e.Progress.Finish(StepImages, "")
	} else if _, err := e.images(r); err != nil {
		return err
	} else {
		e.logln("Using the CARE images already in the engine.")
		e.Progress.Skip(StepImages, "Already in the clinic engine")
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
