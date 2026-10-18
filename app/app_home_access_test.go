package main

import (
	"errors"
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/clinic"
)

func TestFailedEnableAsksForNoRestart(t *testing.T) {
	e := &clinic.Clinic{InstallDir: t.TempDir(), MDNSName: "north"}
	restart, err := enableHomeAccess(e, func() error { return errors.New("tailscale serve failed") })
	if err == nil || restart {
		t.Fatalf("serve failure = %v, restart %v", err, restart)
	}
}
