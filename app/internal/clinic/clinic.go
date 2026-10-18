// Package clinic drives one CARE installation from the desktop app. The CARE
// stack runs in the engine VM owned by care-clinic-service; everything here
// goes through the service's control socket (see internal/stack). Nothing
// runs docker on the host.
package clinic

import (
	"context"

	"github.com/ohcnetwork/care_desktop/app/internal/progress"
	"github.com/ohcnetwork/care_desktop/app/internal/stack"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/proc"
)

type Clinic struct {
	// InstallDir holds this computer's copy of the deployment kit; Configure
	// renders it and uploads it to the engine.
	InstallDir string

	MDNSName      string
	AdminPassword string

	Log     func(string)
	Confirm func(title, message string) bool

	// Stack drives the CARE stack in the engine VM through the service.
	Stack       *stack.Runtime
	StackImages []stack.Image
	// StackPlugs is the ADDITIONAL_PLUGS StackImages are installed with
	// (updates.Coordinator.InstallPlugs); "" means the catalog defaults.
	StackPlugs string
	// Progress receives the setup steps (Step* constants).
	Progress progress.Func

	configured        bool
	localSetupOffered bool
	ctx               context.Context
}

func (e *Clinic) logln(s string) {
	if e.Log != nil {
		e.Log(s)
	}
}

// Runner runs host tools (tailscale, firewall, trust store) for this clinic.
func (e *Clinic) Runner() proc.Runner {
	return proc.Runner{Log: e.Log, Ctx: e.ctx}
}

func (e *Clinic) mdnsName() string {
	if e.MDNSName != "" {
		return e.MDNSName
	}
	return "care"
}

func (e *Clinic) Label() string { return e.mdnsName() }
