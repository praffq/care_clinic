package reboot

import (
	"strings"
	"testing"
)

func TestPendingRestartPlanDoesNotAttributeCauseToTheHypervisor(t *testing.T) {
	if got := pendingPlan(false); got.Needed {
		t.Fatalf("no-pending-restart plan = %+v", got)
	}
	got := pendingPlan(true)
	if !got.Needed || !strings.Contains(got.Detail, "pending restart") {
		t.Fatalf("pending-restart plan = %+v", got)
	}
	if strings.Contains(strings.ToLower(got.Title+got.Detail), "hypervisor") ||
		strings.Contains(strings.ToLower(got.Title+got.Detail), "docker") {
		t.Fatalf("generic Windows restart was blamed on a specific feature: %+v", got)
	}
}
