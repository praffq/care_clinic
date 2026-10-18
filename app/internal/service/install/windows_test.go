package install

import (
	"strings"
	"testing"
)

func TestWindowsScript(t *testing.T) {
	p := Plan{Binary: `C:\Program Files\CARE Clinic\care-clinic-service.exe`, User: "S-1-5-21-1004336348-1177238915-682003330-1001"}
	s, err := WindowsScript(p, `C:\Users\o'brien\AppData\Local\Temp\err.txt`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"New-LocalGroup -Name $g",
		"SecurityIdentifier('S-1-5-21-1004336348-1177238915-682003330-1001')",
		"Add-LocalGroupMember -Group $g -Member $sid",
		"Enable-WindowsOptionalFeature -Online -FeatureName 'HypervisorPlatform' -All -NoRestart",
		"$code = 3010",
		`& 'C:\Program Files\CARE Clinic\care-clinic-service.exe' @args`,
		"Invoke-Service service install --group $g",
		"Invoke-Service service start",
		`Set-Content -LiteralPath 'C:\Users\o''brien\AppData\Local\Temp\err.txt'`,
		"exit $code",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("script lacks %q:\n%s", want, s)
		}
	}
	if strings.Index(s, "service install") > strings.Index(s, "service start") {
		t.Error("starts before installing")
	}
}

func TestWindowsScriptRejects(t *testing.T) {
	good := Plan{Binary: `C:\x\care-clinic-service.exe`, User: "S-1-12-1-1-2-3-4"}
	if _, err := WindowsScript(good, "e"); err != nil {
		t.Fatalf("Azure AD SID: %v", err)
	}
	for _, p := range []Plan{
		{Binary: good.Binary, User: "nurse"},
		{Binary: good.Binary, User: "S-1-5-21-1'; rm"},
		{Binary: `relative\care-clinic-service.exe`, User: good.User},
		{Binary: `\\server\share\care-clinic-service.exe`, User: good.User},
		{Binary: `C:\x\care-clinic-service`, User: good.User},
	} {
		if _, err := WindowsScript(p, "e"); err == nil {
			t.Errorf("accepted %+v", p)
		}
	}
}
