package install

import (
	"os/exec"
	"strings"
	"testing"
)

func TestScript(t *testing.T) {
	p := Plan{Binary: "/Applications/CARE Clinic.app/Contents/MacOS/care-clinic-service", Plist: []byte("<plist/>"), User: "nurse"}
	s, err := Script(p, "/tmp/x.plist")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"dseditgroup -o create -r 'CARE Clinic service users' 'care-clinic'",
		"dseditgroup -o edit -a 'nurse' -t user 'care-clinic'",
		"install -o root -g wheel -m 0755 '/Applications/CARE Clinic.app/Contents/MacOS/care-clinic-service' '/Library/Application Support/care-clinic/bin/care-clinic-service.new'",
		"install -o root -g wheel -m 0644 '/tmp/x.plist' '/Library/LaunchDaemons/org.ohc.care-clinic.plist'",
		"launchctl bootstrap system '/Library/LaunchDaemons/org.ohc.care-clinic.plist'",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("script lacks %q:\n%s", want, s)
		}
	}
	if strings.Index(s, "bootout") > strings.Index(s, "bootstrap") {
		t.Error("bootstrap before bootout")
	}
	if out, err := exec.Command("sh", "-n", "-c", s).CombinedOutput(); err != nil {
		t.Errorf("sh -n: %v %s", err, out)
	}
}

func TestScriptRejects(t *testing.T) {
	for _, p := range []Plan{
		{Binary: "/x", Plist: []byte("p"), User: "root"},
		{Binary: "/x", Plist: []byte("p"), User: "a b"},
		{Binary: "/x", Plist: []byte("p"), User: "x'; rm -rf /"},
		{Binary: "rel", Plist: []byte("p"), User: "u"},
		{Binary: "/x", User: "u"},
	} {
		if _, err := Script(p, "/tmp/p"); err == nil {
			t.Errorf("accepted %+v", p)
		}
	}
}
