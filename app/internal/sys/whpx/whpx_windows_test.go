package whpx

import (
	"errors"
	"testing"
)

// Hosted runners may or may not expose the hypervisor; Check must answer
// one of its three outcomes without crashing.
func TestCheck(t *testing.T) {
	err := Check()
	if err != nil && !errors.Is(err, ErrFeatureOff) && !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Check() = %v", err)
	}
	t.Logf("WHPX: %v", err)
}
