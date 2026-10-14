package proc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine"
)

func (r Runner) Context() context.Context {
	if r.Ctx != nil {
		return r.Ctx
	}
	return context.Background()
}

func (r Runner) AgentClient() *engine.Client {
	if r.Agent != nil {
		return r.Agent
	}
	return engine.Default
}

// Only clinic wiring and explicitly referenced container environment variables
// cross the connection. Host PATH, Docker context and unrelated credentials do not.
func operationEnv(env, extra, args []string) []string {
	allowed := map[string]bool{}
	for _, key := range []string{
		"COMPOSE_PROJECT_NAME", "COMPOSE_FILE", "BACKEND_IMAGE", "FRONTEND_IMAGE",
		"POSTGRES_IMAGE", "REDIS_IMAGE", "MINIO_IMAGE", "CADDY_IMAGE", "CADDY_WAF_IMAGE",
		"BACKUP_IMAGE", "MINIO_ACCESS_KEY", "MINIO_SECRET_KEY", "CORAZA_MODE",
		"FILE_UPLOAD_BUCKET", "FACILITY_S3_BUCKET", "BACKUP_DIR", "CARE_BACKUP_RESERVE_KB",
	} {
		allowed[key] = true
	}
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-e" || args[i] == "--env" {
			key, _, _ := strings.Cut(args[i+1], "=")
			allowed[key] = true
		}
	}
	values := map[string]string{}
	var keys []string
	for _, item := range append(append([]string(nil), env...), extra...) {
		key, _, ok := strings.Cut(item, "=")
		if !ok || !allowed[key] {
			continue
		}
		if _, exists := values[key]; !exists {
			keys = append(keys, key)
		}
		values[key] = item
	}
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, values[key])
	}
	return out
}

func (r Runner) executeAgent(extra []string, op engine.Operation, args []string, output func(string, string)) error {
	ctx := r.Context()
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.PrepareAgent != nil {
		if err := r.PrepareAgent(ctx); err != nil {
			return fmt.Errorf("prepare agent inputs: %w", err)
		}
	}
	tail := networkDiagnosticTail
	pending := map[string]string{}
	inspect := func(line string) {
		if networkFailure.MatchString(line) {
			tail = 0
		} else if tail < networkDiagnosticTail {
			tail++
		}
	}
	err := r.AgentClient().Execute(ctx, engine.Request{
		Operation: op, Args: args, Dir: r.AgentDir, Env: operationEnv(r.Env, extra, args),
	}, func(stream, data string) {
		text := pending[stream] + data
		lines := strings.Split(text, "\n")
		for _, line := range lines[:len(lines)-1] {
			inspect(line)
		}
		last := lines[len(lines)-1]
		if len(last) > chunkSize {
			inspect(last[:chunkSize])
			last = ""
		}
		pending[stream] = last
		if output != nil {
			output(stream, data)
		}
	})
	for _, line := range pending {
		if line != "" {
			inspect(line)
		}
	}
	if err != nil && ctx.Err() == nil && tail < networkDiagnosticTail {
		return &NetworkError{Err: err}
	}
	return err
}

func (r Runner) AgentRun(op engine.Operation, args ...string) error {
	return r.AgentRunWith(nil, op, args...)
}

func (r Runner) AgentRunWith(extra []string, op engine.Operation, args ...string) error {
	return r.executeAgent(extra, op, args, func(_, data string) {
		r.logln(strings.TrimRight(data, "\r\n"))
	})
}

func (r Runner) AgentCapture(op engine.Operation, args ...string) (string, error) {
	var out strings.Builder
	err := r.executeAgent(nil, op, args, func(stream, data string) {
		if stream == "stdout" {
			out.WriteString(data)
		} else {
			r.logln(strings.TrimRight(data, "\r\n"))
		}
	})
	return strings.TrimSpace(out.String()), err
}

func (r Runner) AgentCaptureIn(timeout time.Duration, extra []string, op engine.Operation, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	r.Ctx = ctx
	r.Env = append(append([]string(nil), r.Env...), extra...)
	return r.AgentCapture(op, args...)
}

func (r Runner) AgentLines(op engine.Operation, args ...string) ([]string, error) {
	out, err := r.AgentCapture(op, args...)
	if err != nil {
		return nil, err
	}
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines, nil
}

// AgentBuild transfers a deliberately selected context and Dockerfile. It does
// not infer or rewrite host bind paths.
func (r Runner) AgentBuild(dockerfile, contextDir string, args ...string) (err error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return err
	}
	root := "/var/lib/care/builds/" + hex.EncodeToString(id[:])
	client, ctx := r.AgentClient(), r.Context()
	var uploaded []string
	dirs := map[string]bool{root: true, root + "/context": true}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, remote := range uploaded {
			if removeErr := client.Remove(cleanup, remote); removeErr != nil {
				err = errors.Join(err, fmt.Errorf("remove build input: %w", removeErr))
			}
		}
		ordered := make([]string, 0, len(dirs))
		for dir := range dirs {
			ordered = append(ordered, dir)
		}
		sort.Slice(ordered, func(i, j int) bool { return len(ordered[i]) > len(ordered[j]) })
		for _, dir := range ordered {
			if removeErr := client.Remove(cleanup, dir); removeErr != nil {
				err = errors.Join(err, fmt.Errorf("remove build context: %w", removeErr))
			}
		}
	}()
	err = filepath.WalkDir(contextDir, func(local string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("build context contains a non-regular file: %s", local)
		}
		rel, err := filepath.Rel(contextDir, local)
		if err != nil {
			return err
		}
		remote := path.Join(root, "context", filepath.ToSlash(rel))
		for parent := path.Dir(remote); strings.HasPrefix(parent, root+"/"); parent = path.Dir(parent) {
			dirs[parent] = true
		}
		if err := client.Put(ctx, local, remote); err != nil {
			return err
		}
		uploaded = append(uploaded, remote)
		return nil
	})
	if err != nil {
		return err
	}
	// Upload a marker so even an otherwise empty context has a directory.
	marker, err := os.CreateTemp("", "care-build-context-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(marker.Name()) }()
	if err := marker.Close(); err != nil {
		return err
	}
	remoteMarker := root + "/context/.care-context"
	if err := client.Put(ctx, marker.Name(), remoteMarker); err != nil {
		return err
	}
	uploaded = append(uploaded, remoteMarker)
	df := root + "/Dockerfile"
	if err := client.Put(ctx, dockerfile, df); err != nil {
		return err
	}
	uploaded = append(uploaded, df)
	return r.AgentRun("build", append(append([]string{"-f", df}, args...), root+"/context")...)
}
