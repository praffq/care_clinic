package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"strings"
	"sync"
	"time"
)

type operationRequest struct {
	Operation string   `json:"operation"`
	Args      []string `json:"args"`
	Dir       string   `json:"dir,omitempty"`
	Env       []string `json:"env,omitempty"`
}

type jobEvent struct {
	Stream string `json:"stream,omitempty"`
	Data   string `json:"data,omitempty"`
	Error  string `json:"error,omitempty"`
	Exit   *int   `json:"exit,omitempty"`
}

var operations = map[string]bool{
	"compose": true, "build": true, "image": true, "pull": true,
	"inspect": true, "run": true, "exec": true, "ps": true, "buildx": true,
	"images": true, "volume": true, "rm": true, "info": true,
	"version": true, "system": true, "network": true, "stop": true,
	"builder": true,
}

func validateOperation(in operationRequest) error {
	if !operations[in.Operation] {
		return fmt.Errorf("unsupported engine operation %q", in.Operation)
	}
	if in.Dir != "" && (!path.IsAbs(in.Dir) || path.Clean(in.Dir) != in.Dir) {
		return fmt.Errorf("operation directory must be a clean absolute guest path")
	}
	for _, entry := range in.Env {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || key == "" || strings.ContainsAny(key, "\x00\r\n") ||
			strings.HasPrefix(key, "DOCKER_") || key == "PATH" ||
			key == "LD_PRELOAD" || key == "LD_LIBRARY_PATH" {
			return fmt.Errorf("invalid operation environment key")
		}
	}
	return nil
}

type jobWriter struct {
	mu  *sync.Mutex
	enc *json.Encoder
	w   http.ResponseWriter
	out string
}

func (w jobWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.enc.Encode(jobEvent{Stream: w.out, Data: string(p)}); err != nil {
		return 0, err
	}
	if err := http.NewResponseController(w.w).Flush(); err != nil {
		return 0, err
	}
	return len(p), nil
}

// The request lifetime is the job lifetime: disconnecting or cancelling the
// desktop request terminates the process, including inherited subprocesses.
func (a *api) job(w http.ResponseWriter, r *http.Request) {
	var in operationRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		http.Error(w, "invalid operation request", http.StatusBadRequest)
		return
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		http.Error(w, "expected one operation request", http.StatusBadRequest)
		return
	}
	if err := validateOperation(in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	cmd := exec.CommandContext(r.Context(), "docker", append([]string{in.Operation}, in.Args...)...)
	cmd.Dir = in.Dir
	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env, in.Env...)
	cmd.WaitDelay = 2 * time.Second
	configureJob(cmd)
	w.Header().Set("Content-Type", "application/x-ndjson")
	enc := json.NewEncoder(w)
	var mu sync.Mutex
	cmd.Stdout = jobWriter{mu: &mu, enc: enc, w: w, out: "stdout"}
	cmd.Stderr = jobWriter{mu: &mu, enc: enc, w: w, out: "stderr"}
	err := cmd.Run()
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			_ = enc.Encode(jobEvent{Error: "cannot execute engine operation: " + err.Error()})
			return
		}
		code = exit.ExitCode()
	}
	_ = enc.Encode(jobEvent{Exit: &code})
}
