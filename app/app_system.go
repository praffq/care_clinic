package main

import (
	"context"

	"github.com/ohcnetwork/care_desktop/app/internal/service/api"
)

// EngineSlot is an installed engine version.
type EngineSlot struct {
	Version    string `json:"version"`
	Arch       string `json:"arch,omitempty"`
	DataSchema int    `json:"data_schema"`
	// Current is the active slot; Fallback, the one the service goes back
	// to when the current one fails to boot after an update.
	Current  bool `json:"current,omitempty"`
	Fallback bool `json:"fallback,omitempty"`
}

// SystemInfo is the Engine and system card: the versions of the desktop
// app, the host service and the engine, and the engine VM's state.
type SystemInfo struct {
	DesktopVersion string            `json:"desktop_version"`
	ServiceVersion string            `json:"service_version,omitempty"`
	Engine         *api.EngineStatus `json:"engine,omitempty"`
	Current        string            `json:"current,omitempty"`
	Fallback       string            `json:"fallback,omitempty"`
	Slots          []EngineSlot      `json:"slots"`
	// Error says why the service could not be read.
	Error string `json:"error,omitempty"`
}

// SystemInfo reads the service status and the engine slots.
func (a *App) SystemInfo() SystemInfo {
	out := SystemInfo{DesktopVersion: a.desktopVersion(), Slots: []EngineSlot{}}
	ctx, cancel := context.WithTimeout(a.clientContext(), serviceCallTimeout)
	defer cancel()
	st, err := a.svc().Status(ctx)
	if err != nil {
		out.Error = "Couldn't reach the CARE Clinic service: " + err.Error()
		return out
	}
	out.ServiceVersion, out.Engine = st.Version, &st.Engine
	out.Engine.Stack, out.Engine.ConsoleLog = nil, ""
	list, err := a.svc().Slots(ctx)
	if err != nil {
		out.Error = "Couldn't list the engine versions: " + err.Error()
		return out
	}
	out.Current, out.Fallback = list.Current, list.Previous
	for _, s := range list.Slots {
		out.Slots = append(out.Slots, EngineSlot{Version: s.Version, Arch: s.Arch, DataSchema: s.DataSchema,
			Current: s.Version == list.Current, Fallback: s.Version == list.Previous})
	}
	return out
}
