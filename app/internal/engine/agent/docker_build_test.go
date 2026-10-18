package agent

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
)

var (
	buildBase    = "ghcr.io/x/care-backend@sha256:" + strings.Repeat("a", 64)
	buildBuilder = "ghcr.io/x/care-backend-builder@sha256:" + strings.Repeat("b", 64)
	builtID      = "sha256:" + strings.Repeat("c", 64)
)

func testBuild() protocol.ImageBuild {
	return protocol.ImageBuild{
		Kind: protocol.BuildBackendPlugins, Base: buildBase, Builder: buildBuilder,
		Plugins: []protocol.PluginSpec{
			{Name: "care_filly", Package: "git+https://github.com/ohcnetwork/care_filly.git", Version: "@main"},
			{Name: "care_abdm", Package: "care-abdm", Version: "==1.2"},
		},
	}
}

func TestImageBuildBackend(t *testing.T) {
	op := testBuild()
	key := BuildKey(op)
	tag := protocol.BuildRepoBackend + ":" + key[:12]
	built := false
	d, r := newDocker(func(c Command) fakeRun {
		switch {
		case slices.Equal(c.Args[:3], []string{"docker", "image", "inspect"}) && c.Args[len(c.Args)-1] == tag:
			if !built {
				return fakeRun{err: errors.New("no such image")}
			}
			return fakeRun{stdout: `{"Id":"` + builtID + `"}`}
		case slices.Equal(c.Args[:3], []string{"docker", "image", "inspect"}):
			return fakeRun{stdout: `{"Id":"sha256:` + strings.Repeat("d", 64) + `"}`}
		case c.Args[1] == "build":
			built = true
		}
		return fakeRun{}
	})
	res, err := d.ImageBuild(context.Background(), op, &recEmitter{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Ref != protocol.BuildRepoBackend || string(res.Digest) != builtID {
		t.Fatalf("res = %+v", res)
	}
	var dockerfile string
	for i, a := range r.argv() {
		if a[1] == "build" {
			dockerfile = string(r.stdin[i])
		}
	}
	wantDF := `FROM care-clinic/build-input:builder-` + key[:12] + ` AS plugins
RUN ["python","-m","pip","install","--no-cache-dir","--disable-pip-version-check","git+https://github.com/ohcnetwork/care_filly.git@main","care-abdm==1.2"]
FROM care-clinic/build-input:base-` + key[:12] + `
COPY --from=plugins --chown=django:django /app/.venv /app/.venv
`
	if dockerfile != wantDF {
		t.Fatalf("dockerfile =\n%s\nwant\n%s", dockerfile, wantDF)
	}
	var build []string
	for _, a := range r.argv() {
		if a[1] == "build" {
			build = a
		}
	}
	want := []string{"docker", "build", "--progress", "plain", "--pull=false", "--tag", tag, "--label", protocol.BuildKeyLabel + "=" + key, "-"}
	if !slices.Equal(build, want) {
		t.Fatalf("build argv = %q", build)
	}
	last := r.argv()[len(r.argv())-1]
	if last[1] != "image" || last[2] != "rm" {
		t.Fatalf("input tags not removed: %q", r.argv())
	}
}

func TestImageBuildCached(t *testing.T) {
	op := testBuild()
	key := BuildKey(op)
	d, r := newDocker(func(c Command) fakeRun {
		return fakeRun{stdout: `{"Id":"` + builtID + `","Config":{"Labels":{"` + protocol.BuildKeyLabel + `":"` + key + `"}}}`}
	})
	res, err := d.ImageBuild(context.Background(), op, &recEmitter{})
	if err != nil || string(res.Digest) != builtID {
		t.Fatalf("res = %+v, %v", res, err)
	}
	if len(r.cmds) != 1 {
		t.Fatalf("cached build ran %q", r.argv())
	}
}

func TestImageBuildMissingInput(t *testing.T) {
	d, r := newDocker(func(c Command) fakeRun { return fakeRun{err: errors.New("no such image")} })
	_, err := d.ImageBuild(context.Background(), testBuild(), &recEmitter{})
	var oe *protocol.OpError
	if !errors.As(err, &oe) || !strings.Contains(oe.Message, "not in the engine") {
		t.Fatalf("err = %v", err)
	}
	for _, a := range r.argv() {
		if a[1] == "build" {
			t.Fatal("built without inputs")
		}
	}
}

func TestBuildKeyStable(t *testing.T) {
	a, b := testBuild(), testBuild()
	if BuildKey(a) != BuildKey(b) {
		t.Fatal("key not deterministic")
	}
	b.Plugins = b.Plugins[:1]
	if BuildKey(a) == BuildKey(b) {
		t.Fatal("key ignores plugins")
	}
}
