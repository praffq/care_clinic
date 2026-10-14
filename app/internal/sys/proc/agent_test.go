package proc

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine"
)

func TestAgentOperationsNeverForwardHostEnvironment(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req engine.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}

		want := []string{"COMPOSE_PROJECT_NAME=care-clinic", "PGPASSWORD=selected"}
		if req.Operation != "compose" || !reflect.DeepEqual(req.Env, want) || req.Dir != "/var/lib/care/clinic" {
			t.Errorf("unexpected request: %+v", req)
		}
		_, _ = w.Write([]byte("{\"exit\":0}\n"))
	}))
	defer server.Close()
	r := Runner{Agent: engine.ForURL(server.URL), AgentDir: "/var/lib/care/clinic",
		Env: []string{"PATH=/host", "DOCKER_HOST=tcp://other", "PRIVATE_HOST_TOKEN=secret",
			"COMPOSE_PROJECT_NAME=care-clinic", "PGPASSWORD=selected"}}
	if err := r.AgentRun("compose", "exec", "-e", "PGPASSWORD", "db", "pg_isready"); err != nil {
		t.Fatal(err)
	}
}

func TestAgentNetworkClassificationPreservesExitError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{\"stream\":\"stderr\",\"data\":\"could not resolve host: registry.example\\n\"}\n{\"exit\":1}\n"))
	}))
	defer server.Close()
	err := (Runner{Agent: engine.ForURL(server.URL)}).AgentRun("pull", "fixture/image")
	var network *NetworkError
	var exit *engine.ExitError
	if !errors.As(err, &network) || !errors.As(err, &exit) || exit.Code != 1 {
		t.Fatalf("lost network/exit semantics: %v", err)
	}
}

func TestAgentPreparationFailureStopsOperation(t *testing.T) {
	sentinel := errors.New("input upload failed")
	r := Runner{PrepareAgent: func(context.Context) error { return sentinel }}
	if err := r.AgentRun("compose", "up", "-d"); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
}

func TestLocalRunnerRejectsDocker(t *testing.T) {
	r := Runner{}
	if err := r.Run("docker", "ps"); err == nil {
		t.Fatal("host docker was permitted")
	}
	if _, err := r.Capture("docker", "ps"); err == nil {
		t.Fatal("host docker capture was permitted")
	}
	if _, err := r.CaptureIn(time.Second, nil, "docker", "ps"); err == nil {
		t.Fatal("host docker capture was permitted")
	}
	for _, name := range []string{"docker", "docker.exe", filepath.Join(t.TempDir(), "docker"), "docker-compose"} {
		if err := Command(name, "version").Run(); err == nil {
			t.Fatalf("direct host command was permitted: %s", name)
		}
		if err := r.RunLauncher(time.Second, name, "version"); err == nil {
			t.Fatalf("host launcher was permitted: %s", name)
		}
	}
}
