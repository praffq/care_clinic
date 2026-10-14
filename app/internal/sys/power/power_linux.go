//go:build linux

package power

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func OnACPower() (bool, bool, error) {
	const root = "/sys/class/power_supply"
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	detected := false
	for _, entry := range entries {
		base := filepath.Join(root, entry.Name())
		kind, err := os.ReadFile(filepath.Join(base, "type"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, false, err
		}
		if strings.TrimSpace(string(kind)) != "Mains" {
			continue
		}
		detected = true
		online, err := os.ReadFile(filepath.Join(base, "online"))
		if err != nil {
			return false, true, err
		}
		if strings.TrimSpace(string(online)) == "1" {
			return true, true, nil
		}
	}
	return false, detected, nil
}
