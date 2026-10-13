package autostart

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ohcnetwork/care_desktop/app/internal/sys/proc"
)

const (
	label          = "ohc.care-clinic"
	appName        = "CARE Clinic"
	macBundleID    = "ohc.care-clinic"
	runKey         = `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`
	autostartArg   = "--autostart"
	macOpenCommand = "/usr/bin/open"
)

func macPlistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist")
}

func linuxDesktopPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "autostart", "care-clinic.desktop")
}

// Enabled reports whether the entry launches this exact executable now.
func Enabled() bool {
	switch runtime.GOOS {
	case "darwin":
		data, err := os.ReadFile(macPlistPath())
		if err != nil {
			return false
		}
		return plistLabel(data) == label && sameArgs(plistProgramArguments(data), macProgramArguments())
	case "linux":
		exe, err := os.Executable()
		if err != nil {
			return false
		}
		data, err := os.ReadFile(linuxDesktopPath())
		if err != nil {
			return false
		}
		return linuxEnabled(data, exe)
	case "windows":
		exe, err := os.Executable()
		if err != nil {
			return false
		}
		out, err := proc.Command("reg", "query", runKey, "/v", appName).CombinedOutput()
		return err == nil && windowsEnabled(string(out), exe)
	}
	return false
}

// Present reports whether an entry owned by CARE Clinic exists, even if it
// points at another or a moved executable. Unrelated entries are not owned.
func Present() (bool, error) {
	switch runtime.GOOS {
	case "darwin":
		data, err := os.ReadFile(macPlistPath())
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return macOwned(data), nil
	case "linux":
		data, err := os.ReadFile(linuxDesktopPath())
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return linuxOwned(data), nil
	case "windows":
		out, err := proc.Command("reg", "query", runKey, "/v", appName).CombinedOutput()
		if err != nil {
			var exit *exec.ExitError
			if errors.As(err, &exit) && exit.ExitCode() == 1 {
				return false, nil
			}
			return false, fmt.Errorf("query autostart: %w", err)
		}
		return windowsOwned(string(out)), nil
	}
	return false, nil
}

// Remove deletes only an entry owned by CARE Clinic and leaves unrelated
// entries untouched. A missing entry is success.
func Remove() error {
	switch runtime.GOOS {
	case "darwin":
		return removeOwnedFile(macPlistPath(), macOwned)
	case "linux":
		return removeOwnedFile(linuxDesktopPath(), linuxOwned)
	case "windows":
		present, err := Present()
		if err != nil || !present {
			return err
		}
		return proc.Command("reg", "delete", runKey, "/v", appName, "/f").Run()
	}
	return nil
}

func Set(on bool) error {
	switch runtime.GOOS {
	case "darwin":
		if !on {
			return Remove()
		}
		plist := macPlist()
		if err := os.MkdirAll(filepath.Dir(macPlistPath()), 0o755); err != nil {
			return err
		}
		return os.WriteFile(macPlistPath(), []byte(plist), 0o644)
	case "linux":
		if !on {
			return Remove()
		}
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(linuxDesktopPath()), 0o755); err != nil {
			return err
		}
		return os.WriteFile(linuxDesktopPath(), []byte(linuxDesktop(exe)), 0o644)
	case "windows":
		if !on {
			return Remove()
		}
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		return proc.Command("reg", "add", runKey, "/v", appName, "/t", "REG_SZ",
			"/d", windowsCommand(exe), "/f").Run()
	}
	return nil
}

