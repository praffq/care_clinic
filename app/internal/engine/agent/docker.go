package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
)

// Fixed compose layout inside the engine VM. Callers cannot influence these.
const (
	ComposeProject = "care"
	ComposeDir     = "/var/lib/care/clinic"
	ComposeFile    = ComposeDir + "/docker-compose.yml"

	dockerBin          = "docker"
	defaultWaitTimeout = 300
	maxLineBytes       = 8 << 10
	maxCaptureBytes    = 16 << 20
	loadProgressEvery  = 64 << 20

	envSuperuserPassword = "DJANGO_SUPERUSER_PASSWORD"
	envManageUsername    = "CARE_MANAGE_USERNAME"
	envManagePassword    = "CARE_MANAGE_PASSWORD"

	// changePasswordCode is run with manage.py shell -c; the username and
	// password reach it through the environment, never through argv.
	changePasswordCode = `import os
from django.contrib.auth import get_user_model
u = get_user_model().objects.get(username=os.environ["` + envManageUsername + `"])
u.set_password(os.environ["` + envManagePassword + `"])
u.save()
print("password changed for " + u.username)`
)

// Command is one process invocation. Args[0] is the program; there is no
// shell. Env entries are added to the agent's environment.
type Command struct {
	Args   []string
	Dir    string
	Env    []string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// Runner runs a Command to completion.
type Runner func(ctx context.Context, c Command) error

// ExecRunner runs c with os/exec.
func ExecRunner(ctx context.Context, c Command) error {
	cmd := exec.CommandContext(ctx, c.Args[0], c.Args[1:]...)
	cmd.Dir = c.Dir
	cmd.Env = append(os.Environ(), c.Env...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = c.Stdin, c.Stdout, c.Stderr
	cmd.WaitDelay = 10 * time.Second
	return cmd.Run()
}

// DockerExecutor runs ops with the docker CLI and docker compose.
type DockerExecutor struct {
	// Run executes commands; nil uses ExecRunner.
	Run Runner
	// Now returns the current time; nil uses time.Now.
	Now func() time.Time
	// Stat stats files; nil uses os.Stat.
	Stat func(name string) (os.FileInfo, error)
}

var _ Executor = (*DockerExecutor)(nil)

func (d *DockerExecutor) run(ctx context.Context, c Command) error {
	if d.Run != nil {
		return d.Run(ctx, c)
	}
	return ExecRunner(ctx, c)
}

func (d *DockerExecutor) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d *DockerExecutor) stat(name string) (os.FileInfo, error) {
	if d.Stat != nil {
		return d.Stat(name)
	}
	return os.Stat(name)
}

// noStack reports whether the clinic stack has no compose file yet.
func (d *DockerExecutor) noStack() bool {
	_, err := d.stat(ComposeFile)
	return errors.Is(err, os.ErrNotExist)
}

func compose(args ...string) []string {
	return append([]string{dockerBin, "compose",
		"--project-name", ComposeProject,
		"--file", ComposeFile,
		"--project-directory", ComposeDir,
	}, args...)
}

// lineWriter splits output into lines, emits them as log frames (when em is
// set) and remembers the last line and, optionally, every line.
type lineWriter struct {
	em      Emitter
	service protocol.Service
	stream  string
	keep    bool

	buf   []byte
	last  string
	lines []string
}

func (l *lineWriter) Write(p []byte) (int, error) {
	for _, b := range p {
		if b == '\n' {
			l.line()
			continue
		}
		if len(l.buf) < maxLineBytes {
			l.buf = append(l.buf, b)
		}
	}
	return len(p), nil
}

func (l *lineWriter) line() {
	s := strings.TrimRight(string(l.buf), "\r")
	l.buf = l.buf[:0]
	if strings.TrimSpace(s) == "" {
		return
	}
	l.last = s
	if l.keep && len(l.lines) < 1000 {
		l.lines = append(l.lines, s)
	}
	if l.em != nil {
		l.em.Log(protocol.LogLine{Service: l.service, Stream: l.stream, Line: s})
	}
}

func (l *lineWriter) flush() {
	if len(l.buf) > 0 {
		l.line()
	}
}

// commandError describes a failed step; output is the last stderr line.
func commandError(ctx context.Context, step string, err error, output string) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("%s: %w", step, ctxErr)
	}
	msg := fmt.Sprintf("%s failed: %v", step, err)
	if output != "" {
		msg += ": " + output
	}
	return &protocol.OpError{Code: CodeCommandFailed, Message: msg}
}

