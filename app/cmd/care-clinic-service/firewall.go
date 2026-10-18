package main

// firewallRule names every Windows Firewall rule the service install adds,
// so uninstall can delete them all by name.
const firewallRule = "CARE Clinic"

// firewallAddArgs returns the netsh invocations that let LAN devices reach
// the service's HTTP(S) listeners and mDNS responder. The rules apply to
// every profile because a clinic network is often classified as Public, and
// are scoped to the service binary.
func firewallAddArgs(program string) [][]string {
	rule := func(proto, ports string) []string {
		return []string{
			"advfirewall", "firewall", "add", "rule",
			"name=" + firewallRule, "dir=in", "action=allow", "profile=any",
			"protocol=" + proto, "localport=" + ports, "program=" + program,
			"enable=yes",
		}
	}
	return [][]string{rule("TCP", "80,443"), rule("UDP", "5353")}
}

func firewallDeleteArgs() []string {
	return []string{"advfirewall", "firewall", "delete", "rule", "name=" + firewallRule}
}