func removeOwnedFile(path string, owned func([]byte) bool) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !owned(data) {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// macOwned accepts the current Launch Services entry and the earlier direct
// executable form, which both carry our label and the autostart argument.
func macOwned(data []byte) bool {
	if plistLabel(data) != label {
		return false
	}
	args := plistProgramArguments(data)
	return len(args) >= 2 && args[len(args)-1] == autostartArg
}

func plistLabel(data []byte) string {
	decoder := xml.NewDecoder(strings.NewReader(string(data)))
	for {
		token, err := decoder.Token()
		if err != nil {
			return ""
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "key" {
			continue
		}
		var key string
		if decoder.DecodeElement(&key, &start) != nil || key != "Label" {
			continue
		}
		for {
			token, err = decoder.Token()
			if err != nil {
				return ""
			}
			if value, ok := token.(xml.StartElement); ok {
				var v string
				if value.Name.Local != "string" || decoder.DecodeElement(&v, &value) != nil {
					return ""
				}
				return v
			}
		}
	}
}

func macProgramArguments() []string {
	return []string{macOpenCommand, "-b", macBundleID, "--args", autostartArg}
}

func macPlist() string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>` + label + `</string>
  <key>ProgramArguments</key><array>`)
	for _, arg := range macProgramArguments() {
		b.WriteString("<string>")
		_ = xml.EscapeText(&b, []byte(arg))
		b.WriteString("</string>")
	}
	b.WriteString(`</array>
  <key>RunAtLoad</key><true/>
</dict></plist>
`)
	return b.String()
}

func plistProgramArguments(data []byte) []string {
	decoder := xml.NewDecoder(strings.NewReader(string(data)))
	for {
		token, err := decoder.Token()
		if err != nil {
			return nil
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "key" {
			continue
		}
		var key string
		if decoder.DecodeElement(&key, &start) != nil || key != "ProgramArguments" {
			continue
		}
		for {
			token, err = decoder.Token()
			if err != nil {
				return nil
			}
			start, ok = token.(xml.StartElement)
			if ok && start.Name.Local == "array" {
				break
			}
		}
		var args []string
		for {
			token, err = decoder.Token()
			if err != nil {
				return nil
			}
			switch value := token.(type) {
			case xml.StartElement:
				if value.Name.Local != "string" {
					return nil
				}
				var arg string
				if decoder.DecodeElement(&arg, &value) != nil {
					return nil
				}
				args = append(args, arg)
			case xml.EndElement:
				if value.Name.Local == "array" {
					return args
				}
			}
		}
	}
}

func linuxDesktop(exe string) string {
	return "[Desktop Entry]\nType=Application\nName=" + appName +
		"\nExec=" + desktopEscapeString(desktopCommand(exe)) + "\nX-GNOME-Autostart-enabled=true\n"
}

// desktopCommand is the Exec value after the Exec-argument quoting layer; the
// key-value string layer is applied separately when the file is written.
func desktopCommand(exe string) string {
	return desktopExecArg(exe) + " " + autostartArg
}

func desktopExecArg(arg string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range arg {
		switch r {
		case '\\', '"', '`', '$':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '%':
			b.WriteString("%%")
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func desktopEscapeString(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func desktopUnescapeString(s string) string {
	var b strings.Builder
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		if rs[i] != '\\' || i+1 == len(rs) {
			b.WriteRune(rs[i])
			continue
		}
		i++
		switch rs[i] {
		case 's':
			b.WriteByte(' ')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case '\\':
			b.WriteByte('\\')
		default:
			b.WriteByte('\\')
			b.WriteRune(rs[i])
		}
	}
	return b.String()
}

// desktopExec returns the Exec value with the string layer removed.
func desktopExec(data []byte) string {
	inEntry := false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.HasPrefix(line, "[") {
			inEntry = line == "[Desktop Entry]"
			continue
		}
		if value, ok := strings.CutPrefix(line, "Exec="); ok && inEntry {
			return desktopUnescapeString(value)
		}
	}
	return ""
}

func desktopName(data []byte) string {
	inEntry := false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.HasPrefix(line, "[") {
			inEntry = line == "[Desktop Entry]"
			continue
		}
		if value, ok := strings.CutPrefix(line, "Name="); ok && inEntry {
			return value
		}
	}
	return ""
}

// desktopArgv splits an Exec value (after string unescaping) into arguments,
// applying the reserved-character escapes allowed inside double quotes.
func desktopArgv(command string) ([]string, bool) {
	var args []string
	var cur strings.Builder
	started, quoted := false, false
	rs := []rune(command)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case quoted && r == '"':
			quoted = false
		case quoted && r == '\\':
			if i+1 == len(rs) {
				return nil, false
			}
			i++
			if !strings.ContainsRune("\\\"`$", rs[i]) {
				return nil, false
			}
			cur.WriteRune(rs[i])
		case quoted && (r == '`' || r == '$'):
			return nil, false
		case quoted && r == '%':
			if i+1 < len(rs) && rs[i+1] == '%' {
				i++
			}
			cur.WriteByte('%')
		case quoted:
			cur.WriteRune(r)
		case r == '"':
			quoted, started = true, true
		case r == ' ':
			if started {
				args = append(args, cur.String())
				cur.Reset()
				started = false
			}
		case r == '\\' || r == '`' || r == '$' || r == '\'':
			return nil, false
		case r == '%':
			if i+1 < len(rs) && rs[i+1] == '%' {
				i++
			}
			cur.WriteByte('%')
			started = true
		default:
			cur.WriteRune(r)
			started = true
		}
	}
	if quoted {
		return nil, false
	}
	if started {
		args = append(args, cur.String())
	}
	return args, true
}

func linuxEnabled(data []byte, exe string) bool {
	args, ok := desktopArgv(desktopExec(data))
	return ok && desktopName(data) == appName && sameArgs(args, []string{exe, autostartArg})
}

func linuxOwned(data []byte) bool {
	args, ok := desktopArgv(desktopExec(data))
	return ok && desktopName(data) == appName && len(args) >= 2 && args[len(args)-1] == autostartArg
}

func windowsCommand(exe string) string {
	return `"` + exe + `" ` + autostartArg
}

func windowsValue(output string) (string, bool) {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.TrimSpace(line)
		name, rest, ok := strings.Cut(fields, "    ")
		if !ok || name != appName {
			continue
		}
		kind, data, ok := strings.Cut(strings.TrimLeft(rest, " "), "    ")
		if ok && kind == "REG_SZ" {
			return strings.TrimSpace(data), true
		}
	}
	return "", false
}

func windowsEnabled(output, exe string) bool {
	value, ok := windowsValue(output)
	return ok && value == windowsCommand(exe)
}

func windowsOwned(output string) bool {
	value, ok := windowsValue(output)
	return ok && strings.HasSuffix(value, " "+autostartArg)
}

func sameArgs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
