package proc

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// fakeDocker puts a "docker" script on PATH that records each run and exits
// with the given status.
func fakeDocker(t *testing.T, status int) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX command fixture")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	script := "#!/bin/sh\necho run >> '" + marker + "'\necho out\nexit " + string(rune('0'+status)) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return marker
}

func setHook(t *testing.T, hook func([]string) ([]string, func() error, error)) {
	t.Helper()
	old := DockerHook
	DockerHook = hook
	t.Cleanup(func() { DockerHook = old })
}

type runVariant struct {
	name string
	run  func(Runner) error
}

var runVariants = []runVariant{
	{"RunWith", func(r Runner) error { return r.RunWith(nil, "docker", "x") }},
	{"Capture", func(r Runner) error { _, err := r.Capture("docker", "x"); return err }},
	{"CaptureIn", func(r Runner) error { _, err := r.CaptureIn(5*time.Second, nil, "docker", "x"); return err }},
	{"Lines", func(r Runner) error { _, err := r.Lines("docker", "x"); return err }},
}

func TestPrepareFailurePreventsCommand(t *testing.T) {
	prep := errors.New("sync before failed")
	for _, v := range runVariants {
		t.Run(v.name, func(t *testing.T) {
			marker := fakeDocker(t, 0)
			afterCalled := false
			setHook(t, func(args []string) ([]string, func() error, error) {
				return nil, func() error { afterCalled = true; return nil }, prep
			})
			err := v.run(Runner{})
			var hook *HookError
			if !errors.Is(err, prep) || !errors.As(err, &hook) || hook.Stage != "prepare" {
				t.Fatalf("err = %v", err)
			}
			if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
				t.Fatal("docker ran despite a preparation failure")
			}
			if afterCalled {
				t.Fatal("after ran despite a preparation failure")
			}
		})
	}
}

func TestFinishFailureFailsSuccessfulCommand(t *testing.T) {
	finish := errors.New("sync after failed")
	for _, v := range runVariants {
		t.Run(v.name, func(t *testing.T) {
			marker := fakeDocker(t, 0)
			setHook(t, func(args []string) ([]string, func() error, error) {
				return args, func() error { return finish }, nil
			})
			err := v.run(Runner{})
			var hook *HookError
			if !errors.Is(err, finish) || !errors.As(err, &hook) || hook.Stage != "finish" {
				t.Fatalf("err = %v", err)
			}
			if _, statErr := os.Stat(marker); statErr != nil {
				t.Fatal("docker did not run")
			}
		})
	}
}

func TestFinishFailureJoinsCommandFailure(t *testing.T) {
	finish := errors.New("sync after failed")
	for _, v := range runVariants {
		t.Run(v.name, func(t *testing.T) {
			fakeDocker(t, 3)
			setHook(t, func(args []string) ([]string, func() error, error) {
				return args, func() error { return finish }, nil
			})
			err := v.run(Runner{})
			var exit *exec.ExitError
			if !errors.Is(err, finish) || !errors.As(err, &exit) || exit.ExitCode() != 3 {
				t.Fatalf("both causes not kept: %v", err)
			}
		})
	}
}

func TestHookRewritesAndSucceeds(t *testing.T) {
	for _, v := range runVariants {
		t.Run(v.name, func(t *testing.T) {
			fakeDocker(t, 0)
			var got []string
			finished := 0
			setHook(t, func(args []string) ([]string, func() error, error) {
				got = args
				return append(args, "rewritten"), func() error { finished++; return nil }, nil
			})
			if err := v.run(Runner{}); err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0] != "x" || finished != 1 {
				t.Fatalf("args %v finished %d", got, finished)
			}
		})
	}
}

func TestHookIgnoredForOtherCommands(t *testing.T) {
	setHook(t, func(args []string) ([]string, func() error, error) {
		return nil, nil, errors.New("must not be called")
	})
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX command fixture")
	}
	if _, err := (Runner{}).Capture("/bin/sh", "-c", "true"); err != nil {
		t.Fatal(err)
	}
}
