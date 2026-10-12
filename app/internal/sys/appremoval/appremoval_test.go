package appremoval

import "testing"

func TestBundleOfAcceptsInstalledBundleOnly(t *testing.T) {
	cases := []struct {
		exe  string
		want string
	}{
		{"/Applications/CARE Clinic.app/Contents/MacOS/care-clinic", "/Applications/CARE Clinic.app"},
		{"/Users/a/Applications/CARE Clinic.app/Contents/MacOS/care-clinic", "/Users/a/Applications/CARE Clinic.app"},
		{"/Volumes/CARE Clinic/CARE Clinic.app/Contents/MacOS/care-clinic", ""},
		{"/private/var/folders/x/AppTranslocation/1/d/CARE Clinic.app/Contents/MacOS/care-clinic", ""},
		{"/Users/a/go/bin/care-clinic", ""},
	}
	for _, c := range cases {
		got, err := bundleOf(c.exe)
		if c.want == "" {
			if err == nil {
				t.Errorf("bundleOf(%q) = %q, want an error", c.exe, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("bundleOf(%q) = %q, %v; want %q", c.exe, got, err, c.want)
		}
	}
}

func TestOtherPIDsIgnoresOwnProcessAndVMHost(t *testing.T) {
	own := `[{"pid":4242,"commandLine":"CARE Clinic.exe"}]`
	helper := own[:len(own)-1] + `,{"pid":77,"commandLine":"CARE Clinic.exe --vm-host"}]`
	gui := own[:len(own)-1] + `,{"pid":88,"commandLine":"CARE Clinic.exe"}]`
	if otherPIDs(own, 4242) {
		t.Error("the uninstall process itself was treated as another instance")
	}
	if otherPIDs(helper, 4242) {
		t.Error("the detached engine helper was treated as another app window")
	}
	if !otherPIDs(gui, 4242) {
		t.Error("a second CARE Clinic process was not detected")
	}
	if !otherPIDs(`[{"pid":99}]`, 4242) {
		t.Error("a matching process with an unreadable command line must be treated conservatively")
	}
	if otherPIDs(`[]`, 4242) {
		t.Error("an empty process list was treated as another instance")
	}
}

func TestDifferentUserComparesAccountsCaseInsensitively(t *testing.T) {
	if differentUser(`CLINIC-PC\Nurse`, `clinic-pc\nurse`) {
		t.Error("the same account in different case was treated as different")
	}
	if !differentUser(`CLINIC-PC\Nurse`, `CLINIC-PC\Admin`) {
		t.Error("an administrator approving the uninstall for another account was not detected")
	}
	if differentUser("", `CLINIC-PC\Admin`) {
		t.Error("an unknown console user must not block removal")
	}
}