// stream runs args, emitting stdout and stderr lines tagged with svc.
func (d *DockerExecutor) stream(ctx context.Context, em Emitter, step string, svc protocol.Service, c Command) (*lineWriter, error) {
	out := &lineWriter{em: em, service: svc, stream: "stdout", keep: true}
	errw := &lineWriter{em: em, service: svc, stream: "stderr"}
	c.Stdout, c.Stderr = out, errw
	err := d.run(ctx, c)
	out.flush()
	errw.flush()
	if err != nil {
		return out, commandError(ctx, step, err, errw.last)
	}
	return out, nil
}

// capture runs args and returns stdout (bounded); stderr is kept for errors.
func (d *DockerExecutor) capture(ctx context.Context, step string, c Command) ([]byte, error) {
	var out bytes.Buffer
	errw := &lineWriter{}
	c.Stdout, c.Stderr = &limitWriter{w: &out, n: maxCaptureBytes}, errw
	err := d.run(ctx, c)
	errw.flush()
	if err != nil {
		return nil, commandError(ctx, step, err, errw.last)
	}
	return out.Bytes(), nil
}

type limitWriter struct {
	w io.Writer
	n int64
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > l.n {
		return 0, errors.New("command output too large")
	}
	l.n -= int64(len(p))
	return l.w.Write(p)
}

func waitTimeout(sec int) int {
	if sec == 0 {
		return defaultWaitTimeout
	}
	return sec
}

func serviceArgs(svcs []protocol.Service) []string {
	out := make([]string, len(svcs))
	for i, s := range svcs {
		out[i] = string(s)
	}
	return out
}

func (d *DockerExecutor) Ping(ctx context.Context) error {
	_, err := d.capture(ctx, "docker version", Command{Args: []string{dockerBin, "version", "--format", "{{.Server.Version}}"}})
	return err
}

func (d *DockerExecutor) StackUp(ctx context.Context, op protocol.StackUp, em Emitter) error {
	args := []string{"up", "--detach", "--remove-orphans"}
	if op.Wait {
		t := waitTimeout(op.TimeoutSec)
		args = append(args, "--wait", "--wait-timeout", strconv.Itoa(t))
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(t+60)*time.Second)
		defer cancel()
	}
	_, err := d.stream(ctx, em, "compose up", "", Command{Args: compose(append(args, serviceArgs(op.Services)...)...), Dir: ComposeDir})
	return err
}

// StackStop succeeds without doing anything when there is no stack yet.
func (d *DockerExecutor) StackStop(ctx context.Context, op protocol.StackStop, em Emitter) error {
	if d.noStack() {
		return nil
	}
	args := []string{"stop"}
	if op.TimeoutSec != 0 {
		args = append(args, "--timeout", strconv.Itoa(op.TimeoutSec))
	}
	_, err := d.stream(ctx, em, "compose stop", "", Command{Args: compose(append(args, serviceArgs(op.Services)...)...), Dir: ComposeDir})
	return err
}

// StackDown succeeds without doing anything when there is no stack yet.
func (d *DockerExecutor) StackDown(ctx context.Context, op protocol.StackDown, em Emitter) error {
	if d.noStack() {
		return nil
	}
	args := []string{"down", "--remove-orphans"}
	if op.RemoveVolumes && op.ConfirmPurge == protocol.PurgeConfirmation {
		args = append(args, "--volumes")
	}
	_, err := d.stream(ctx, em, "compose down", "", Command{Args: compose(args...), Dir: ComposeDir})
	return err
}

type composePS struct {
	Service string `json:"Service"`
	State   string `json:"State"`
	Health  string `json:"Health"`
	Image   string `json:"Image"`
}

