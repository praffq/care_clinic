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

func testFrontendBuild() protocol.ImageBuild {
	return protocol.ImageBuild{
		Kind:    protocol.BuildFrontendRebuild,
		Base:    "ghcr.io/x/care-frontend@sha256:" + strings.Repeat("a", 64),
		Builder: "ghcr.io/x/care-frontend-builder@sha256:" + strings.Repeat("b", 64),
		BuildEnv: map[string]string{
			"REACT_APP_TITLE":    "Asha Clinic",
			"REACT_CARE_API_URL": "",
		},
	}
}

func TestImageBuildFrontend(t *testing.T) {
	op := testFrontendBuild()
	key := BuildKey(op)
	tag := protocol.BuildRepoFrontend + ":" + key[:12]
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
	if res.Ref != protocol.BuildRepoFrontend || string(res.Digest) != builtID {
		t.Fatalf("res = %+v", res)
	}
	var build Command
	var dockerfile string
	for i, c := range r.cmds {
		if c.Args[1] == "build" {
			build, dockerfile = c, string(r.stdin[i])
		}
	}
	base, builder := "care-clinic/build-input:base-"+key[:12], "care-clinic/build-input:builder-"+key[:12]
	wantDF := `FROM ` + base + ` AS published
FROM ` + builder + ` AS app
ARG REACT_APP_TITLE
ARG REACT_CARE_API_URL
WORKDIR /app
RUN ["npm","run","build"]
RUN ["rm","-rf","/app/build/plugins"]
COPY --from=published /usr/share/nginx/html/plugins /app/build/plugins
FROM ` + base + `
RUN ["rm","-rf","/usr/share/nginx/html"]
COPY --from=app /app/build /usr/share/nginx/html
`
	if dockerfile != wantDF {
		t.Fatalf("dockerfile =\n%s\nwant\n%s", dockerfile, wantDF)
	}
	want := []string{"docker", "build", "--progress", "plain", "--pull=false", "--tag", tag, "--label", protocol.BuildKeyLabel + "=" + key,
		"--build-arg", "REACT_APP_TITLE", "--build-arg", "REACT_CARE_API_URL", "-"}
	if !slices.Equal(build.Args, want) {
		t.Fatalf("build argv = %q", build.Args)
	}
	if !slices.Equal(build.Env, []string{"REACT_APP_TITLE=Asha Clinic", "REACT_CARE_API_URL="}) {
		t.Fatalf("build env = %q", build.Env)
	}
	for _, a := range build.Args {
		if strings.Contains(a, "Asha") {
			t.Fatal("value in argv")
		}
	}
}

func TestBuildKeyIncludesEnv(t *testing.T) {
	a, b := testFrontendBuild(), testFrontendBuild()
	b.BuildEnv = map[string]string{"REACT_APP_TITLE": "Other", "REACT_CARE_API_URL": ""}
	if BuildKey(a) == BuildKey(b) {
		t.Fatal("key ignores build env")
	}
}

func TestImageBuildFailureClassified(t *testing.T) {
	cases := []struct {
		name, stderr string
		permanent    bool
	}{
		{"pip not found", "#9 3.1 ERROR: Could not find a version that satisfies the requirement care-nope (from versions: none)\n#9 3.1 ERROR: No matching distribution found for care-nope", true},
		{"git missing repo", "#9 1.2 fatal: could not read Username for 'https://github.com': No such device or address", true},
		{"pip resolver", "#9 9.0 ERROR: ResolutionImpossible: for help visit https://pip.pypa.io", true},
		{"vite compile", "#12 40.1 error during build:\n#12 40.1 [vite]: Rollup failed to resolve import", true},
		{"network", "#9 3.1 WARNING: Retrying after connection broken by 'NewConnectionError: Temporary failure in name resolution'\n#9 3.1 ERROR: No matching distribution found for care-x", false},
		{"timeout", "#9 30.0 npm error code ETIMEDOUT", false},
		{"unknown", "#9 1.0 something odd", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, _ := newDocker(func(c Command) fakeRun {
				switch {
				case slices.Equal(c.Args[:3], []string{"docker", "image", "inspect"}) && strings.HasPrefix(c.Args[len(c.Args)-1], protocol.BuildRepoBackend):
					return fakeRun{err: errors.New("no such image")}
				case c.Args[1] == "build":
					return fakeRun{stderr: tc.stderr + "\n", err: errors.New("exit status 1")}
				}
				return fakeRun{stdout: `{"Id":"sha256:` + strings.Repeat("d", 64) + `"}`}
			})
			_, err := d.ImageBuild(context.Background(), testBuild(), &recEmitter{})
			var oe *protocol.OpError
			if !errors.As(err, &oe) {
				t.Fatalf("err = %v", err)
			}
			if got := oe.Code == protocol.CodeBuildFailed; got != tc.permanent {
				t.Fatalf("code = %s (%s)", oe.Code, oe.Message)
			}
			if tc.permanent && strings.Contains(oe.Message, "#9") {
				t.Fatalf("buildkit prefix kept: %s", oe.Message)
			}
		})
	}
}
