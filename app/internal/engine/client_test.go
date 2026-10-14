package engine

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecuteRequiresTerminalExitFrame(t *testing.T) {
	for _, tc := range []struct {
		name, frames string
		success      bool
		code         int
	}{
		{"success", "{\"stream\":\"stdout\",\"data\":\"ready\\n\"}\n{\"exit\":0}\n", true, 0},
		{"failure", "{\"exit\":9}\n", false, 9},
		{"truncated", "{\"stream\":\"stdout\",\"data\":\"ready\\n\"}\n", false, 0},
		{"malformed", "not json\n", false, 0},
		{"start failure", "{\"error\":\"docker missing\"}\n", false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1/jobs" {
					t.Errorf("wrong endpoint: %s %s", r.Method, r.URL.Path)
				}
				_, _ = w.Write([]byte(tc.frames))
			}))
			defer server.Close()
			var output strings.Builder
			err := ForURL(server.URL).Execute(context.Background(), Request{Operation: "compose", Args: []string{"ps"}},
				func(_, data string) { output.WriteString(data) })
			if (err == nil) != tc.success {
				t.Fatalf("error=%v", err)
			}
			if tc.code != 0 {
				var exit *ExitError
				if !errors.As(err, &exit) || exit.Code != tc.code {
					t.Fatalf("lost remote exit status: %v", err)
				}
			}
			if tc.success && output.String() != "ready\n" {
				t.Fatalf("output=%q", output.String())
			}
		})
	}
}

func TestExecuteReportsAgentHTTPFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "engine unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	err := ForURL(server.URL).Execute(context.Background(), Request{Operation: "version"}, nil)
	if err == nil || !strings.Contains(err.Error(), "engine unavailable") {
		t.Fatalf("agent failure hidden: %v", err)
	}
}

func TestDownloadFailurePreservesExistingExport(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("short"))
	}))
	defer server.Close()
	dir := t.TempDir()
	target := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(target, []byte("previous verified manifest"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ForURL(server.URL).Get(context.Background(), "/var/lib/care/backups/manifest.json", target); err == nil {
		t.Fatal("truncated transfer was accepted")
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "previous verified manifest" {
		t.Fatalf("previous export changed: %q %v", data, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary download was not cleaned: %v %v", entries, err)
	}
}

func TestExecuteCancellationReachesAgent(t *testing.T) {
	cancelled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{\"stream\":\"stdout\",\"data\":\"started\"}\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(cancelled)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := ForURL(server.URL).Execute(ctx, Request{Operation: "exec"}, func(_, _ string) { cancel() })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	<-cancelled
}
