package residue

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/sys/proc"
)

func noSystemTraces(proc.Runner) ([]Trace, error) { return nil, nil }

func fakeDocker(t *testing.T, script string) (string, proc.Runner) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX command fixtures")
	}
	root := t.TempDir()
	t.Setenv("HOME", root)
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if script != "" {
		if err := os.WriteFile(filepath.Join(bin, "docker"), []byte("#!/bin/sh\n"+script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	return root, proc.Runner{Env: os.Environ()}
}

func TestCleanComputerSerializesAnEmptyList(t *testing.T) {
	root, run := fakeDocker(t, "")
	report, err := scan(Options{Runner: run, InstallDir: filepath.Join(root, "install")}, noSystemTraces)
	if err != nil || !report.Clean || len(report.Traces) != 0 {
		t.Fatalf("a computer without leftovers is not dirty: %+v, %v", report, err)
	}
	data, err := json.Marshal(report)
	if err != nil || string(data) != `{"clean":true,"traces":[]}` {
		t.Fatalf("empty scan must serialize traces as an array: %s, %v", data, err)
	}
}

func TestStoppedDockerIsNotABlocker(t *testing.T) {
	root, run := fakeDocker(t, "exit 1\n")
	report, err := scan(Options{Runner: run, InstallDir: filepath.Join(root, "install")}, noSystemTraces)
	if err != nil || !report.Clean {
		t.Fatalf("a stopped Docker can't hold an old clinic open: %+v, %v", report, err)
	}
}

const earlierClinic = `case "$1" in
version) echo 27.0 ;;
ps) echo c1; echo c2 ;;
volume) echo care-clinic_db ;;
network) ;;
images) echo ghcr.io/ohcnetwork/care:latest; echo care-clinic-dev/backend:arm64; echo postgres:16 ;;
rm|image) ;;
*) exit 1 ;;
esac
`

func TestEarlierDockerClinicBlocksSetup(t *testing.T) {
	root, run := fakeDocker(t, earlierClinic)
	report, err := scan(Options{Runner: run, InstallDir: filepath.Join(root, "install")}, noSystemTraces)
	if err != nil || report.Clean {
		t.Fatalf("the earlier clinic's containers were missed: %+v, %v", report, err)
	}
	got := map[string]string{}
	for _, trace := range report.Traces {
		got[trace.ID] = trace.Detail
	}
	if !strings.HasPrefix(got["containers"], "2 containers") || !strings.HasPrefix(got["volumes"], "1 data volume") ||
		!strings.HasPrefix(got["images"], "1 Docker image") || len(got) != 3 {
		t.Fatalf("traces = %+v", report.Traces)
	}
}

func TestCachedImagesDoNotBlockSetup(t *testing.T) {
	root, run := fakeDocker(t, "case \"$1\" in version) echo 27.0 ;; images) echo ghcr.io/ohcnetwork/care_fe:v1 ;; esac\n")
	report, err := scan(Options{Runner: run, InstallDir: filepath.Join(root, "install")}, noSystemTraces)
	if err != nil || !report.Clean || len(report.Traces) != 1 || report.Traces[0].ID != "images" {
		t.Fatalf("cached images should be reported but not block: %+v, %v", report, err)
	}
	if err := os.MkdirAll(filepath.Join(root, "install"), 0o700); err != nil {
		t.Fatal(err)
	}
	report, err = scan(Options{Runner: run, InstallDir: filepath.Join(root, "install")}, noSystemTraces)
	if err != nil || report.Clean {
		t.Fatalf("installed files should still block: %+v, %v", report, err)
	}
}

func TestFilesFromAnEarlierSetupBlock(t *testing.T) {
	root, run := fakeDocker(t, "")
	state := filepath.Join(root, "clinic")
	config := filepath.Join(root, "config.json")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{config, filepath.Join(state, "backup-recovery.key")} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	o := Options{Runner: run, InstallDir: filepath.Join(root, "install"), StateDir: state}
	report, err := scan(o, noSystemTraces)
	if err != nil || !report.Clean {
		t.Fatalf("kept backup keys and unflagged settings are not leftovers: %+v, %v", report, err)
	}
	if err := os.WriteFile(filepath.Join(state, SecretsFile), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	o.ConfigPath = config
	report, err = scan(o, noSystemTraces)
	if err != nil || report.Clean || len(report.Traces) != 2 || report.Traces[0].ID != "secrets" || report.Traces[1].ID != "config" {
		t.Fatalf("old secrets and settings were missed: %+v, %v", report, err)
	}
	if err := RemoveSecrets(state); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(state, "backup-recovery.key")); err != nil {
		t.Fatalf("removing secrets touched the backup key: %v", err)
	}
	o.ConfigPath = ""
	if report, err := scan(o, noSystemTraces); err != nil || !report.Clean {
		t.Fatalf("removed secrets were still reported: %+v, %v", report, err)
	}
}

func TestEngineLeftovers(t *testing.T) {
	root, run := fakeDocker(t, "")
	failure := errors.New("service down")
	for _, tc := range []struct {
		name   string
		engine Engine
		err    error
		trace  bool
		clean  bool
	}{
		{"no engine yet", Engine{}, nil, false, true},
		{"empty engine", Engine{Installed: true, Reachable: true}, nil, false, true},
		{"stopped engine", Engine{Installed: true}, nil, true, false},
		{"running clinic", Engine{Installed: true, Reachable: true, Services: 3}, nil, true, false},
		{"configured clinic", Engine{Installed: true, Reachable: true, Configured: true}, nil, true, false},
		{"service down", Engine{}, failure, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report, err := scan(Options{
				Runner: run, InstallDir: filepath.Join(root, "install"),
				Engine: func() (Engine, error) { return tc.engine, tc.err },
			}, noSystemTraces)
			if report.Clean != tc.clean || !errors.Is(err, tc.err) || (len(report.Traces) == 1) != tc.trace {
				t.Fatalf("engine inspection was misread: %+v, %v", report, err)
			}
			if tc.trace && report.Traces[0].ID != "engine" {
				t.Fatalf("trace = %+v", report.Traces[0])
			}
		})
	}
}

func TestSystemTracesClassifySetupResidue(t *testing.T) {
	root, run := fakeDocker(t, "")
	inspectionError := errors.New("hosts file could not be read")
	for _, tc := range []struct {
		name   string
		traces []Trace
		err    error
		clean  bool
	}{
		{"after cleanup", nil, nil, true},
		{"after network repair", []Trace{{ID: "firewall", Label: "Firewall rules"}}, nil, true},
		{"unknown system state", nil, inspectionError, false},
		{"firewall and inspection failure", []Trace{{ID: "firewall"}}, inspectionError, false},
		{"existing hosts entry", []Trace{{ID: "hosts", Label: "Hosts file entry"}}, nil, false},
		{"firewall and old hosts entry", []Trace{{ID: "firewall"}, {ID: "hosts"}}, nil, false},
		{"firewall and old certificate", []Trace{{ID: "firewall"}, {ID: "certificate"}}, nil, false},
		{"start at login", []Trace{{ID: "autostart"}}, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report, err := scan(Options{Runner: run, InstallDir: filepath.Join(root, "install")},
				func(proc.Runner) ([]Trace, error) { return tc.traces, tc.err })
			if report.Clean != tc.clean || !errors.Is(err, tc.err) || len(report.Traces) != len(tc.traces) {
				t.Fatalf("system inspection was lost: %+v, %v", report, err)
			}
			if report.Traces == nil {
				t.Fatal("scan returned null traces instead of an empty array")
			}
		})
	}
}

