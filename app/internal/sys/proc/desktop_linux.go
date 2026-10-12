//go:build linux

package proc

import (
	"os"
	"path/filepath"
	"strings"
)

func DesktopDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" || !filepath.IsAbs(configHome) {
		configHome = filepath.Join(home, ".config")
	}
	data, err := os.ReadFile(filepath.Join(configHome, "user-dirs.dirs"))
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if err == nil {
		if desktop, ok := xdgDesktopDir(string(data), home); ok {
			return desktop, nil
		}
	}
	return filepath.Join(home, "Desktop"), nil
}

func xdgDesktopDir(config, home string) (string, bool) {
	for _, line := range strings.Split(config, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || key != "XDG_DESKTOP_DIR" {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
			return "", false
		}
		value = strings.NewReplacer(`\\`, `\`, `\"`, `"`).Replace(value[1 : len(value)-1])
		if strings.HasPrefix(value, "${HOME}") {
			suffix := strings.TrimPrefix(value, "${HOME}")
			if suffix != "" && !strings.HasPrefix(suffix, "/") {
				return "", false
			}
			value = home + suffix
		} else if strings.HasPrefix(value, "$HOME") {
			suffix := strings.TrimPrefix(value, "$HOME")
			if suffix != "" && !strings.HasPrefix(suffix, "/") {
				return "", false
			}
			value = home + suffix
		}
		if strings.ContainsAny(value, "$`") || !filepath.IsAbs(value) {
			return "", false
		}
		return filepath.Clean(value), true
	}
	return "", false
}
