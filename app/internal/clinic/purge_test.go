package clinic

import (
	"errors"
	"fmt"
	"io/fs"
	"testing"
)

func TestAgentStatusOnlyTreatsMissingSocketAsUnavailable(t *testing.T) {
	if available, err := agentStatus(nil); !available || err != nil {
		t.Fatalf("answering agent status = %v, %v; want available", available, err)
	}
	missing := fmt.Errorf("CARE agent: dial unix: %w", fs.ErrNotExist)
	if available, err := agentStatus(missing); available || err != nil {
		t.Fatalf("missing agent socket status = %v, %v; want unavailable without error", available, err)
	}
	failure := errors.New("agent operation exited with status 1")
	if available, err := agentStatus(failure); available || !errors.Is(err, failure) {
		t.Fatalf("unexpected agent failure status = %v, %v; want fail closed", available, err)
	}
}
