package main

import (
	"github.com/ohcnetwork/care_desktop/app/internal/clinic"
)

// RunUninstall removes CARE from the engine and undoes this computer's
// hosts entry, certificate trust and home access. With removeData the clinic
// database and files go too. The service and engine stay installed.
func (a *App) RunUninstall(removeData bool, adminPassword string) (err error) {
	defer a.logError(&err)
	return a.run(func() error {
		if err := a.requireAdmin(adminPassword); err != nil {
			return err
		}
		if err := a.beginRemoval(); err != nil {
			return err
		}
		if err := a.engine().Uninstall(clinic.UninstallOptions{
			RemoveData:       removeData,
			RemoveInstallDir: true,
		}); err != nil {
			return err
		}
		if err := a.resetConfigAfterUninstall(); err != nil {
			return err
		}
		a.logln("Uninstall complete.")
		a.emit("uninstalled", true)
		return nil
	}, false, "uninstall")
}
