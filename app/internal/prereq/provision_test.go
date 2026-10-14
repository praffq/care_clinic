package prereq

import (
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestDockerWaitKeepsTheReadinessDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		err := waitForDockerReady(40*time.Second, func() bool { return false }, func(string) {})
		if err == nil || !strings.Contains(err.Error(), "taking longer than usual") {
			t.Fatalf("missing readiness timeout: %v", err)
		}
		if elapsed := time.Since(start); elapsed != 40*time.Second {
			t.Fatalf("readiness wait exceeded its deadline: %s", elapsed)
		}
	})
}

func TestDockerWaitReturnsOnceReady(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		var logs []string
		err := waitForDockerReady(dockerReadyTimeout,
			func() bool { return time.Since(start) >= time.Minute },
			func(line string) { logs = append(logs, line) })
		if err != nil {
			t.Fatal(err)
		}
		if elapsed := time.Since(start); elapsed < time.Minute || elapsed > time.Minute+3*time.Second {
			t.Fatalf("wait returned after %s", elapsed)
		}
		if len(logs) != 1 || logs[0] != "Docker is ready." {
			t.Fatalf("unexpected progress: %v", logs)
		}
	})
}

func TestWindowsElevatedCommandFailsOnDeclinedUAC(t *testing.T) {
	command := windowsElevatedCommand(`C:\Program Files\CARE\setup.exe`, "/quiet", "O’Brien")
	for _, required := range []string{
		"$ErrorActionPreference = 'Stop'",
		"try { $p = Start-Process",
		"-Verb RunAs",
		"if ($null -eq $p -or $null -eq $p.ExitCode)",
		"catch { [Console]::Error.WriteLine($_.Exception.Message); exit 1 }",
		"exit [int]$p.ExitCode",
	} {
		if !strings.Contains(command, required) {
			t.Errorf("elevated command is missing %q: %s", required, command)
		}
	}
}
