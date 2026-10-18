package clinic

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/stack"
)

// ErrCareNotRunning means the CARE backend isn't up to take a password change.
var ErrCareNotRunning = errors.New("CARE isn't running; start CARE from Overview, then try again")

const webPasswordTimeout = 2 * time.Minute

func (e *Clinic) backendRunning() (bool, error) {
	out, err := e.Status()
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "backend running" {
			return true, nil
		}
	}
	return false, nil
}

// ResetWebPassword sets the password of a CARE web user. CARE's own password
// validators decide whether the password is acceptable.
func (e *Clinic) ResetWebPassword(username, password string) error {
	username = strings.TrimSpace(username)
	if username == "" {
		return errors.New("enter the CARE username whose password should change")
	}
	if password == "" {
		return errors.New("enter a new CARE web password")
	}
	running, err := e.backendRunning()
	if err != nil {
		e.logln("Couldn't check whether CARE is running: " + err.Error())
		return ErrCareNotRunning
	}
	if !running {
		return ErrCareNotRunning
	}
	r, err := e.runtime()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(e.context(), webPasswordTimeout)
	defer cancel()
	var rejected *stack.PasswordRejectedError
	switch err := r.ChangePassword(ctx, username, password); {
	case err == nil:
		e.logln("Changed the CARE web password for " + username + ".")
		return nil
	case errors.Is(err, stack.ErrNoUser):
		return fmt.Errorf("no CARE user named %q; usernames are case-sensitive", username)
	case errors.As(err, &rejected):
		return err
	default:
		detail := strings.ReplaceAll(err.Error(), password, "[new password]")
		if ctx.Err() != nil {
			detail = "timed out after " + webPasswordTimeout.String() + "; " + detail
		}
		e.logln("CARE web password change for " + username + " failed: " + detail)
		return errors.New("CARE couldn't change that web password; nothing was confirmed. Open the log file for details")
	}
}
