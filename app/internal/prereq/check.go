package prereq

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/sys/proc"
	"github.com/ohcnetwork/care_desktop/app/internal/vm"
)

const cmdTimeout = 5 * time.Second

type Status struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

func DockerCheck(run proc.Runner) Status {
	out, err := run.AgentCaptureIn(cmdTimeout, nil, "version", "--format", "{{.Server.Os}}/{{.Server.Version}}")
	if err == nil {
		serverOS, version, _ := strings.Cut(strings.TrimSpace(out), "/")
		if problem := wrongContainerOS(serverOS); problem != "" {
			return Status{OK: false, Message: problem}
		}
		if vm.Supported() {
			return Status{OK: true, Message: engineName + " (Docker " + version + ")"}
		}
		return Status{OK: true, Message: "Docker " + version}
	}
	if vm.Supported() {
		return engineCheck()
	}
	if runtime.GOOS == "linux" && agentSocketPermissionDenied(err) {
		return Status{OK: false, Message: agentLoginAdvice}
	}
	return Status{OK: false, Message: "The CARE agent is not running."}
}

const dockerLoginAdvice = "Docker is running, but this login session cannot access it. Log out and back in so your account picks up its new docker group membership, then check again."
const agentLoginAdvice = "The CARE agent is running, but this desktop process cannot access it. Restart the desktop app or log out and back in to refresh group membership, then check again."

func dockerSocketPermissionDenied(err error) bool {
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return false
	}
	output := strings.ToLower(string(exit.Stderr))
	return strings.Contains(output, "permission denied") && strings.Contains(output, "docker.sock")
}

func dockerNeedsLogin(message string) bool {
	return message == dockerLoginAdvice
}

func agentSocketPermissionDenied(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "permission denied") && strings.Contains(message, "control.sock")
}

func agentNeedsLogin(message string) bool {
	return message == agentLoginAdvice
}

func engineCheck() Status {
	if runtime.GOOS == "windows" {
		if err := vm.HypervisorReady(); err != nil {
			return Status{OK: false, Message: "The " + engineName + " can't run yet: " + err.Error() + "."}
		}
	}
	if _, err := vm.Installed(); err != nil || !vm.ToolsInstalled() {
		return Status{OK: false, Message: "The " + engineName + " is not installed."}
	}
	ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
	defer cancel()
	s := vm.Status(ctx)
	switch {
	case s.Error != "":
		return Status{OK: false, Message: "The " + engineName + " could not start: " + s.Error}
	case s.Running:
		return Status{OK: false, Message: "The " + engineName + " is starting. This usually takes under a minute."}
	default:
		return Status{OK: false, Message: "The " + engineName + " is installed but not running."}
	}
}

func wrongContainerOS(serverOS string) string {
	if serverOS == "" || serverOS == "linux" {
		return ""
	}
	return "Docker is set to " + serverOS + " containers, and CARE needs Linux containers."
}
