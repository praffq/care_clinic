package main

import (
	"strings"
	"testing"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
	"github.com/ohcnetwork/care_desktop/app/internal/feed"
)

func TestCareReleaseVersionPlugins(t *testing.T) {
	rev := func(c string) string { return strings.Repeat(c, 40) }
	src := func(r string) feed.Source { return feed.Source{Repo: "o/r", Ref: "main", Rev: r} }
	c := &CareArtifacts{BackendRev: "aaaaaaaaaaaa", FrontendRev: "bbbbbbbbbbbb"}
	if got := careReleaseVersion("3.1.0", c); got != "3.1.0+be.aaaaaaaaaaaa.fe.bbbbbbbbbbbb" {
		t.Fatalf("no plugins: %s", got)
	}
	c.Sources = &feed.CareSources{Backend: src(rev("a")), Frontend: src(rev("b")), Plugins: map[string]feed.PluginSource{
		"care_filly":         {Source: src(rev("1")), Kind: "backend"},
		"care_onboarding_fe": {Source: src(rev("2")), Kind: "frontend"},
	}}
	v1 := careReleaseVersion("3.1.0", c)
	if !strings.HasPrefix(v1, "3.1.0+be.aaaaaaaaaaaa.fe.bbbbbbbbbbbb.pl.") || len(v1) != len("3.1.0+be.aaaaaaaaaaaa.fe.bbbbbbbbbbbb.pl.")+8 {
		t.Fatalf("with plugins: %s", v1)
	}
	if again := careReleaseVersion("3.1.0", c); again != v1 {
		t.Fatalf("not deterministic: %s vs %s", again, v1)
	}
	c.Sources.Plugins["care_filly"] = feed.PluginSource{Source: src(rev("3")), Kind: "backend"}
	if v2 := careReleaseVersion("3.1.0", c); v2 == v1 {
		t.Fatal("plugin change must change the version")
	}
}

func TestBuildCarriesBuildersAndSources(t *testing.T) {
	d := func(c string) protocol.Digest { return protocol.Digest("sha256:" + strings.Repeat(c, 64)) }
	img := func(name, c string) feed.ImageRef {
		return feed.ImageRef{Ref: "ghcr.io/x/" + name + ":sha-1", Digest: d(c)}
	}
	rev := strings.Repeat("a", 40)
	src := feed.Source{Repo: "ohcnetwork/care", Ref: "develop", Rev: rev}
	m, err := Build(BuildOptions{
		Pins: Pins{CareVersion: "3.1.0", CareRequiresDesktop: ">=0.2.0", CareRequiresEngine: ">=0.1.0"},
		Artifacts: Artifacts{Care: &CareArtifacts{
			BackendRev: rev[:12], FrontendRev: rev[:12], ReleasedAt: time.Unix(0, 0),
			Images: map[protocol.ImageRole]feed.ImageRef{
				"backend": img("care-backend", "1"), "frontend": img("care-frontend", "2"),
				"caddy": img("care-caddy", "3"), "backup": img("care-backup", "4"),
			},
			Builders: map[protocol.ImageRole]feed.ImageRef{
				protocol.ImageRoleBackendBuilder:  img("care-backend-builder", "5"),
				protocol.ImageRoleFrontendBuilder: img("care-frontend-builder", "6"),
			},
			Sources: &feed.CareSources{Backend: src, Frontend: src, Plugins: map[string]feed.PluginSource{
				"care_filly": {Source: src, Kind: "backend"},
			}},
		}},
		Revision: 1, PublishedAt: time.Unix(1, 0), Expires: time.Unix(2, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	c := m.Care[0]
	if got := c.Builders[protocol.ImageRoleBackendBuilder].Ref; got != "ghcr.io/x/care-backend-builder@"+string(d("5")) {
		t.Fatalf("builder ref = %s", got)
	}
	if c.Sources == nil || c.Sources.Plugins["care_filly"].Rev != rev || !strings.Contains(c.Version.String(), ".pl.") {
		t.Fatalf("care = %+v", c)
	}
}
