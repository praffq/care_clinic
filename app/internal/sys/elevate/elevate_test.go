package elevate

import (
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/ohcnetwork/care_desktop/app/internal/sys/proc"
)

func TestUnixStepsPreserveEarlierFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fixture")
	}
	cmd := proc.Command("sh", "-c", stepScript([]Step{
		{Sh: "exit 23"},
		{Sh: "printf 'must not run'"},
	}, false))
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 23 || len(out) != 0 {
		t.Fatalf("first failure was masked: %v, %q", err, out)
	}
}

func TestUnixStepsGroupCompoundCommands(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fixture")
	}
	cmd := proc.Command("sh", "-c", stepScript([]Step{
		{Sh: "true || false"},
		{Sh: "printf 'second'"},
	}, false))
	out, err := cmd.CombinedOutput()
	if err != nil || string(out) != "second" {
		t.Fatalf("steps were not independently grouped: %v, %q", err, out)
	}
}

func TestWindowsStepsGenerateFailurePropagatingEncodedScript(t *testing.T) {
	inner := stepScript([]Step{{PS: "Add-Content -Path 'fixture' -Value 'value'"}, {PS: "certutil -addstore Root 'fixture'"}}, true)
	if !strings.Contains(inner, "$ErrorActionPreference = 'Stop'") ||
		strings.Count(inner, "$global:LASTEXITCODE = 0") != 2 ||
		strings.Count(inner, "if (-not $? -or $LASTEXITCODE -ne 0) { exit 1 }") != 2 {
		t.Fatalf("step failures are not propagated: %s", inner)
	}
	outer := elevatedPS(inner)
	for _, required := range []string{"-Wait -PassThru", "-WindowStyle Hidden", "-EncodedCommand", "exit $p.ExitCode"} {
		if !strings.Contains(outer, required) {
			t.Errorf("elevated script is missing %q: %s", required, outer)
		}
	}
	if strings.Contains(outer, inner) {
		t.Fatalf("elevated script passed the raw inner script on its command line: %s", outer)
	}
	encoded := encodePowerShell(inner)
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	units := make([]uint16, len(decoded)/2)
	for i := range units {
		units[i] = uint16(decoded[i*2]) | uint16(decoded[i*2+1])<<8
	}
	if got := string(utf16.Decode(units)); got != inner {
		t.Fatalf("encoded elevated script round trip = %q, want %q", got, inner)
	}
}

func TestPSQuoteEscapesPowerShellQuoteCharacters(t *testing.T) {
	input := "O'Brien O’Brien O’Brian ‘quoted’ ‚low‛"
	want := "'O''Brien O’’Brien O’’Brian ‘‘quoted’’ ‚‚low‛‛'"
	if got := PSQuote(input); got != want {
		t.Fatalf("PSQuote(%q) = %q, want %q", input, got, want)
	}
}

func TestRunPreservesCommandOutputOnFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX command fixture")
	}
	err := run(proc.Command("sh", "-c", "printf 'teardown failed' >&2; exit 1"))
	if err == nil || !strings.Contains(err.Error(), "teardown failed") {
		t.Fatalf("command output was not retained: %v", err)
	}
}

func TestEmptyStepsDoNotElevate(t *testing.T) {
	if err := Steps(nil); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsTeardownScriptKeepsAttemptingSteps(t *testing.T) {
	script := teardownScript([]Step{
		{PS: "certutil -delstore Root 'missing'"},
		{PS: "Remove-Item 'hosts-entry'"},
		{PS: "Remove-NetFirewallRule"},
	}, true)

	if strings.Contains(script, "exit 1") {
		t.Fatalf("a failing step must not stop the ones after it: %s", script)
	}
	if strings.Count(script, "try {") != 3 || strings.Count(script, "catch { }") != 3 {
		t.Fatalf("every step must be attempted independently: %s", script)
	}
	if strings.Contains(script, "$ErrorActionPreference = 'Stop'") {
		t.Fatalf("teardown must not abort the script on the first error: %s", script)
	}
}

func TestUnixTeardownAttemptsEveryStep(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fixture")
	}
	path := t.TempDir() + "/second-step"
	cmd := proc.Command("sh", "-c", teardownScript([]Step{
		{Sh: "false"},
		{Sh: "printf second > " + ShQuote(path)},
	}, false))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("teardown stopped after the first failure: %v, %q", err, out)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "second" {
		t.Fatalf("later teardown step did not run: %q, %v", data, err)
	}
}

func TestWindowsInstallStepsGenerateShortCircuitScript(t *testing.T) {
	script := stepScript([]Step{{PS: "a"}, {PS: "b"}}, true)
	if !strings.Contains(script, "if (-not $? -or $LASTEXITCODE -ne 0) { exit 1 }") {
		t.Fatalf("install must not run step 2 when step 1 failed: %s", script)
	}
}
