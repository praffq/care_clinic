package updates_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
	"github.com/ohcnetwork/care_desktop/app/internal/feed"
	"github.com/ohcnetwork/care_desktop/app/internal/stack"
	"github.com/ohcnetwork/care_desktop/app/internal/updates"
)

const imageSize = 100 << 20

func (h *harness) engineRunning(version string) {
	h.w.AddSlot(version, 1)
	h.w.Lock()
	h.w.Current, h.w.Booted = version, true
	h.w.Unlock()
}

func roleDigests(imgs []updates.Image) []string {
	var out []string
	for _, i := range imgs {
		out = append(out, string(i.Role)+"="+string(i.Digest))
	}
	return out
}

func TestLatestCompatiblePinsEveryImageOfTheNewestRelease(t *testing.T) {
	h := newHarness(t)
	h.engineRunning("0.1.0")
	h.publish(spec{rev: 3, engines: []eng{{"0.1.0", 1}}, cares: []string{"A", "B"}, builders: true})
	r, err := h.c.LatestCompatible(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.Version != careVersion("B") || r.Revision != 3 || !r.ReleasedAt.Equal(careReleased("B")) {
		t.Fatalf("release = %s rev %d at %s", r.Version, r.Revision, r.ReleasedAt)
	}
	want := []string{
		"backend=" + string(Digest("backend-B")), "frontend=" + string(Digest("frontend")),
		"caddy=" + string(Digest("caddy")), "backup=" + string(Digest("backup")),
		"postgres=" + string(Digest("postgres")), "redis=" + string(Digest("redis")), "silo=" + string(Digest("silo")),
		"backend-builder=" + string(Digest("builder-B")), "frontend-builder=" + string(Digest("fe-builder-B")),
	}
	if got := roleDigests(r.Pull()); !slices.Equal(got, want) {
		t.Fatalf("pull = %v\nwant %v", got, want)
	}
	if got := roleDigests(r.Images); !slices.Equal(got, want[:7]) {
		t.Fatalf("stack images = %v", got)
	}
	if st := h.status(); st.Revision != 3 || st.CheckedAt.IsZero() {
		t.Fatalf("verified feed not recorded: %+v", st)
	}
}

func TestLatestCompatibleSkipsBlockedReleases(t *testing.T) {
	h := newHarness(t)
	h.engineRunning("0.1.0")
	h.publish(spec{rev: 1, engines: []eng{{"0.1.0", 1}}, cares: []string{"A", "B"}, blocked: []string{"care:" + careVersion("B")}})
	r, err := h.c.LatestCompatible(context.Background())
	if err != nil || r.Version != careVersion("A") {
		t.Fatalf("release = %v, %v; want A", r, err)
	}
}

func TestLatestCompatibleNamesTheVersionNeeded(t *testing.T) {
	cases := []struct {
		name, engine string
		s            spec
		want         string
	}{
		{"app too old", "0.1.0", spec{rev: 1, engines: []eng{{"0.1.0", 1}}, cares: []string{"A"}, careNeeds: ">=2.0.0"},
			"CARE 3.1.0 needs app ≥ 2.0.0 (this app is 1.0.0); update the app first."},
		{"engine too old", "0.0.0", spec{rev: 1, engines: []eng{{"0.0.0", 1}}, cares: []string{"A"}},
			"CARE 3.1.0 needs engine ≥ 0.0.1 (this computer runs 0.0.0); update the app first."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.engineRunning(tc.engine)
			h.publish(tc.s)
			_, err := h.c.LatestCompatible(context.Background())
			var nc *feed.NoCompatibleError
			if !errors.As(err, &nc) || nc.Reason != tc.want {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestLatestCompatibleReportsAnUnreachableFeed(t *testing.T) {
	h := newHarness(t)
	h.engineRunning("0.1.0")
	if _, err := h.c.LatestCompatible(context.Background()); !errors.Is(err, updates.ErrFeedUnreachable) {
		t.Fatalf("error = %v, want ErrFeedUnreachable", err)
	}
	h.setFeed([]byte(`{"schema":1}`), nil)
	if _, err := h.c.LatestCompatible(context.Background()); err == nil || errors.Is(err, updates.ErrFeedUnreachable) {
		t.Fatalf("an invalid feed was reported as unreachable: %v", err)
	}
}

func TestPrepareInstallChecksDiskSpaceForMissingImages(t *testing.T) {
	h := newHarness(t)
	h.engineRunning("0.1.0")
	h.publish(spec{rev: 1, engines: []eng{{"0.1.0", 1}}, cares: []string{"A"}, builders: true})
	r, err := h.c.LatestCompatible(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	h.w.Lock()
	h.w.DiskFree = 3 << 30
	h.w.Unlock()
	_, err = h.c.PrepareInstall(context.Background(), r, nil)
	var de *updates.DiskSpaceError
	if !errors.As(err, &de) || de.Free != 3<<30 || de.Need != 2<<30+2*9*imageSize {
		t.Fatalf("error = %v (%+v)", err, de)
	}
	if len(h.requests) != 0 {
		t.Fatalf("downloaded before checking the disk: %v", h.requests)
	}
	have := map[protocol.Digest]bool{}
	for _, img := range r.Pull()[:8] {
		have[img.Digest] = true
	}
	pull, err := h.c.PrepareInstall(context.Background(), r, have)
	if err != nil {
		t.Fatalf("with 8 of 9 images present: %v", err)
	}
	if len(pull) != 9 || pull[8].Tarball == "" || pull[0].Tarball != "" {
		t.Fatalf("pull = %+v", pull)
	}
	if got := strings.Join(h.requests, ","); got != "/images/fe-builder-A.tar " {
		t.Fatalf("downloads = %q, want only the missing builder", got)
	}
}

func TestPrepareInstallNamesAFailedImageDownload(t *testing.T) {
	h := newHarness(t)
	h.engineRunning("0.1.0")
	h.publish(spec{rev: 1, engines: []eng{{"0.1.0", 1}}, cares: []string{"A"}})
	r, err := h.c.LatestCompatible(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	delete(h.files, "/images/frontend.tar")
	h.mu.Unlock()
	_, err = h.c.PrepareInstall(context.Background(), r, nil)
	var ie *stack.ImageError
	if !errors.As(err, &ie) || ie.Image.Role != protocol.ImageRoleFrontend || !ie.Missing() {
		t.Fatalf("error = %v", err)
	}
}

func TestRecordInstallWritesTheInstalledState(t *testing.T) {
	h := newHarness(t)
	h.engineRunning("0.1.0")
	h.publish(spec{rev: 2, engines: []eng{{"0.1.0", 1}}, cares: []string{"A"}, builders: true})
	r, err := h.c.LatestCompatible(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pull, err := h.c.PrepareInstall(context.Background(), r, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.w.EnsureImages(context.Background(), updates.StackImages(pull)); err != nil {
		t.Fatal(err)
	}
	if err := h.w.WriteEnv(context.Background(), updates.StackImages(r.Images), ""); err != nil {
		t.Fatal(err)
	}
	if err := h.c.RecordInstall(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	st := h.status()
	if st.Installed.Care == nil || st.Installed.Care.Version != careVersion("A") || !st.Installed.Care.ReleasedAt.Equal(careReleased("A")) {
		t.Fatalf("installed care = %+v", st.Installed.Care)
	}
	if got := roleDigests(st.Installed.Care.Images); !slices.Equal(got, roleDigests(r.Images)) {
		t.Fatalf("installed images = %v", got)
	}
	for _, img := range st.Installed.Care.Images {
		if img.Tarball != "" {
			t.Fatalf("a local tarball path was recorded: %+v", img)
		}
	}
	if st.Installed.Engine == nil || st.Installed.Engine.Version != "0.1.0" {
		t.Fatalf("installed engine = %+v", st.Installed.Engine)
	}
	last := h.lastHistory()
	if last.Component != feed.ComponentCare || last.To != careVersion("A") || last.Revision != 2 || last.Result != updates.ResultApplied {
		t.Fatalf("history = %+v", last)
	}
	if res := h.check(); res.Available || res.Reason != updates.ErrUpToDate.Error() {
		t.Fatalf("check after the install = %+v", res)
	}
}
