package clinic

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/sys/autostart"
	"github.com/ohcnetwork/care_desktop/app/internal/vm"
)

func (e *Clinic) Project() string { return composeProject }

func (e *Clinic) Images() []string { return e.uninstallImages() }

func (e *Clinic) Purge() error {
	dockerAvailable, err := e.engineAvailable()
	if err != nil {
		return fmt.Errorf("could not check the CARE engine: %w", err)
	}
	if dockerAvailable {
		if _, err := e.inspectProject(); err != nil {
			return err
		}
	} else {
		e.logln("The CARE engine is absent; skipping Docker resource, image, and build-cache cleanup.")
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

	e.logln("Removing CARE's start-at-login entry if present...")
	if err := autostart.Remove(); err != nil {
		failed = append(failed, err)
	}

	if err := errors.Join(failed...); err != nil {
		return err
	}
	return e.removeInstallFiles(false)
}

// engineAvailable reports whether the CARE agent answers. A managed engine
// whose disk remains must answer: its retained volumes are never treated as
// gone just because the helper is stopped or damaged.
func (e *Clinic) engineAvailable() (bool, error) {
	absent, err := vm.ManagedEngineAbsent()
	if err != nil || absent {
		return false, err
	}
	_, err = e.Runner().AgentCaptureIn(30*time.Second, nil, "version", "--format", "{{.Server.Version}}")
	if err != nil && vm.Supported() {
		return false, fmt.Errorf("CARE engine files remain but its agent is not answering; recover the engine before removing retained clinic volumes: %w", err)
	}
	return agentStatus(err)
}

// agentStatus treats only a missing native agent socket as an absent engine;
// every other failure keeps cleanup fail-closed.
func agentStatus(err error) (bool, error) {
	if err == nil {
		return true, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return false, err
}
