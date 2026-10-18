package main

// ResetWebPassword sets a CARE web user's password, for staff who are locked
// out of CARE itself. The CARE Clinic admin password authorises it.
func (a *App) ResetWebPassword(username, newPassword, adminPassword string) error {
	return a.withServerJob(func() error {
		if err := a.requireAdmin(adminPassword); err != nil {
			return err
		}
		if err := a.requireSetup(); err != nil {
			return err
		}
		return a.engine().ResetWebPassword(username, newPassword)
	})
}