func TestRemoveDockerRemovesOnlyTheEarlierClinic(t *testing.T) {
	root, run := fakeDocker(t, "")
	log := filepath.Join(root, "calls")
	script := `echo "$*" >> ` + log + "\n" + earlierClinic
	if err := os.WriteFile(filepath.Join(root, "bin", "docker"), []byte("#!/bin/sh\n"+script), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := RemoveDocker(run, nil); err != nil {
		t.Fatalf("remove: %v", err)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	calls := string(data)
	for _, want := range []string{"rm -f c1 c2", "volume rm -f care-clinic_db", "image rm ghcr.io/ohcnetwork/care:latest"} {
		if !strings.Contains(calls, want+"\n") {
			t.Errorf("missing %q in:\n%s", want, calls)
		}
	}
	for _, kept := range []string{"network rm", "image rm postgres", "image rm care-clinic-dev"} {
		if strings.Contains(calls, kept) {
			t.Errorf("ran %q:\n%s", kept, calls)
		}
	}
}

func TestAutostartFromTheEarlierAppIsRemoved(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses the per-user start-at-login file")
	}
	root := t.TempDir()
	t.Setenv("HOME", root)
	if AutostartEnabled() {
		t.Fatal("an empty home reported start at login")
	}
	p := autostartFile()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !AutostartEnabled() {
		t.Fatal("the earlier start-at-login entry was missed")
	}
	if err := RemoveAutostart(); err != nil || AutostartEnabled() {
		t.Fatalf("start at login was not removed: %v", err)
	}
}
