package elevate

import (
	"encoding/base64"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"unicode/utf16"

	"github.com/ohcnetwork/care_desktop/app/internal/sys/proc"
)

type Step struct {
	What string
	Sh   string
	PS   string
}

func PSQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func ShQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func OSAQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func Run(sh string, elevated bool) error {
	switch {
	case !elevated:
		return run(proc.Command("sh", "-c", sh))
	case runtime.GOOS == "darwin":
		return run(proc.Command("osascript", "-e",
			"do shell script "+OSAQuote(sh)+" with administrator privileges"))
	default:
		return run(proc.Command("pkexec", "sh", "-c", sh))
	}
}

func Steps(steps []Step) error {
	if len(steps) == 0 {
		return nil
	}
	if runtime.GOOS == "windows" {
		return run(proc.Command("powershell", "-NoProfile", "-Command",
			elevatedPS(stepScript(steps, true))))
	}
	return Run(stepScript(steps, false), true)
}

func run(c *exec.Cmd) error {
	out, err := c.CombinedOutput()
	if msg := strings.TrimSpace(string(out)); err != nil && msg != "" {
		return fmt.Errorf("%w: %s", err, msg)
	}
	return err
}

// RunPS runs a PowerShell script elevated (one UAC prompt) and waits for
// it. The script travels base64-encoded, so it needs no quoting; its exit
// code comes back as an *exec.ExitError. Its output is not captured: the
// elevated process has its own (hidden) console.
func RunPS(script string) error {
	return run(proc.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", elevatedEncodedPS(script)))
}

// elevatedEncodedPS is the unelevated command that starts script elevated.
func elevatedEncodedPS(script string) string {
	return "$ErrorActionPreference = 'Stop'; $p = Start-Process powershell -Verb RunAs -WindowStyle Hidden -Wait -PassThru " +
		"-ArgumentList '-NoProfile','-NonInteractive','-ExecutionPolicy','Bypass','-WindowStyle','Hidden','-EncodedCommand','" + EncodePS(script) + "'; exit $p.ExitCode"
}

// EncodePS encodes script for powershell -EncodedCommand (UTF-16LE, base64).
func EncodePS(script string) string {
	u := utf16.Encode([]rune(script))
	b := make([]byte, 2*len(u))
	for i, c := range u {
		b[2*i], b[2*i+1] = byte(c), byte(c>>8)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func elevatedPS(inner string) string {
	return "$ErrorActionPreference = 'Stop'; $p = Start-Process powershell -Verb RunAs -WindowStyle Hidden -Wait -PassThru " +
		"-ArgumentList '-NoProfile','-WindowStyle','Hidden','-Command'," + PSQuote(inner) + "; exit $p.ExitCode"
}

func stepScript(steps []Step, windows bool) string {
	pick := func(s Step) string { return s.Sh }
	if windows {
		pick = func(s Step) string { return s.PS }
	}
	parts := make([]string, 0, len(steps))
	for _, s := range steps {
		if windows {
			parts = append(parts, "& { "+pick(s)+" }; if (-not $?) { exit 1 }")
		} else {
			parts = append(parts, "( "+pick(s)+" )")
		}
	}
	if windows {
		return "$ErrorActionPreference = 'Stop'; " + strings.Join(parts, "; ")
	}
	return strings.Join(parts, " && ")
}

func Teardown(steps []Step) error {
	if len(steps) == 0 {
		return nil
	}
	if runtime.GOOS == "windows" {
		return proc.Command("powershell", "-NoProfile", "-Command",
			elevatedPS(teardownScript(steps, true))).Run()
	}
	return Run(teardownScript(steps, false), true)
}

func teardownScript(steps []Step, windows bool) string {
	pick := func(s Step) string { return s.Sh }
	if windows {
		pick = func(s Step) string { return s.PS }
	}
	parts := make([]string, 0, len(steps))
	for _, s := range steps {
		if windows {
			parts = append(parts, "& { try { "+pick(s)+" } catch { } }")
		} else {
			parts = append(parts, "( "+pick(s)+" ) || true")
		}
	}
	if windows {
		return "$ErrorActionPreference = 'Continue'; " + strings.Join(parts, "; ")
	}
	return strings.Join(parts, "; ")
}
