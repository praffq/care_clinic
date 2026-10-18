package updates_test

import (
	"strings"
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/feed"
	"github.com/ohcnetwork/care_desktop/app/internal/updates"
)

func TestReleaseNotes(t *testing.T) {
	h := newHarness(t)
	if n, err := h.c.ReleaseNotes([]updates.PlannedStep{{Component: feed.ComponentCare, To: "1.0.0"}}); err != nil || n != nil {
		t.Fatalf("before a check = %+v, %v", n, err)
	}
	h.publish(spec{rev: 1, engines: []eng{{"0.1.0", 3}}, cares: []string{"A"}})
	r := h.check()
	steps := append(r.Steps, updates.PlannedStep{Component: feed.ComponentDesktop, To: "1.0.0"}, updates.PlannedStep{Component: feed.ComponentEngine, To: "9.9.9"}, updates.PlannedStep{Component: updates.ComponentPlugins, To: "x"})
	notes, err := h.c.ReleaseNotes(steps)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 3 {
		t.Fatalf("notes = %+v", notes)
	}
	if n := notes[0]; n.Component != feed.ComponentEngine || n.Version != "0.1.0" || n.DataSchema != 3 || len(n.Links) != 1 || !strings.HasSuffix(n.Links[0].URL, "/engine-v0.1.0") {
		t.Fatalf("engine = %+v", n)
	}
	if n := notes[1]; n.Component != feed.ComponentCare || n.Version != careVersion("A") || !n.Migrations || n.ReleasedAt.IsZero() {
		t.Fatalf("care = %+v", n)
	}
	if n := notes[2]; n.Component != feed.ComponentDesktop || len(n.Links) != 1 || n.Links[0].URL != updates.ReleasesPage+"desktop-v1.0.0" {
		t.Fatalf("desktop = %+v", n)
	}
}
