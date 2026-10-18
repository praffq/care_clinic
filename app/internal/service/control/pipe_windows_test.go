package control

import (
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestCheckPipeOwner(t *testing.T) {
	path := `\\.\pipe\care-clinic-owner-test`
	ln, err := Listen(ListenConfig{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	c, err := dialSocket(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if err := checkPipeOwner(c, func(*windows.SID) bool { return true }); err != nil {
		t.Fatalf("owner check: %v", err)
	}
	if err := checkPipeOwner(c, func(*windows.SID) bool { return false }); err == nil || !strings.Contains(err.Error(), "not the CARE Clinic service") {
		t.Fatalf("untrusted owner accepted: %v", err)
	}
}

func TestLocalGroupMembers(t *testing.T) {
	group, members, err := groupSIDs("Administrators")
	if err != nil {
		t.Fatal(err)
	}
	if group != "S-1-5-32-544" || len(members) == 0 {
		t.Fatalf("Administrators = %s %v", group, members)
	}
}
