package clinic

import (
	"os"

	"github.com/ohcnetwork/care_desktop/app/internal/backup"
	"github.com/ohcnetwork/care_desktop/app/internal/plugins"
)

func (e *Clinic) Setup() error {
	if err := backup.CheckLocation(e.backupDir(), e.InstallDir); err != nil {
		return err
	}
	if err := plugins.New(e.InstallDir).InitializeDefaults(); err != nil {
		return err
	}
	if err := e.Backups().InstallCertificate([]byte(e.BackupCertificate)); err != nil {
		return err
	}
	if err := os.MkdirAll(e.backupDir(), 0o755); err != nil {
		return err
	}
	e.logln("Backups will go to: " + e.backupDir())
	if err := e.Backups().EnsureKeysDir(); err != nil {
		return err
	}
	r, err := e.runtime()
	if err != nil {
		return err
	}
	if err := r.Images(e.context(), e.StackImages); err != nil {
		return err
	}
	if err := e.Configure(); err != nil {
		return err
	}
	e.logln("Setup done.")
	return nil
}
