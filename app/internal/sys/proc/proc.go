package proc

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

const chunkSize = 64 * 1024

const pathMarker = "__care_path__"

// Runner executes commands with a fixed working directory, environment, and log
type Runner struct {
	Dir string
	Env []string
	Log func(string)
	Ctx context.Context
}

func (r Runner) logln(s string) {
	if r.Log != nil {
		r.Log(s)
	}
}

func Command(name string, args ...string) *exec.Cmd {
	c := exec.Command(name, args...)
	hideConsole(c)
	return c
}

func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	c := exec.CommandContext(ctx, name, args...)
	c.WaitDelay = 2 * time.Second
	hideConsole(c)
	return c
}

// DockerHook, when set, sees every docker command a Runner starts. It may
// rewrite the arguments and returns a function to call once the command has
// finished. Windows uses it to map bind-mount paths into the CARE engine and
// keep the folders it mounts in step. A preparation error stops the command
// from running and after is not called; a completion error is joined with the command's own result.
var DockerHook func(args []string) (rewritten []string, after func() error, err error)

// HookError reports a DockerHook failure before or after a docker command.
type HookError struct {
	Stage string // "prepare" or "finish"
	Err   error
}

func (e *HookError) Error() string {
	if e.Stage == "prepare" {
		return "prepare docker command: " + e.Err.Error()
	}
	return "finish docker command: " + e.Err.Error()
}

func (e *HookError) Unwrap() error { return e.Err }

func noAfter() error { return nil }

func dockerHook(name string, args []string) ([]string, func() error, error) {
	if name != "docker" || DockerHook == nil {
		return args, noAfter, nil
	}
	out, after, err := DockerHook(args)
	if err != nil {
		return nil, nil, &HookError{Stage: "prepare", Err: err}
	}
	if after == nil {
		after = noAfter
	}
	return out, func() error {
		if err := after(); err != nil {
			return &HookError{Stage: "finish", Err: err}
		}
		return nil
	}, nil
}

// joinAfter runs after and joins its failure into *err.
func joinAfter(err *error, after func() error) {
	if afterErr := after(); afterErr != nil {
		*err = errors.Join(*err, afterErr)
	}
}

func (r Runner) cmd(name string, args ...string) (*exec.Cmd, func() error, error) {
	args, after, err := dockerHook(name, args)
	if err != nil {
		return nil, nil, err
	}
	c := Command(name, args...)
	if r.Ctx != nil {
		c = CommandContext(r.Ctx, name, args...)
	}
	c.Dir = r.Dir
	c.Env = r.Env
	return c, after, nil
}

func (r Runner) Run(name string, args ...string) error {
	return r.RunWith(nil, name, args...)
}

// RunLauncher discards output through the OS null device, not pipes tied to
// CARE's lifetime or an unbounded inherited file. Readiness is checked separately.
func (r Runner) RunLauncher(timeout time.Duration, name string, args ...string) error {
	parent := r.Ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	cmd := CommandContext(ctx, name, args...)
	cmd.Dir, cmd.Env = r.Dir, r.Env
	// exec.Cmd connects nil stdout/stderr directly to os.DevNull.
	r.logln("Launching " + name + "; output is discarded. Use the application's own logs for diagnostics.")
	err := cmd.Run()
	if ctx.Err() != nil {
		return fmt.Errorf("%s launch interrupted: %w", name, ctx.Err())
	}
	return err
}

func (r Runner) RunWith(extraEnv []string, name string, args ...string) (err error) {
	cmd, after, err := r.cmd(name, args...)
	if err != nil {
		return err
	}
	defer joinAfter(&err, after)
	if len(extraEnv) > 0 {
		cmd.Env = append(cmd.Environ(), extraEnv...)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", name, err)
	}
	streamDone := make(chan struct{})
	defer close(streamDone)
	if r.Ctx != nil {
		go func() {
			select {
			case <-r.Ctx.Done():
				// A helper can inherit these pipes after its parent is killed.
				// Do not let its output hold a timed-out build or recovery open.
				_ = stdout.Close()
				_ = stderr.Close()
			case <-streamDone:
			}
		}()
	}
	var wg sync.WaitGroup
	var outputMu sync.Mutex
	linesSinceNetworkFailure := networkDiagnosticTail

	stream := func(rd io.Reader) {
		defer wg.Done()
		br := bufio.NewReaderSize(rd, chunkSize)
		skipping := false
		for {
			chunk, isPrefix, err := br.ReadLine()
			if len(chunk) > 0 && !skipping {
				outputMu.Lock()
				if networkFailure.Match(chunk) {
					linesSinceNetworkFailure = 0
				} else if linesSinceNetworkFailure < networkDiagnosticTail {
					linesSinceNetworkFailure++
				}
				outputMu.Unlock()
				r.logln(string(chunk))
				if isPrefix {
					r.logln("  (line too long to show in full - truncated)")
				}
			}
			skipping = isPrefix
			if err != nil {
				return
			}
		}
	}
	wg.Add(2)
	go stream(stdout)
	go stream(stderr)
	wg.Wait()
	err = cmd.Wait()
	if r.Ctx != nil && r.Ctx.Err() != nil {
		return fmt.Errorf("%s interrupted: %w", name, r.Ctx.Err())
	}
	if err != nil && linesSinceNetworkFailure < networkDiagnosticTail && (r.Ctx == nil || r.Ctx.Err() == nil) {
		return &NetworkError{Err: err}
	}
	return err
}

