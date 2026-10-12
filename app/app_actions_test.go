package main

import (
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/prereq"
)

func TestNoActionDockerPlanMustStillHaveReadyStatus(t *testing.T) {
	want := "The CARE engine can't run yet: Windows Hypervisor Platform is disabled."
	err := noActionDockerError(prereq.ToolPlan{}, prereq.Status{Message: want})
	if err == nil || err.Error() != want {
		t.Fatalf("unready no-action plan error = %v, want %q", err, want)
	}
	if err := noActionDockerError(prereq.ToolPlan{}, prereq.Status{OK: true}); err != nil {
		t.Fatalf("ready no-action plan returned an error: %v", err)
	}
	if err := noActionDockerError(prereq.ToolPlan{Action: prereq.ActionManual}, prereq.Status{Message: want}); err != nil {
		t.Fatalf("a manual action was handled as an already-complete plan: %v", err)
	}
}
