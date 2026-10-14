package vm

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/engine"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/proc"
)

func TestHelperForwardsAgentJobsWithoutHostDocker(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	var operations []engine.Operation
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/jobs" {
			t.Errorf("unexpected endpoint: %s", r.URL.Path)
		}
		var in engine.Request
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Error(err)
		}
		operations = append(operations, in.Operation)
		_, _ = w.Write([]byte("{\"stream\":\"stdout\",\"data\":\"guest result\\n\"}\n{\"exit\":0}\n"))
	}))
	defer agent.Close()
	target := strings.TrimPrefix(agent.URL, "http://")
	g := newGuest(func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", target)
	})
	defer g.http.CloseIdleConnections()
	h := &host{g: g}
	helper := httptest.NewServer(h.routes())
	defer helper.Close()
	r := proc.Runner{Agent: engine.ForURL(helper.URL)}
	for _, op := range []engine.Operation{"compose", "image", "exec", "build"} {
		output, err := r.AgentCapture(op, "fixture")
		if err != nil || output != "guest result" {
			t.Fatalf("%s output=%q err=%v", op, output, err)
		}
	}
	if len(operations) != 4 {
		t.Fatalf("operations=%v", operations)
	}
}
