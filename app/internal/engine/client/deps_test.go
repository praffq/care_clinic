package client

import (
	"os/exec"
	"strings"
	"testing"
)

// The desktop binary must not link the in-VM agent (DockerExecutor, os/exec
// plumbing); wire-level types belong in protocol. Test files may import agent.
func TestNoAgentDependency(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go tool not in PATH")
	}
	out, err := exec.Command(gobin, "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	for _, dep := range strings.Fields(string(out)) {
		if strings.HasSuffix(dep, "/internal/engine/agent") || strings.Contains(dep, "/internal/engine/agent/") {
			t.Fatalf("client depends on %s", dep)
		}
	}
	if !strings.Contains(string(out), "/internal/engine/protocol") {
		t.Fatalf("unexpected go list output:\n%s", out)
	}
}
