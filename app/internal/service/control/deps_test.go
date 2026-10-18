package control

import (
	"os/exec"
	"strings"
	"testing"
)

// The app links this package to reach the service; it must not pull in the
// VM (Virtualization.framework via cgo), the supervisor or the agent.
func TestAppSideDependencies(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go tool not in PATH")
	}
	out, err := exec.Command(gobin, "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	for _, dep := range strings.Fields(string(out)) {
		for _, bad := range []string{"/internal/engine/vm", "/internal/engine/agent", "/internal/service/supervisor"} {
			if strings.HasSuffix(dep, bad) || strings.Contains(dep, bad+"/") {
				t.Fatalf("control depends on %s", dep)
			}
		}
	}
	if !strings.Contains(string(out), "/internal/engine/client") {
		t.Fatalf("unexpected go list output:\n%s", out)
	}
}