func (d *DockerExecutor) StackStatus(ctx context.Context) (protocol.StackStatusResult, error) {
	res := protocol.StackStatusResult{Services: []protocol.ServiceState{}}
	// A fresh engine has no stack yet: report it as empty, not as a failure.
	if d.noStack() {
		return res, nil
	}
	out, err := d.capture(ctx, "compose ps", Command{Args: compose("ps", "--all", "--format", "json"), Dir: ComposeDir})
	if err != nil {
		return res, err
	}
	rows, err := parseComposePS(out)
	if err != nil {
		return res, &protocol.OpError{Code: CodeCommandFailed, Message: "compose ps: " + err.Error()}
	}
	for _, r := range rows {
		svc := protocol.Service(r.Service)
		if !svc.Valid() {
			continue
		}
		st := protocol.ServiceState{Name: svc, State: r.State, Health: r.Health}
		if i := strings.LastIndex(r.Image, "@"); i >= 0 && protocol.Digest(r.Image[i+1:]).Valid() {
			st.Image = protocol.Digest(r.Image[i+1:])
		}
		res.Services = append(res.Services, st)
	}
	slices.SortFunc(res.Services, func(a, b protocol.ServiceState) int { return strings.Compare(string(a.Name), string(b.Name)) })
	return res, nil
}

// parseComposePS accepts both the JSON array (older compose) and the NDJSON
// (compose >= 2.21) output of "compose ps --format json".
func parseComposePS(out []byte) ([]composePS, error) {
	out = bytes.TrimSpace(out)
	var rows []composePS
	if len(out) == 0 {
		return rows, nil
	}
	if out[0] == '[' {
		err := json.Unmarshal(out, &rows)
		return rows, err
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var r composePS
		if err := dec.Decode(&r); errors.Is(err, io.EOF) {
			return rows, nil
		} else if err != nil {
			return nil, err
		}
		rows = append(rows, r)
	}
}

func (d *DockerExecutor) StackLogs(ctx context.Context, op protocol.StackLogs, em Emitter) error {
	if d.noStack() {
		return &protocol.OpError{Code: CodeNotInstalled, Message: "the clinic stack is not installed"}
	}
	args := []string{"logs", "--no-color", "--no-log-prefix", "--tail", strconv.Itoa(op.TailLines)}
	if op.Since != nil {
		args = append(args, "--since", op.Since.UTC().Format(time.RFC3339))
	}
	args = append(args, string(op.Service))
	_, err := d.stream(ctx, em, "compose logs", op.Service, Command{Args: compose(args...), Dir: ComposeDir})
	return err
}

func (d *DockerExecutor) RunMigrations(ctx context.Context, em Emitter) error {
	em.Progress(protocol.Progress{Service: protocol.ServiceMigrate, State: "running"})
	_, err := d.stream(ctx, em, "migrations", protocol.ServiceMigrate,
		Command{Args: compose("run", "--rm", "-T", string(protocol.ServiceMigrate)), Dir: ComposeDir})
	if err != nil {
		return err
	}
	em.Progress(protocol.Progress{Service: protocol.ServiceMigrate, State: "done"})
	return nil
}

type imageInfo struct {
	ID          string   `json:"Id"`
	RepoDigests []string `json:"RepoDigests"`
}

func (d *DockerExecutor) inspectImage(ctx context.Context, ref string) (imageInfo, error) {
	var info imageInfo
	out, err := d.capture(ctx, "image inspect", Command{Args: []string{dockerBin, "image", "inspect", "--format", "{{json .}}", ref}})
	if err != nil {
		return info, err
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &info); err != nil {
		return info, &protocol.OpError{Code: CodeCommandFailed, Message: "image inspect: " + err.Error()}
	}
	return info, nil
}

// matchesDigest reports whether the image is digest, by repo digest or by ID.
func (i imageInfo) matchesDigest(digest protocol.Digest) bool {
	if i.ID == string(digest) {
		return true
	}
	return slices.ContainsFunc(i.RepoDigests, func(rd string) bool { return strings.HasSuffix(rd, "@"+string(digest)) })
}

func (d *DockerExecutor) ImagePull(ctx context.Context, op protocol.ImagePull, em Emitter) error {
	repo, _, _ := strings.Cut(op.Ref, "@")
	ref := repo + "@" + string(op.Digest)
	em.Progress(protocol.Progress{State: "pulling", Message: ref})
	if _, err := d.stream(ctx, em, "image pull", "", Command{Args: []string{dockerBin, "pull", ref}}); err != nil {
		return err
	}
	em.Progress(protocol.Progress{State: "verifying", Message: ref})
	info, err := d.inspectImage(ctx, ref)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(info.RepoDigests, func(rd string) bool { return strings.HasSuffix(rd, "@"+string(op.Digest)) }) {
		return &protocol.OpError{Code: CodeDigestMismatch, Message: "pulled image does not carry digest " + string(op.Digest)}
	}
	return nil
}

