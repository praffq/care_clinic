package clinic

func (e *Clinic) Status() (string, error) {
	return e.capture("compose", "ps", "--format", "{{.Service}} {{.State}}")
}
