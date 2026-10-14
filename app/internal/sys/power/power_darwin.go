//go:build darwin

package power

import (
	"fmt"
	"os/exec"
	"strings"
)

func OnACPower() (bool, bool, error) {
	out, err := exec.Command("/usr/bin/pmset", "-g", "batt").Output()
	if err != nil {
		return false, false, fmt.Errorf("detect mains power: %w", err)
	}
	text := string(out)
	if strings.Contains(text, "AC Power") {
		return true, true, nil
	}
	if strings.Contains(text, "Battery Power") {
		return false, true, nil
	}
	return false, false, nil
}
