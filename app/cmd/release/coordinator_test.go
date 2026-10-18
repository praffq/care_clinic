package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
	"github.com/ohcnetwork/care_desktop/app/internal/feed"
	"github.com/ohcnetwork/care_desktop/app/internal/updates"
	"github.com/ohcnetwork/care_desktop/app/internal/updates/updatestest"
)

// demoImages are the image tarballs the demo feed pins.
var demoImages = []string{
	"care-backend-A", "care-backend-B", "care-backend-C-broken", "care-frontend-A",
	"care-caddy", "care-backup", "postgres", "redis", "silo",
}

func fakeDigest(name string) protocol.Digest {
	s := sha256.Sum256([]byte("demo image " + name))
	return protocol.Digest("sha256:" + hex.EncodeToString(s[:]))
}

// TestDemoWithCoordinator walks the update coordinator through demo feed
// revisions 1-7 (served over HTTP, with fake engine bundles and image
// tarballs) against a fake engine, checking the outcomes in demo/README.md.
func TestDemoWithCoordinator(t *testing.T) {
	dist := t.TempDir()
	for _, e := range []struct {
		version string
		schema  int
	}{{"0.1.0", 1}, {"0.1.1", 1}, {"0.2.0", 2}} {
		for _, arch := range []string{feed.ArchARM64, feed.ArchAMD64} {
			name := fmt.Sprintf("care-engine-%s-%s.tar.gz", e.version, arch)
			if err := os.WriteFile(filepath.Join(dist, name), updatestest.EngineBundle(e.version, e.schema), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	images := filepath.Join(dist, "images")
	if err := os.MkdirAll(images, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range demoImages {
		p := filepath.Join(images, name+".tar")
		if err := os.WriteFile(p, []byte("image "+name), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p+".digest", []byte(fakeDigest(name)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out := t.TempDir()
	srv := httptest.NewServer(http.FileServer(http.Dir(out)))
	defer srv.Close()
	if _, err := runDemo(demoOptions{Key: devKey(t), Out: out, EngineDist: dist, ImagesDist: images, BaseURL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	pub, err := loadPublicKey(devPubPath)
	if err != nil {
		t.Fatal(err)
	}

	w := updatestest.New()
	w.BadBackends[fakeDigest("care-backend-C-broken")] = true
	state := t.TempDir()
	ctx := context.Background()
	// Each revision is a new process pointed at that revision's feed; the
	// state directory carries over.
	open := func(rev int) *updates.Coordinator {
		t.Helper()
		c, err := updates.Open(updates.Options{
			Dir: state, Feed: updates.HTTPFetcher{URL: fmt.Sprintf("%s/rev-%d/updates.json", srv.URL, rev)},
			Keys: feed.NewTrustedKeys(pub), AllowInsecureURLs: true,
			Service: w.Service(), Agent: w, Stack: w,
			DesktopVersion: "0.2.0", DesktopPlatform: feed.PlatformDarwinARM64, EngineArch: feed.ArchARM64,
			MaxDataSchema: 1, RetryDelay: -1, BootTimeout: time.Second, PollInterval: time.Millisecond,
			Now: func() time.Time { return demoPublished(8) },
			Log: func(s string) { t.Logf("rev-%d: %s", rev, s) },
		})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	check := func(rev int, c *updates.Coordinator, want string) {
		t.Helper()
		r, err := c.Check(ctx)
		if err != nil {
			t.Fatalf("rev-%d check: %v", rev, err)
		}
		var got []string
		for _, s := range r.Steps {
			got = append(got, fmt.Sprintf("%s %s->%s", s.Component, s.From, s.To))
		}
		if !r.Available || !r.Prefetched || strings.Join(got, ", ") != want {
			t.Fatalf("rev-%d check = %+v, want steps %q", rev, r, want)
		}
	}
	on := func(rev int, c *updates.Coordinator, engine, care, backend string) {
		t.Helper()
		if err := w.Healthy(); err != nil {
			t.Fatalf("rev-%d: clinic not healthy: %v", rev, err)
		}
		if w.Current != engine || w.Env[protocol.ImageRoleBackend] != fakeDigest(backend) {
			t.Fatalf("rev-%d: running engine %s backend %s, want %s %s", rev, w.Current, w.Env[protocol.ImageRoleBackend], engine, backend)
		}
		st, err := c.Status()
		if err != nil {
			t.Fatal(err)
		}
		if st.Journal != nil || st.Installed.Engine.Version != engine || st.Installed.Care.Version != care {
			t.Fatalf("rev-%d: status = %+v", rev, st)
		}
	}
	const (
		careA = "3.1.0+be.A.fe.A"
		careB = "3.1.0+be.B.fe.A"
		careC = "3.1.0+be.C.fe.A"
	)

	// 1: fresh install.
	c := open(1)
	check(1, c, "engine ->0.1.0, care ->"+careA)
	if res, err := c.Apply(ctx); err != nil || len(res.Applied) != 2 {
		t.Fatalf("rev-1 apply = %+v, %v", res, err)
	}
	on(1, c, "0.1.0", careA, "care-backend-A")

	// 2: CARE update.
	c = open(2)
	check(2, c, "care "+careA+"->"+careB)
	if _, err := c.Apply(ctx); err != nil {
		t.Fatalf("rev-2 apply: %v", err)
	}
	on(2, c, "0.1.0", careB, "care-backend-B")

	// 3: engine slot swap.
	c = open(3)
	check(3, c, "engine 0.1.0->0.1.1")
	if _, err := c.Apply(ctx); err != nil {
		t.Fatalf("rev-3 apply: %v", err)
	}
	on(3, c, "0.1.1", careB, "care-backend-B")
	if w.Previous != "0.1.0" {
		t.Fatalf("rev-3: previous slot = %q", w.Previous)
	}

	// 4: be.C fails its health check and is rolled back via db.restore.
	c = open(4)
	check(4, c, "care "+careB+"->"+careC)
	restores := w.Count("op db.restore")
	_, err = c.Apply(ctx)
	oe, ok := errors.AsType[*updates.OpError](err)
	if !ok || oe.Result != updates.ResultRolledBack || oe.Step != updates.StepHealth {
		t.Fatalf("rev-4 apply = %v, want rolled back at health", err)
	}
	if w.Count("op db.restore") != restores+1 {
		t.Fatal("rev-4: database not restored")
	}
	on(4, c, "0.1.1", careB, "care-backend-B")
	hist, err := c.History()
	if err != nil {
		t.Fatal(err)
	}
	if e := hist[len(hist)-1]; e.Result != updates.ResultRolledBack || e.To != careC || e.BackupID == "" {
		t.Fatalf("rev-4 history = %+v", e)
	}

	// 5: revision went backwards.
	if _, err := open(5).Check(ctx); !errors.Is(err, feed.ErrRollback) {
		t.Fatalf("rev-5 check = %v, want ErrRollback", err)
	}
	// 6: tampered signature.
	if _, err := open(6).Check(ctx); !errors.Is(err, feed.ErrBadSignature) {
		t.Fatalf("rev-6 check = %v, want ErrBadSignature", err)
	}

	// 7: nothing installable, with a reason.
	c = open(7)
	r, err := c.Check(ctx)
	if err != nil {
		t.Fatalf("rev-7 check: %v", err)
	}
	if r.Available || r.Revision != 7 || !strings.Contains(r.Reason, "desktop >=0.3.0") {
		t.Fatalf("rev-7 check = %+v, want not available because of desktop >=0.3.0", r)
	}
	if _, err := c.Apply(ctx); !errors.Is(err, updates.ErrNotAvailable) {
		t.Fatalf("rev-7 apply = %v", err)
	}
	on(7, c, "0.1.1", careB, "care-backend-B")
}