// Capture returns trimmed stdout without streaming.
func (r Runner) Capture(name string, args ...string) (_ string, err error) {
	cmd, after, err := r.cmd(name, args...)
	if err != nil {
		return "", err
	}
	defer joinAfter(&err, after)
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

func (r Runner) CaptureIn(timeout time.Duration, extraEnv []string, name string, args ...string) (_ string, err error) {
	parent := r.Ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	args, after, err := dockerHook(name, args)
	if err != nil {
		return "", err
	}
	defer joinAfter(&err, after)
	cmd := CommandContext(ctx, name, args...)
	cmd.Dir, cmd.Env = r.Dir, r.Env
	if len(extraEnv) > 0 {
		cmd.Env = append(cmd.Environ(), extraEnv...)
	}
	out, err := cmd.Output()
	if err := parent.Err(); err != nil {
		return "", fmt.Errorf("%s was stopped: %w", name, err)
	}
	if ctx.Err() != nil {
		return "", fmt.Errorf("%s timed out after %s", name, timeout)
	}
	return strings.TrimSpace(string(out)), err
}

// Lines returns Capture's non-empty output lines, trimmed.
func (r Runner) Lines(name string, args ...string) ([]string, error) {
	out, err := r.Capture(name, args...)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	if out == "" {
		return nil, nil
	}
	var lines []string
	for _, ln := range strings.Split(out, "\n") {
		if s := strings.Trim(ln, " \t\r"); s != "" {
			lines = append(lines, s)
		}
	}
	return lines, nil
}

func AugmentedPath() string {
	var parts []string
	sep := ":"
	if runtime.GOOS == "windows" {
		sep = ";"
		parts = []string{
			`C:\Program Files\Git\bin`,
			`C:\Program Files\Git\cmd`,
		}
	} else {
		parts = []string{
			"/opt/homebrew/bin", "/opt/homebrew/sbin",
			"/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin",
		}
	}
	if existing := os.Getenv("PATH"); existing != "" {
		parts = append(parts, existing)
	}
	return strings.Join(parts, sep)
}

// FixPath widens the process PATH so binary lookups succeed.
func FixPath() {
	var parts []string
	if sp := loginShellPath(); sp != "" {
		parts = append(parts, sp)
	}
	parts = append(parts, AugmentedPath())
	sep := ":"
	if runtime.GOOS == "windows" {
		sep = ";"
	}
	_ = os.Setenv("PATH", strings.Join(parts, sep))
}

func loginShellPath() string {
	if runtime.GOOS == "windows" {
		return ""
	}
	return loginShellPathWithTimeout(3 * time.Second)
}

func loginShellPathWithTimeout(timeout time.Duration) string {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh"
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := CommandContext(ctx, shell, "-lc",
		"printf '"+pathMarker+"%s\\n' \"$PATH\"").Output()
	return pathFromLoginOutput(out, err, ctx.Err())
}

// pathFromLoginOutput accepts a complete marker line when only exec.ErrWaitDelay
// is reported: the shell exited normally but a background child kept the pipe.
// Any other failure, or cancellation, yields no path.
func pathFromLoginOutput(out []byte, err, ctxErr error) string {
	if ctxErr != nil || (err != nil && !errors.Is(err, exec.ErrWaitDelay)) {
		return ""
	}
	lines := strings.Split(string(out), "\n")
	// The final element has no terminating newline, so the line is incomplete.
	for _, line := range lines[:len(lines)-1] {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), pathMarker); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func Exists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func FileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
