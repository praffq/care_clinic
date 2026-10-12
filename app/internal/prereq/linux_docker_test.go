package prereq

import (
	"os/exec"
	"testing"
	"time"
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
