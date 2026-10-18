package clinic

import (
	"runtime"
	"strings"

	"github.com/ohcnetwork/care_desktop/app/internal/sys/elevate"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/hosts"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/trust"
)

// setUpThisComputer reports whether this computer can open the clinic.
func (e *Clinic) setUpThisComputer() bool {
	e.localSetupOffered = true
	host := e.host()

	hostsStep, needHosts := hosts.Step(e.Log, host)
	cert, cleanup, needTrust := trust.Step(e.Log, host, e.caddyRootPEM())
	defer cleanup()
	var planned []elevate.Step
	if needHosts {
		planned = append(planned, hostsStep)
	}
	if needTrust {
		planned = append(planned, cert.Step)
	}
	var err error
	if len(planned) > 0 {
		e.logln("Setting up this computer to open https://" + host + "/...")
		title, message := confirmPrompt(runtime.GOOS, host, planned)
		if e.Confirm == nil || !e.Confirm(title, message) {
			e.logln("Skipped - other devices can still use the clinic, but this computer's own " +
				"browser may not open https://" + host + "/. Starting CARE again will offer this once more.")
			return false
		}
		var steps []elevate.Step
		if needHosts {
			steps = append(steps, hostsStep)
		}
		userTrustFirst := trustWithoutAdminFirst(runtime.GOOS, needHosts)
		if needTrust && (!userTrustFirst || !cert.TryWithoutAdmin()) {
			steps = append(steps, cert.Step)
		}
		err = elevate.Steps(steps)
		if needTrust && !userTrustFirst && !trust.HostTrusts(host) {
			e.logln("The system certificate store didn't take CARE's certificate; trying this user's keychain instead...")
			cert.TryWithoutAdmin()
		}
	}
	hostsReady, trustReady := hosts.HasEntry(host), trust.HostTrusts(host)
	e.logln(localSetupResult(host, hostsReady, trustReady, err))
	return err == nil && hostsReady && trustReady
}

func trustWithoutAdminFirst(goos string, elevatingAnyway bool) bool {
	return goos != "darwin" || !elevatingAnyway
}

func localSetupResult(host string, hostsReady, trustReady bool, err error) string {
	var incomplete []string
	if !hostsReady {
		incomplete = append(incomplete, "the local hosts entry")
	}
	if !trustReady {
		incomplete = append(incomplete, "certificate trust")
	}
	if len(incomplete) == 0 {
		return "This computer can now open https://" + host + "/."
	}
	detail := ""
	if err != nil {
		detail = " (" + err.Error() + ")"
	}
	return "Could not confirm " + strings.Join(incomplete, " and ") + detail +
		". Other devices are unaffected; try starting CARE again to retry local setup, or ask your administrator for help."
}

func confirmPrompt(goos, host string, steps []elevate.Step) (title, message string) {
	return "Set up " + host + " on this computer?",
		"This lets you open CARE securely on this computer.\n\n" + approvalNote(goos, len(steps))
}

func approvalNote(goos string, changes int) string {
	switch goos {
	case "darwin":
		if changes > 1 {
			return "Enter your computer's administrator password and approve the security prompt when macOS asks."
		}
		return "Use your computer's password or Touch ID when macOS asks."
	case "windows":
		return "Approve the Windows permission prompt to continue."
	}
	return "Enter your computer's administrator password when asked."
}
