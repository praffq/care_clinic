package install

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/ohcnetwork/care_desktop/app/internal/sys/elevate"
)

// Windows service names (see app/cmd/care-clinic-service/servicecmd_windows.go).
const (
	ServiceName = "CareClinic"
	// WindowsRootName is the state root under %ProgramData%.
	WindowsRootName = "CARE Clinic"
	// RestartExitCode is what WindowsScript exits with when Windows must
	// restart (the DISM convention, ERROR_SUCCESS_REBOOT_REQUIRED).
	RestartExitCode = 3010
)

var sidRE = regexp.MustCompile(`^S-1-[0-9]+(-[0-9]+)+$`)

func (p Plan) validateWindows() error {
	if !sidRE.MatchString(p.User) {
		return fmt.Errorf("can't add %q to the %s group: not a Windows SID", p.User, Group)
	}
	if !windowsAbs(p.Binary) || !strings.EqualFold(p.Binary[len(p.Binary)-4:], ".exe") {
		return fmt.Errorf("service binary must be an absolute path to an .exe: %q", p.Binary)
	}
	return nil
}

// windowsAbs accepts a drive-letter path (C:\...); UNC and relative paths
// are refused.
func windowsAbs(p string) bool {
	return len(p) > 7 && (p[0]|0x20 >= 'a' && p[0]|0x20 <= 'z') && p[1] == ':' && p[2] == '\\'
}

// WindowsScript is the PowerShell script, run elevated, that installs the
// service from p.Binary. It is idempotent:
//
//  1. creates the care-clinic local group and adds the user (by SID);
//  2. turns on the Windows Hypervisor Platform when it is off;
//  3. runs `<binary> service install` (stops a running service, copies the
//     binary and qemu\ to %ProgramData%\CARE Clinic\bin, registers the
//     service and its firewall rules) and `service start`, so a restart
//     picks up the group change.
//
// It exits with RestartExitCode when Windows must restart for the
// hypervisor, 1 with the error written to errFile on failure, else 0.
func WindowsScript(p Plan, errFile string) (string, error) {
	if err := p.validateWindows(); err != nil {
		return "", err
	}
	q := elevate.PSQuote
	lines := []string{
		"$ErrorActionPreference = 'Stop'",
		"$code = 0",
		// Windows PowerShell turns a native command's stderr into errors,
		// which 'Stop' would throw on: collect it and check the exit code.
		"function Invoke-Service { $ErrorActionPreference = 'Continue'; $out = & " + q(p.Binary) + " @args 2>&1 | Out-String; if ($LASTEXITCODE -ne 0) { throw ('care-clinic-service ' + ($args -join ' ') + ' failed: ' + $out) } }",
		"try {",
		"  $g = " + q(Group),
		"  if (-not (Get-LocalGroup -Name $g -ErrorAction SilentlyContinue)) { New-LocalGroup -Name $g -Description 'CARE Clinic service users' | Out-Null }",
		"  $sid = New-Object System.Security.Principal.SecurityIdentifier(" + q(p.User) + ")",
		"  try { Add-LocalGroupMember -Group $g -Member $sid } catch [Microsoft.PowerShell.Commands.MemberExistsException] { }",
		"  $f = Get-WindowsOptionalFeature -Online -FeatureName 'HypervisorPlatform'",
		"  if ($f.State -eq 'EnablePending') { $code = " + fmt.Sprint(RestartExitCode) + " }",
		"  elseif ($f.State -ne 'Enabled') {",
		"    $r = Enable-WindowsOptionalFeature -Online -FeatureName 'HypervisorPlatform' -All -NoRestart",
		"    if ($r.RestartNeeded) { $code = " + fmt.Sprint(RestartExitCode) + " }",
		"  }",
		"  Invoke-Service service install --group $g --log-user " + q(p.User),
		"  Invoke-Service service start",
		"} catch {",
		"  ($_ | Out-String) | Set-Content -LiteralPath " + q(errFile) + " -Encoding UTF8",
		"  exit 1",
		"}",
		"exit $code",
	}
	return strings.Join(lines, "\n") + "\n", nil
}
