package clinic

// Setup loads the stack's images into the engine and uploads its config.
// Start then migrates and brings it up. Without StackImages it reuses the
// images the engine's clinic already runs (see images).
func (e *Clinic) Setup() error {
	r, err := e.runtime()
	if err != nil {
		return err
	}
	if len(e.StackImages) > 0 {
		if err := r.Images(e.context(), e.StackImages); err != nil {
			return err
		}
	} else if _, err := e.images(r); err != nil {
		return err
	} else {
		e.logln("Using the CARE images already in the engine.")
	}
	if err := e.Configure(); err != nil {
		return err
	}
	e.logln("Setup done.")
	return nil
}
