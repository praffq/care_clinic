package clinic

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/ohcnetwork/care_desktop/app/internal/stack"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/mdns"
)

func (e *Clinic) host() string {
	return mdns.Label(e.mdnsName()) + ".local"
}

func (e *Clinic) Host() string { return e.host() }

func (e *Clinic) context() context.Context {
	if e.ctx != nil {
		return e.ctx
	}
	return context.Background()
}

func (e *Clinic) runtime() (*stack.Runtime, error) {
	if e.Stack == nil {
		return nil, errors.New("the CARE Clinic service is not configured")
	}
	return e.Stack, nil
}

// Configure renders the install kit for this clinic's name and uploads it
// to the engine. It takes effect on the next start.
func (e *Clinic) Configure() error {
	r, err := e.runtime()
	if err != nil {
		return err
	}
	return r.Configure(e.context(), os.DirFS(e.InstallDir), e.host(), nil, e.StackImages)
}

func (e *Clinic) Stop() error {
	r, err := e.runtime()
	if err != nil {
		return err
	}
	return r.Stop(e.context())
}

func (e *Clinic) Restart() error {
	e.logln("Restarting CARE...")
	if err := e.Stop(); err != nil {
		return err
	}
	return e.Start()
}

// Status lists "service state" lines, one per service.
func (e *Clinic) Status() (string, error) {
	r, err := e.runtime()
	if err != nil {
		return "", err
	}
	st, err := r.Status(e.context())
	if err != nil {
		return "", err
	}
	lines := make([]string, 0, len(st.Services))
	for _, s := range st.Services {
		lines = append(lines, fmt.Sprintf("%s %s", s.Name, s.State))
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n"), nil
}

func (e *Clinic) migrate() error {
	r, err := e.runtime()
	if err != nil {
		return err
	}
	if err := r.Migrate(e.context()); err != nil {
		return fmt.Errorf("%w; resolve the migration error and start CARE again", err)
	}
	return nil
}

func (e *Clinic) createAdmin() error {
	if e.AdminPassword == "" {
		return nil
	}
	r, err := e.runtime()
	if err != nil {
		return err
	}
	switch err := r.CreateSuperuser(e.context(), "admin", e.AdminPassword); {
	case err == nil:
		e.logln("Created admin login (username: admin).")
	case errors.Is(err, stack.ErrUserExists):
	default:
		return fmt.Errorf("couldn't create the admin login: %w", err)
	}
	return nil
}
