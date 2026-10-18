package main

import (
	"fmt"
	"os/exec"
	"strings"
)

func netsh(args ...string) error {
	out, err := exec.Command("netsh", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("netsh %s: %w: %s", strings.Join(args[:4], " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// openFirewall replaces the service's firewall rules.
func openFirewall(program string) error {
	closeFirewall()
	for _, args := range firewallAddArgs(program) {
		if err := netsh(args...); err != nil {
			return err
		}
	}
	return nil
}

// closeFirewall deletes the rules; netsh fails when none exist, which is fine.
func closeFirewall() { _ = netsh(firewallDeleteArgs()...) }
