package prereq

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/proc"
)

func TestDockerSocketPermissionDenialRequestsNewLogin(t *testing.T) {
	err := &exec.ExitError{Stderr: []byte("permission denied while connecting to /var/run/docker.sock")}
	if !dockerSocketPermissionDenied(err) {
		t.Fatal("Docker socket permission denial was not recognized")
	}
	if dockerSocketPermissionDenied(&exec.ExitError{Stderr: []byte("cannot connect to Docker daemon")}) {
		t.Fatal("a stopped daemon was misclassified as a group refresh")
	}
}

func TestDockerInstallReadinessAcceptsDaemonNeedingGroupRefresh(t *testing.T) {
	logged := ""
	err := waitForDockerReadyOrLoginRefresh(time.Second, func() Status {
		return Status{Message: dockerLoginAdvice}
	}, func(message string) { logged = message })
	if err != nil {
		t.Fatalf("group refresh was treated as install failure: %v", err)
	}
	if logged != dockerLoginAdvice {
		t.Fatalf("group refresh guidance was not logged: %q", logged)
	}
}

func TestNativeLinuxDockerPluginPackageNames(t *testing.T) {
	for pm, want := range map[string]string{
		"apt":    "docker-compose-v2 docker-buildx",
		"dnf":    "docker-compose-plugin docker-buildx-plugin",
		"zypper": "docker-compose docker-buildx",
		"pacman": "docker-compose docker-buildx",
	} {
		got := strings.Join(linuxDockerPluginPackages(pm), " ")
		if got != want {
			t.Errorf("%s: packages %q, want %q", pm, got, want)
		}
	}
	if got := linuxDockerPluginPackages("unknown"); len(got) != 0 {
		t.Errorf("unsupported package manager returned packages: %v", got)
	}
}

func TestNativeAgentServiceUsesProtectedGroupSocket(t *testing.T) {
	unit := careAgentServiceUnit("1000")
	for _, expected := range []string{
		"User=root", "Group=1000", "UMask=0007",
		"RuntimeDirectory=care-engine-agent", "RuntimeDirectoryMode=0750",
		"ExecStart=/usr/local/bin/care-engine-agent --socket /run/care-engine-agent/control.sock",
		"Environment=PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	} {
		if !strings.Contains(unit, expected) {
			t.Errorf("native agent service lacks %q", expected)
		}
	}
}

func TestNativeAgentDirectoriesKeepPrivateStateAndWritableBackupMount(t *testing.T) {
	command := nativeDataDirSetupCommand()
	for _, expected := range []string{
		"-m 0700 /var/lib/care",
		"-m 0700 /var/lib/care/clinic",
		"-m 0755 /var/lib/care/backups",
	} {
		if !strings.Contains(command, expected) {
			t.Errorf("native agent directory setup lacks %q: %s", expected, command)
		}
	}
}

func TestDockerCheckUsesAgentVersionOperation(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := exec.LookPath("docker"); err == nil {
		t.Fatal("readiness regression fixture unexpectedly has a host Docker CLI")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request engine.Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode agent request: %v", err)
			return
		}
		if request.Operation != "version" ||
			strings.Join(request.Args, " ") != `--format {{.Server.Os}}/{{.Server.Version}}` {
			t.Errorf("unexpected agent request: %+v", request)
		}
		_, _ = fmt.Fprintln(w, `{"stream":"stdout","data":"linux/29.0\n"}`)
		_, _ = fmt.Fprintln(w, `{"exit":0}`)
	}))
	defer server.Close()
	status := DockerCheck(proc.Runner{Agent: engine.ForURL(server.URL)})
	if !status.OK || !strings.Contains(status.Message, "Docker 29.0") {
		t.Fatalf("agent readiness failed: %+v", status)
	}
}