// countingReader reports progress while an image tarball streams into docker.
type countingReader struct {
	r    io.Reader
	em   Emitter
	n    int64
	next int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	if c.n >= c.next {
		c.em.Progress(protocol.Progress{State: "loading", Bytes: c.n})
		c.next = c.n + loadProgressEvery
	}
	return n, err
}

func (d *DockerExecutor) ImageLoad(ctx context.Context, op protocol.ImageLoad, tar io.Reader, em Emitter) error {
	if tar == nil {
		return &protocol.OpError{Code: protocol.CodeBadRequest, Message: "image tarball body is required"}
	}
	cr := &countingReader{r: tar, em: em, next: loadProgressEvery}
	out, err := d.stream(ctx, em, "image load", "", Command{Args: []string{dockerBin, "load"}, Stdin: cr})
	if err != nil {
		return err
	}
	em.Progress(protocol.Progress{State: "verifying", Bytes: cr.n})
	var refs []string
	for _, l := range out.lines {
		if v, ok := strings.CutPrefix(l, "Loaded image ID: "); ok {
			refs = append(refs, strings.TrimSpace(v))
		} else if v, ok := strings.CutPrefix(l, "Loaded image: "); ok {
			refs = append(refs, strings.TrimSpace(v))
		}
	}
	if len(refs) == 0 {
		return &protocol.OpError{Code: CodeCommandFailed, Message: "docker load reported no loaded image"}
	}
	matched := false
	for _, ref := range refs {
		info, err := d.inspectImage(ctx, ref)
		if err != nil {
			return err
		}
		if info.matchesDigest(op.Digest) {
			matched = true
		}
	}
	if matched {
		return nil
	}
	_ = d.run(ctx, Command{Args: append([]string{dockerBin, "image", "rm"}, refs...), Stdout: io.Discard, Stderr: io.Discard})
	return &protocol.OpError{Code: CodeDigestMismatch, Message: "loaded image does not match digest " + string(op.Digest)}
}

func (d *DockerExecutor) ImageBuild(context.Context, protocol.ImageBuild, Emitter) (protocol.ImageBuildResult, error) {
	return protocol.ImageBuildResult{}, ErrNotImplemented
}

type imageRow struct {
	ID         string `json:"ID"`
	Repository string `json:"Repository"`
	Tag        string `json:"Tag"`
	Digest     string `json:"Digest"`
}

// ImagePrune removes every image that is neither listed in op.Keep (by image
// ID or repo digest) nor used by a container. Images in use make docker
// refuse the removal; those are reported as skipped.
func (d *DockerExecutor) ImagePrune(ctx context.Context, op protocol.ImagePrune, em Emitter) error {
	out, err := d.capture(ctx, "image ls", Command{Args: []string{dockerBin, "image", "ls", "--digests", "--no-trunc", "--format", "{{json .}}"}})
	if err != nil {
		return err
	}
	keep := map[string]bool{}
	for _, k := range op.Keep {
		keep[string(k)] = true
	}
	type image struct {
		refs []string
		kept bool
	}
	images := map[string]*image{}
	var order []string
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var r imageRow
		if err := dec.Decode(&r); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return &protocol.OpError{Code: CodeCommandFailed, Message: "image ls: " + err.Error()}
		}
		img := images[r.ID]
		if img == nil {
			img = &image{}
			images[r.ID] = img
			order = append(order, r.ID)
		}
		img.kept = img.kept || keep[r.ID] || keep[r.Digest]
		switch {
		case r.Tag != "" && r.Tag != "<none>":
			img.refs = append(img.refs, r.Repository+":"+r.Tag)
		case r.Digest != "" && r.Digest != "<none>":
			img.refs = append(img.refs, r.Repository+"@"+r.Digest)
		}
	}
	removed := 0
	for _, id := range order {
		img := images[id]
		if img.kept {
			continue
		}
		refs := img.refs
		if len(refs) == 0 {
			refs = []string{id}
		}
		errw := &lineWriter{}
		err := d.run(ctx, Command{Args: append([]string{dockerBin, "image", "rm"}, refs...), Stdout: io.Discard, Stderr: errw})
		errw.flush()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			em.Log(protocol.LogLine{Stream: "stderr", Line: "skipped " + id + ": " + errw.last})
			continue
		}
		removed++
		em.Log(protocol.LogLine{Stream: "stdout", Line: "removed " + id})
	}
	em.Progress(protocol.Progress{State: "done", Message: fmt.Sprintf("removed %d images", removed)})
	return nil
}

