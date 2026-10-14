// Package enginetest is an in-process CARE agent for tests. It speaks the
// agent's job and file APIs over httptest, records every operation, and backs
// guest paths with a temporary directory. Nothing here runs a host Docker CLI.
package enginetest

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/engine"
)

// Call is one operation the agent received.
type Call struct {
	Operation string
	Args      []string
	Dir       string
	Env       []string
}

// Command is the operation and arguments as one docker-style line, for
// matching in assertions: "compose exec -T backend ...".
func (c Call) Command() string {
	return strings.TrimSpace(c.Operation + " " + strings.Join(c.Args, " "))
}

// Getenv returns the value of key in the call's forwarded environment.
func (c Call) Getenv(key string) (string, bool) {
	for _, kv := range c.Env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v, true
		}
	}
	return "", false
}

// Result scripts the reply to a call. Exit is the process status; Error is
// an agent-side failure reported instead of an exit frame.
type Result struct {
	Stdout string
	Stderr string
	Exit   int
	Error  string
}

// Handler answers one call. Returning the zero Result is a silent success.
type Handler func(Call) Result

type Agent struct {
	Client *engine.Client
	Root   string

	srv     *httptest.Server
	mu      sync.Mutex
	handler Handler
	calls   []Call
}

// New starts an agent fixture and closes it when the test ends.
func New(t testing.TB, handler Handler) *Agent {
	t.Helper()
	a := &Agent{Root: t.TempDir(), handler: handler}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/jobs", a.job)
	mux.HandleFunc("GET /v1/fs/list", a.list)
	mux.HandleFunc("GET /v1/fs/file", a.getFile)
	mux.HandleFunc("PUT /v1/fs/file", a.putFile)
	mux.HandleFunc("PUT /v1/fs/dir", func(w http.ResponseWriter, r *http.Request) {
		p, ok := a.resolve(w, r.URL.Query().Get("path"))
		if !ok {
			return
		}
		mode, err := strconv.ParseUint(r.URL.Query().Get("mode"), 8, 32)
		if err == nil {
			err = os.MkdirAll(p, os.FileMode(mode))
		}
		if err == nil {
			err = os.Chmod(p, os.FileMode(mode))
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})
	mux.HandleFunc("DELETE /v1/fs/file", a.deleteFile)
	mux.HandleFunc("GET /v1/journal", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "enginetest journal\n")
	})
	a.srv = httptest.NewServer(mux)
	t.Cleanup(a.srv.Close)
	a.Client = engine.ForURL(a.srv.URL)
	return a
}

// Install makes the fixture engine.Default for the rest of the test.
func (a *Agent) Install(t testing.TB) *Agent {
	t.Helper()
	previous := engine.Default
	engine.Default = a.Client
	t.Cleanup(func() { engine.Default = previous })
	return a
}

// SetHandler replaces the reply script.
func (a *Agent) SetHandler(h Handler) {
	a.mu.Lock()
	a.handler = h
	a.mu.Unlock()
}

// Calls returns every operation received so far.
func (a *Agent) Calls() []Call {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.calls)
}

// Commands returns Call.Command for every operation received so far.
func (a *Agent) Commands() []string {
	var out []string
	for _, c := range a.Calls() {
		out = append(out, c.Command())
	}
	return out
}

// Path maps a guest path to the fixture's backing file.
func (a *Agent) Path(guest string) string {
	return filepath.Join(a.Root, filepath.FromSlash(path.Clean("/"+guest)))
}

