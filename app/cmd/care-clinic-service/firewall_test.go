package main

import (
	"strings"
	"testing"
)

func TestFirewallArgs(t *testing.T) {
	got := firewallAddArgs(`C:\ProgramData\CARE Clinic\bin\care-clinic-service.exe`)
	if len(got) != 2 {
		t.Fatalf("got %d rules", len(got))
	}
	want := []string{
		`advfirewall firewall add rule name=CARE Clinic dir=in action=allow profile=any protocol=TCP localport=80,443 program=C:\ProgramData\CARE Clinic\bin\care-clinic-service.exe enable=yes`,
		`advfirewall firewall add rule name=CARE Clinic dir=in action=allow profile=any protocol=UDP localport=5353 program=C:\ProgramData\CARE Clinic\bin\care-clinic-service.exe enable=yes`,
	}
	for i, args := range got {
		if s := strings.Join(args, " "); s != want[i] {
			t.Errorf("rule %d = %s", i, s)
		}
	}
	if s := strings.Join(firewallDeleteArgs(), " "); s != "advfirewall firewall delete rule name=CARE Clinic" {
		t.Errorf("delete = %s", s)
	}
}
