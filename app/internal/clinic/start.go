package clinic

// Start uploads the config (unless Setup just did), migrates, brings the
// stack up, creates the admin login and offers to set up this computer.
func (e *Clinic) Start() error {
	if !e.configured {
		if err := e.configure(); err != nil {
			return err
		}
	}
	e.Progress.Start(StepDatabase, "")
	if err := e.migrate(); err != nil {
		return err
	}
	e.Progress.Finish(StepDatabase, "")
	r, err := e.runtime()
	if err != nil {
		return err
	}
	e.Progress.Start(StepStart, "")
	if err := r.Up(e.context()); err != nil {
		return err
	}
	if err := r.RegisterFrontendPlugins(e.context(), e.host()); err != nil {
		return err
	}
	e.Progress.Finish(StepStart, "")
	if err := e.createAdmin(); err != nil {
		return err
	}
	e.logln("")
	e.logln("CARE is up -> https://" + e.host() + "/   (login: admin)")
	if !e.localSetupOffered {
		e.Progress.Start(StepComputer, "")
		if e.setUpThisComputer() {
			e.Progress.Finish(StepComputer, "")
		} else {
			e.Progress.Skip(StepComputer, "Skipped")
		}
	} else {
		e.Progress.Skip(StepComputer, "Already offered")
	}
	return nil
}

// Up brings an already configured stack up without re-uploading config.
func (e *Clinic) Up() error {
	r, err := e.runtime()
	if err != nil {
		return err
	}
	return r.Up(e.context())
}
