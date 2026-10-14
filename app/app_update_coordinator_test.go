package main

import (
	"context"
	"strings"
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/release"
	"github.com/ohcnetwork/care_desktop/app/internal/updates"
)

func TestCombinedUpdateHooksProvideDesktopInstaller(t *testing.T) {
	for _, request := range []updates.UpdateType{updates.Engine, updates.CAREImages} {
		t.Run(string(request), func(t *testing.T) {
			a := &App{pins: &release.Pins{AppVersion: "1.0.0-dev"}}
			hooks := a.updateHooks(release.Manifest{}, nil)
			plan := &updates.Journal{Steps: []updates.Step{
				{Name: string(updates.Desktop)},
				{Name: string(request)},
			}}
			_, err := hooks.Apply(context.Background(), string(updates.Desktop), plan, func() error { return nil })
			if err == nil || !strings.Contains(err.Error(), "development builds cannot replace themselves") {
				t.Fatalf("combined plan did not reach the staged desktop installer: %v", err)
			}
		})
	}
}