func (d *DockerExecutor) DBDump(context.Context, protocol.DBDump, Emitter) (protocol.DBDumpResult, error) {
	return protocol.DBDumpResult{}, ErrNotImplemented
}

func (d *DockerExecutor) DBRestore(context.Context, protocol.DBRestore, Emitter) error {
	return ErrNotImplemented
}

// trimPassword removes the trailing newline a password file usually has.
func trimPassword(p []byte) string {
	return strings.TrimSuffix(strings.TrimSuffix(string(p), "\n"), "\r")
}

func (d *DockerExecutor) Manage(ctx context.Context, op protocol.ManageCmd, password []byte, em Emitter) error {
	manage := []string{"python", "manage.py"}
	var args, env []string
	switch op.Command {
	case protocol.ManageCreateSuperuser, protocol.ManageChangePassword:
		pw := trimPassword(password)
		if pw == "" {
			return &protocol.OpError{Code: protocol.CodeInvalidParams, Message: "password file is empty"}
		}
		if op.Command == protocol.ManageCreateSuperuser {
			email := op.Username
			if !strings.Contains(email, "@") {
				email += "@care.local"
			}
			env = []string{envSuperuserPassword + "=" + pw}
			args = compose("exec", "-T", "-e", envSuperuserPassword, string(protocol.ServiceBackend))
			args = append(args, manage...)
			args = append(args, "createsuperuser", "--noinput", "--username", op.Username, "--email", email)
		} else {
			env = []string{envManageUsername + "=" + op.Username, envManagePassword + "=" + pw}
			args = compose("exec", "-T", "-e", envManageUsername, "-e", envManagePassword, string(protocol.ServiceBackend))
			args = append(args, manage...)
			args = append(args, "shell", "-c", changePasswordCode)
		}
	case protocol.ManageSyncPermissionsRoles, protocol.ManageSyncValueset:
		args = compose("exec", "-T", string(protocol.ServiceBackend))
		args = append(args, manage...)
		args = append(args, string(op.Command))
	default:
		return &protocol.OpError{Code: protocol.CodeInvalidParams, Message: "unknown command " + string(op.Command)}
	}
	_, err := d.stream(ctx, em, "manage "+string(op.Command), protocol.ServiceBackend, Command{Args: args, Dir: ComposeDir, Env: env})
	return err
}

// BackupRun asks the running backup service for a one-shot backup named
// manual-<UTC timestamp>.
func (d *DockerExecutor) BackupRun(ctx context.Context, em Emitter) error {
	name := "manual-" + d.now().UTC().Format("20060102T150405Z")
	em.Progress(protocol.Progress{Service: protocol.ServiceBackup, State: "running", Message: name})
	_, err := d.stream(ctx, em, "backup", protocol.ServiceBackup, Command{
		Args: compose("exec", "-T", string(protocol.ServiceBackup), "/bin/sh", "/backup.sh", "once", name),
		Dir:  ComposeDir,
	})
	return err
}

func (d *DockerExecutor) Containers(ctx context.Context) ([]string, error) {
	out, err := d.capture(ctx, "docker ps", Command{Args: []string{dockerBin, "ps", "--all", "--no-trunc",
		"--format", "{{.Names}}\t{{.Image}}\t{{.State}}\t{{.Status}}"}})
	if err != nil {
		return nil, err
	}
	var lines []string
	for l := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines, nil
}

func (d *DockerExecutor) SetTime(ctx context.Context, now time.Time) error {
	_, err := d.capture(ctx, "date", Command{Args: []string{"date", "-u", "-s", "@" + strconv.FormatInt(now.Unix(), 10)}})
	return err
}

func (d *DockerExecutor) PowerOff(ctx context.Context) error {
	_, err := d.capture(ctx, "poweroff", Command{Args: []string{"poweroff"}})
	return err
}
