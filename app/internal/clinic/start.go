package clinic

// Start uploads the current config, migrates and brings the stack up. The
// service also brings an installed stack up after every engine boot, so
// this is only needed after setup or a config change.
func (e *Clinic) Start() error {
	if recovered, err := e.RecoverPlugins(); err != nil || recovered {
		return err
	}
	if err := e.Backups().RecoverRestore(); err != nil {
		return err
	}
	if err := e.Backups().EnsureKeysDir(); err != nil {
		return err
	}
	if err := e.Configure(); err != nil {
		return err
	}
	if err := e.migrate(); err != nil {
		return err
	}
	r, err := e.runtime()
	if err != nil {
		return err
	}
	if err := r.Up(e.context()); err != nil {
		return err
	}
	if err := e.createAdmin(); err != nil {
		return err
	}
	if err := e.Backups().FinishRestore(); err != nil {
		return err
	}
	e.logln("")
	e.logln("CARE is up -> https://" + e.host() + "/   (login: admin)")
	if !e.localSetupOffered {
		e.setUpThisComputer()
	}
	return nil
}
