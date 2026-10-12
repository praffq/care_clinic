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
	return "'" + strings.NewReplacer(
		"'", "''",
		"\u2018", "\u2018\u2018",
		"\u2019", "\u2019\u2019",
		"\u201a", "\u201a\u201a",
		"\u201b", "\u201b\u201b",
	).Replace(s) + "'"
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

func elevatedPS(inner string) string {
	return "$ErrorActionPreference = 'Stop'; $p = Start-Process powershell -Verb RunAs -WindowStyle Hidden -Wait -PassThru " +
		"-ArgumentList '-NoProfile','-WindowStyle','Hidden','-EncodedCommand','" +
		encodePowerShell(inner) + "'; exit $p.ExitCode"
}

func encodePowerShell(script string) string {
	encoded := utf16.Encode([]rune(script))
	bytes := make([]byte, len(encoded)*2)
	for i, unit := range encoded {
		bytes[i*2] = byte(unit)
		bytes[i*2+1] = byte(unit >> 8)
	}
	return base64.StdEncoding.EncodeToString(bytes)
}

func stepScript(steps []Step, windows bool) string {
	pick := func(s Step) string { return s.Sh }
	if windows {
		pick = func(s Step) string { return s.PS }
	}
	parts := make([]string, 0, len(steps))
	for _, s := range steps {
		if windows {
			parts = append(parts, "$global:LASTEXITCODE = 0; & { "+pick(s)+
				" }; if (-not $? -or $LASTEXITCODE -ne 0) { exit 1 }")
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
		return run(proc.Command("powershell", "-NoProfile", "-Command",
			elevatedPS(teardownScript(steps, true))))
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