// WriteFile places a guest file, as if a container had written it.
func (a *Agent) WriteFile(t testing.TB, guest string, data []byte) {
	t.Helper()
	p := a.Path(guest)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// ReadFile reads a guest file, failing the test if it is missing.
func (a *Agent) ReadFile(t testing.TB, guest string) []byte {
	t.Helper()
	b, err := os.ReadFile(a.Path(guest))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Exists reports whether a guest file exists.
func (a *Agent) Exists(guest string) bool {
	_, err := os.Stat(a.Path(guest))
	return err == nil
}

func (a *Agent) job(w http.ResponseWriter, r *http.Request) {
	var req engine.Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	call := Call{Operation: string(req.Operation), Args: req.Args, Dir: req.Dir, Env: req.Env}
	a.mu.Lock()
	a.calls = append(a.calls, call)
	handler := a.handler
	a.mu.Unlock()
	var res Result
	if handler != nil {
		res = handler(call)
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	enc := json.NewEncoder(w)
	emit := func(stream, data string) {
		for _, line := range strings.SplitAfter(data, "\n") {
			if line != "" {
				_ = enc.Encode(engine.Event{Stream: stream, Data: line})
			}
		}
	}
	emit("stdout", res.Stdout)
	emit("stderr", res.Stderr)
	if res.Error != "" {
		_ = enc.Encode(engine.Event{Error: res.Error})
		return
	}
	exit := res.Exit
	_ = enc.Encode(engine.Event{Exit: &exit})
}

func (a *Agent) resolve(w http.ResponseWriter, guest string) (string, bool) {
	if !path.IsAbs(guest) || path.Clean(guest) == "/" {
		http.Error(w, "absolute path required", http.StatusBadRequest)
		return "", false
	}
	return a.Path(guest), true
}

func (a *Agent) list(w http.ResponseWriter, r *http.Request) {
	root, ok := a.resolve(w, r.URL.Query().Get("root"))
	if !ok {
		return
	}
	entries := []engine.Entry{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) && p == root {
				return fs.SkipAll
			}
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		entries = append(entries, engine.Entry{Path: filepath.ToSlash(rel), Size: info.Size(),
			Mode: uint32(info.Mode().Perm()), MtimeNs: info.ModTime().UnixNano()})
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = json.NewEncoder(w).Encode(entries)
}

func (a *Agent) getFile(w http.ResponseWriter, r *http.Request) {
	p, ok := a.resolve(w, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if info, err := os.Stat(p); err == nil {
		w.Header().Set("X-Mtime-Ns", strconv.FormatInt(info.ModTime().UnixNano(), 10))
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	_, _ = w.Write(b)
}

func (a *Agent) putFile(w http.ResponseWriter, r *http.Request) {
	p, ok := a.resolve(w, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	mode := os.FileMode(0o644)
	if m, err := strconv.ParseUint(r.URL.Query().Get("mode"), 8, 32); err == nil {
		mode = os.FileMode(m) & os.ModePerm
	}
	b, err := io.ReadAll(r.Body)
	if err == nil {
		err = os.MkdirAll(filepath.Dir(p), 0o755)
	}
	if err == nil {
		err = os.WriteFile(p, b, 0o600)
	}
	if err == nil {
		err = os.Chmod(p, mode)
	}
	if err != nil {
		http.Error(w, fmt.Sprint(err), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *Agent) deleteFile(w http.ResponseWriter, r *http.Request) {
	p, ok := a.resolve(w, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GuestRoot is the guest tree the agent owns; Exec maps it into Root.
const GuestRoot = "/var/lib/care"

// Exec answers calls by running program, a test-only script standing in for
// the engine's Docker, as `program operation args...`. Guest paths under
// GuestRoot in arguments, environment and working directory are rewritten to
// the fixture's backing files so the script can inspect uploaded inputs.
// Nothing named docker is looked up on PATH.
func (a *Agent) Exec(program string) Handler {
	return func(c Call) Result {
		local := func(s string) string {
			return strings.ReplaceAll(s, GuestRoot, filepath.ToSlash(a.Path(GuestRoot)))
		}
		args := []string{c.Operation}
		for _, arg := range c.Args {
			args = append(args, local(arg))
		}
		cmd := exec.Command(program, args...)
		cmd.Env = os.Environ()
		for _, kv := range c.Env {
			cmd.Env = append(cmd.Env, local(kv))
		}
		if c.Dir != "" {
			if dir := local(c.Dir); dirExists(dir) {
				cmd.Dir = dir
			}
		}
		var stdout, stderr strings.Builder
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		res := Result{Stdout: stdout.String(), Stderr: stderr.String()}
		var exitErr *exec.ExitError
		switch {
		case errors.As(err, &exitErr):
			res.Exit = exitErr.ExitCode()
		case err != nil:
			res.Error = err.Error()
		}
		return res
	}
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}
