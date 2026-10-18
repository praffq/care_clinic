package clinic

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ohcnetwork/care_desktop/app/internal/sys/elevate"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/hosts"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/netfix"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/tailscale"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/trust"
)

type UninstallOptions struct {
	// RemoveData also deletes the clinic database and files in the engine.
	RemoveData bool
	// RemoveInstallDir deletes this computer's copy of the deployment kit.
	RemoveInstallDir bool
}

// Uninstall removes the CARE stack from the engine and undoes this
// computer's hosts entry, certificate trust and home access. The service and
// engine stay installed.
func (e *Clinic) Uninstall(opts UninstallOptions) error {
	r, err := e.runtime()
	if err != nil {
		return err
	}
	rootPEM := e.caddyRootPEM()
	if err := r.Down(e.context(), opts.RemoveData); err != nil {
		return fmt.Errorf("couldn't remove CARE from the engine: %w", err)
	}
	var failed []error
	for _, detail := range e.revertSystemChanges(rootPEM) {
		failed = append(failed, errors.New(detail))
	}
	if err := tailscale.Disable(e.Runner(), tailscale.Target(HomePort)); err != nil {
		e.logln("Couldn't turn off home access in Tailscale (" + err.Error() + "). Turn it off in Tailscale if it is still shared.")
	}
	if err := errors.Join(failed...); err != nil {
		return err
	}
	if opts.RemoveInstallDir {
		return e.RemoveInstallFiles()
	}
	return nil
}

// RemoveInstallFiles deletes this computer's copy of the deployment kit.
func (e *Clinic) RemoveInstallFiles() error {
	dir := filepath.Clean(e.InstallDir)
	if !filepath.IsAbs(dir) || !strings.EqualFold(filepath.Base(dir), "install") ||
		!strings.EqualFold(filepath.Base(filepath.Dir(dir)), "care-clinic") {
		return fmt.Errorf("refusing to delete an unrecognized installation directory: %s", dir)
	}
	e.logln("Removing installed files " + dir)
	return os.RemoveAll(dir)
}

func (e *Clinic) revertSystemChanges(rootPEM string) []string {
	if runtime.GOOS == "windows" {
		return e.revertSystemChangesWindows(rootPEM)
	}
	var failed []string
	if s := trust.Untrust(e.Log, e.Confirm, rootPEM); s != "" {
		failed = append(failed, s)
	}
	if s := hosts.Remove(e.Log, e.Confirm, e.host()); s != "" {
		failed = append(failed, s)
	}
	present, err := netfix.InspectRules(e.Runner())
	if err != nil {
		failed = append(failed, err.Error())
	} else if present {
		if err := netfix.Undo(e.Log); err != nil {
			failed = append(failed, err.Error())
		}
	}
	return failed
}

func (e *Clinic) revertSystemChangesWindows(rootPEM string) []string {
	var failed []string
	var steps []elevate.Step

	certStep, certNeed, err := trust.RemoveStepWindows(rootPEM)
	if err != nil {
		failed = append(failed, err.Error())
	} else if certNeed {
		steps = append(steps, certStep)
	}

	host := e.host()
	hostsStep, hostsCleanup, hostsNeed := hosts.RemoveStepWindows(host)
	defer hostsCleanup()
	if hostsNeed {
		steps = append(steps, hostsStep)
	}

	fwStep, fwNeed, err := netfix.UndoStepWindows(e.Runner())
	if err != nil {
		failed = append(failed, err.Error())
	} else if fwNeed {
		steps = append(steps, fwStep)
	}

	if len(steps) == 0 {
		return failed
	}

	e.logln("Removing this computer's certificate trust, hosts entry, and firewall rules (approve the prompt)...")
	elevateErr := elevate.Teardown(steps)
	detail := ""
	if elevateErr != nil {
		detail = " (" + elevateErr.Error() + ")"
	}

	if certNeed {
		if present, err := trust.Inspect(); err != nil {
			failed = append(failed, err.Error())
		} else if present {
			failed = append(failed, "The certificate \""+trust.CommonName+"\" is still trusted by this computer"+detail+
				". Remove it in certmgr.msc, or run as administrator: certutil -delstore Root \""+trust.CommonName+"\"")
		} else {
			e.logln("Removed CARE's certificate from this machine's trust store.")
		}
	}
	if hostsNeed {
		if s := hosts.Leftover(host); s != "" {
			failed = append(failed, s+detail)
		} else {
			e.logln("Removed the " + host + " hosts entry.")
		}
	}
	if fwNeed {
		if present, err := netfix.InspectRules(e.Runner()); err != nil {
			failed = append(failed, err.Error())
		} else if present {
			failed = append(failed, "CARE's firewall rules are still present"+detail)
		} else {
			e.logln("Removed CARE's firewall rules.")
		}
	}
	return failed
}
