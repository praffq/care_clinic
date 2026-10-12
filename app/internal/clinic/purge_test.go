package clinic

import (
	"errors"
	"os/exec"
	"testing"
)

func TestDockerCLIStatusOnlyTreatsMissingExecutableAsUnavailable(t *testing.T) {
	if available, err := dockerCLIStatus(nil); !available || err != nil {
		t.Fatalf("installed Docker CLI status = %v, %v; want available", available, err)
	}
	if available, err := dockerCLIStatus(exec.ErrNotFound); available || err != nil {
		t.Fatalf("missing Docker CLI status = %v, %v; want unavailable without error", available, err)
	}
	failure := errors.New("docker returned an unexpected error")
	if available, err := dockerCLIStatus(failure); available || !errors.Is(err, failure) {
		t.Fatalf("unexpected Docker probe failure status = %v, %v; want fail closed", available, err)
	}
}
