package updates_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
	"github.com/ohcnetwork/care_desktop/app/internal/updates"
)

func TestPruneImagesKeepsCurrentPreviousAndBuilder(t *testing.T) {
	h := newHarness(t)
	h.installWithBuilders()
	if err := h.c.SetPlugins(context.Background(), h.enable(custom)); err != nil {
		t.Fatal(err)
	}
	derivedA, _ := h.env()
	h.publish(spec{rev: 2, engines: []eng{{"0.1.0", 1}}, cares: []string{"A", "B"}, builders: true})
	h.check()
	h.mustApply()
	derivedB, _ := h.env()

	h.w.Lock()
	stale := Digest("stale")
	h.w.Images[stale] = true
	h.w.Unlock()
	res, err := h.c.PruneImages(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	h.w.Lock()
	defer h.w.Unlock()
	if h.w.Images[stale] || res.Removed == 0 {
		t.Fatalf("stale image kept; result %+v", res)
	}
	for name, d := range map[string]protocol.Digest{
		"derived B": derivedB, "derived A (previous)": derivedA,
		"backend B": Digest("backend-B"),
		"builder B": Digest("builder-B"),
	} {
		if !h.w.Images[d] {
			t.Errorf("%s (%s) removed", name, d)
		}
	}
	if h.w.Images[Digest("builder-A")] {
		t.Error("builder of the previous release kept")
	}
	for role, d := range h.w.Env {
		if !h.w.Images[d] {
			t.Errorf("%s (%s) removed", role, d)
		}
	}
}

func TestPruneImagesNeedsInstalledCare(t *testing.T) {
	h := newHarness(t)
	if _, err := h.c.PruneImages(context.Background()); !errors.Is(err, updates.ErrNotInstalled) {
		t.Fatalf("err = %v", err)
	}
}
