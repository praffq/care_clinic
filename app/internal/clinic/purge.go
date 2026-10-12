package clinic

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/ohcnetwork/care_desktop/app/internal/sys/autostart"
)

func (e *Clinic) Project() string { return composeProject }

func (e *Clinic) Images() []string { return e.uninstallImages() }

func (e *Clinic) Purge() error {
	dockerAvailable, err := e.dockerCLIAvailable()
	if err != nil {
		return fmt.Errorf("could not check Docker CLI: %w", err)
	}
	if dockerAvailable {
		if _, err := e.inspectProject(); err != nil {
			return err
		}
	} else {
		e.logln("Docker CLI is not installed; skipping Docker resource, image, and build-cache cleanup.")
	}
	if err := e.Backups().PreserveBackupCertificate(); err != nil {
		return err
	}
	rootPEM := ""

	if info, err := os.Stat(filepath.Join(e.InstallDir, "docker-compose.yml")); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("the installed compose file is not a regular file")
		}
		rootPEM = e.caddyRootPEM()
		if dockerAvailable {
			e.logln("Removing containers, network, and data volumes...")
			if err := e.dc("down", "-v", "--remove-orphans"); err != nil {
				e.logln("  (compose down reported an error - continuing cleanup)")
			}
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if dockerAvailable {
		if err := e.TeardownProject(); err != nil {
			return err
		}
	}

	var failed []error
	if dockerAvailable {
		e.logln("Removing Docker images...")
		if err := e.removeImages(); err != nil {
			failed = append(failed, err)
		}
	}
	for _, detail := range e.revertSystemChanges(rootPEM) {
		failed = append(failed, errors.New(detail))
	}

	if autostart.Enabled() {
		e.logln("Removing the start-at-login entry...")
		if err := autostart.Set(false); err != nil {
			failed = append(failed, err)
		}
	}

	if err := errors.Join(failed...); err != nil {
		return err
	}
	return e.removeInstallFiles(false)
}

func (e *Clinic) dockerCLIAvailable() (bool, error) {
	_, err := e.capture("docker", "--version")
	return dockerCLIStatus(err)
}

func dockerCLIStatus(err error) (bool, error) {
	if err == nil {
		return true, nil
	}
	if errors.Is(err, exec.ErrNotFound) {
		return false, nil
	}
	return false, err
}
