package install

import (
	"os"
	"os/exec"
	"path/filepath"
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

func TestCurrent(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	p := Plan{Binary: write("bundled", "service v2"), Plist: []byte("plist v2"), User: "nurse"}
	binary, plist := filepath.Join(dir, "installed"), filepath.Join(dir, "installed.plist")
	if ok, err := current(p, binary, plist); ok || err != nil {
		t.Fatalf("nothing installed: %v, %v", ok, err)
	}
	write("installed", "service v1")
	write("installed.plist", "plist v2")
	if ok, err := current(p, binary, plist); ok || err != nil {
		t.Fatalf("older binary: %v, %v", ok, err)
	}
	write("installed", "service v2")
	if ok, err := current(p, binary, plist); !ok || err != nil {
		t.Fatalf("same service: %v, %v", ok, err)
	}
	write("installed.plist", "plist v1")
	if ok, err := current(p, binary, plist); ok || err != nil {
		t.Fatalf("older plist: %v, %v", ok, err)
	}
}
