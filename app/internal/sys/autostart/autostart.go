package autostart

import (
	"encoding/xml"
	"os"
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

func Enabled() bool {
	switch runtime.GOOS {
	case "darwin":
		data, err := os.ReadFile(macPlistPath())
		if err != nil {
			return false
		}
		return sameArgs(plistProgramArguments(data), macProgramArguments())
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

func Set(on bool) error {
	switch runtime.GOOS {
	case "darwin":
		if !on {
			return removeIfExists(macPlistPath())
		}
		plist := macPlist()
		if err := os.MkdirAll(filepath.Dir(macPlistPath()), 0o755); err != nil {
			return err
		}
		return os.WriteFile(macPlistPath(), []byte(plist), 0o644)
	case "linux":
		if !on {
			return removeIfExists(linuxDesktopPath())
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
			return proc.Command("reg", "delete", runKey, "/v", appName, "/f").Run()
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
		"\nExec=" + desktopCommand(exe) + "\nX-GNOME-Autostart-enabled=true\n"
}

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

func desktopExec(data []byte) string {
	for _, line := range strings.Split(string(data), "\n") {
		if value, ok := strings.CutPrefix(line, "Exec="); ok {
			return value
		}
	}
	return ""
}

func linuxEnabled(data []byte, exe string) bool {
	return desktopExec(data) == desktopCommand(exe)
}

func windowsCommand(exe string) string {
	return `"` + exe + `" ` + autostartArg
}

func windowsEnabled(output, exe string) bool {
	return strings.HasSuffix(strings.TrimSpace(output), windowsCommand(exe))
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

func removeIfExists(p string) error {
	if _, err := os.Stat(p); err != nil {
		return nil
	}
	return os.Remove(p)
}
