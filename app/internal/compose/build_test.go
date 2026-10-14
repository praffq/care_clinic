package compose

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/enginetest"

	"github.com/ohcnetwork/care_desktop/app/internal/careversions"
	"github.com/ohcnetwork/care_desktop/app/internal/release"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/proc"
)

func writeBuildFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func digestRef(name string, c byte) string {
	return "ghcr.io/ohcnetwork/care-clinic-" + name + "@sha256:" + strings.Repeat(string(c), 64)
}

func versionFixture(c byte) careversions.Manifest {
	return careversions.Manifest{
		Version: "3.1.0",
		Backend: careversions.Image{
			Commit:     strings.Repeat(string(c), 40),
			Image:      digestRef("backend", c),
			Builder:    digestRef("backend-builder", c),
			SourceHash: strings.Repeat(string(c), 64),
		},
		Frontend: careversions.Image{
			Commit:     strings.Repeat(string(c), 40),
			Image:      digestRef("frontend", c),
			Builder:    digestRef("frontend-builder", c),
			SourceHash: strings.Repeat(string(c), 64),
			BuildEnv:   map[string]string{"REACT_MFE_REGISTERED_COMPONENTS": "UserDashboard"},
		},
	}
}

type fixtureTransport func(*http.Request) (*http.Response, error)

func (f fixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func publish(t *testing.T, b *Builder, m careversions.Manifest) {
	t.Helper()
	hash := strings.Repeat("a", 64)
	asset := release.Asset{URL: "https://updates.test/installer", SHA256: hash, Size: 42}
	revision := uint64(1)
	if previous, err := os.ReadFile(filepath.Join(b.dir, "fixture-updates.json")); err == nil {
		var old release.Manifest
		if err := json.Unmarshal(previous, &old); err != nil {
			t.Fatal(err)
		}
		revision = old.Revision + 1
	}
	manifest := release.Manifest{
		Schema: 1, Revision: revision, PublishedAt: "2026-10-14T14:00:00Z",
		Desktop:       release.DesktopUpdate{Version: "0.1.19", RequiresEngine: ">=0.1.0", Assets: map[string]release.Asset{"darwin-universal": asset, "windows-amd64": asset}},
		Engine:        release.EngineUpdate{Version: "0.1.2", RequiresDesktop: ">=0.1.0", Assets: map[string]release.Asset{"linux-arm64": asset, "linux-amd64": asset}},
		Care:          release.CareUpdate{Version: m.Version, RequiresDesktop: ">=0.1.0", RequiresEngine: ">=0.1.0", Backend: m.Backend, Frontend: m.Frontend},
		SupportImages: map[string]string{"postgres": digestRef("postgres", 'a'), "redis": digestRef("redis", 'a'), "caddy": digestRef("caddy", 'a'), "silo": digestRef("silo", 'a'), "backup": digestRef("backup", 'a')},
		Blocked:       []string{}, SourceHashes: map[string]string{"desktop": hash, "engine": hash, "support": hash},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	key, err := os.ReadFile(filepath.Join(b.dir, "fixture-key"))
	if err != nil {
		t.Fatal(err)
	}
	writeBuildFile(t, b.dir, "fixture-updates.json", string(data))
	writeBuildFile(t, b.dir, "fixture-updates.json.sig", string(ed25519.Sign(key, data)))
}

func builderFixture(t *testing.T) *Builder {
	t.Helper()
	dir := t.TempDir()
	for name, content := range map[string]string{
		"backend.env":             "ADDITIONAL_PLUGS='[{\"name\":\"one\",\"package_name\":\"pkg_one\"}]'\nPOSTGRES_HOST=db\n",
		"frontend.env":            "REACT_CARE_API_URL=https://care.local\nREACT_MFE_REGISTERED_COMPONENTS=UserDashboard\n",
		"backup.Dockerfile":       "ARG POSTGRES_IMAGE\nFROM ${POSTGRES_IMAGE}\nRUN apk add --no-cache openssl\n",
		"caddy.Dockerfile":        "ARG CADDY_IMAGE\nFROM ${CADDY_IMAGE}\nARG CORAZA_VERSION\n",
		backendPluginsDockerfile:  "ARG BACKEND_IMAGE\nFROM ${BACKEND_IMAGE}\n# plugins\n",
		frontendRebuildDockerfile: "ARG FRONTEND_IMAGE\nFROM ${FRONTEND_IMAGE}\n# rebuild\n",
	} {
		writeBuildFile(t, dir, name, content)
	}
	b := NewBuilder(proc.Runner{Dir: dir}, dir, &release.Pins{
		AppVersion:    "0.1.0-dev",
		UpdatesURL:    "https://updates.test/fixture-updates.json",
		CAREVersion:   "3.1.0",
		BackendImage:  "care:clinic",
		FrontendImage: "care_fe:clinic",
		BackupImage:   "care-backup:clinic",
		CaddyWafImage: "care-caddy:clinic",
		PostgresImage: "postgres:17-alpine",
		CaddyImage:    "caddy:2",
		CorazaVersion: "v2.6.1",
	}, nil)
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keys, client := release.PublicKeys, http.DefaultClient
	release.PublicKeys = []string{base64.StdEncoding.EncodeToString(pub)}
	http.DefaultClient = &http.Client{Transport: fixtureTransport(func(r *http.Request) (*http.Response, error) {
		data, err := os.ReadFile(filepath.Join(b.dir, filepath.Base(r.URL.Path)))
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(data))), Header: make(http.Header)}, nil
	})}
	t.Cleanup(func() { release.PublicKeys, http.DefaultClient = keys, client })
	writeBuildFile(t, b.dir, "fixture-key", string(private))
	publish(t, b, versionFixture('a'))
	return b
}

func key(t *testing.T, plan func() (plan, error)) string {
	t.Helper()
	p, err := plan()
	if err != nil {
		t.Fatal(err)
	}
	return p.key
}

func imageKeys(t *testing.T, b *Builder) [4]string {
	t.Helper()
	backup, err := b.backupBuiltFrom()
	if err != nil {
		t.Fatal(err)
	}
	caddy, err := b.caddyBuiltFrom()
	if err != nil {
		t.Fatal(err)
	}
	be := key(t, b.backendPlan)
	fe := key(t, b.frontendPlan)
	return [4]string{be, fe, backup, caddy}
}

func TestImageKeysTrackBuildInputs(t *testing.T) {
	tests := []struct {
		name    string
		changed [4]bool
		mutate  func(*testing.T, *Builder)
	}{
		{"app version", [4]bool{}, func(t *testing.T, b *Builder) { b.set.AppVersion = "0.2.0-dev" }},
		{"backend release", [4]bool{true}, func(t *testing.T, b *Builder) {
			l := versionFixture('a')
			l.Backend = versionFixture('b').Backend
			publish(t, b, l)
		}},
		{"frontend release", [4]bool{false, true}, func(t *testing.T, b *Builder) {
			l := versionFixture('a')
			l.Frontend = versionFixture('b').Frontend
			publish(t, b, l)
		}},
		{"plugins", [4]bool{true}, func(t *testing.T, b *Builder) {
			writeBuildFile(t, b.dir, "backend.env", "ADDITIONAL_PLUGS='[{\"name\":\"two\",\"package_name\":\"pkg_two\"}]'\n")
		}},
		{"frontend API origin", [4]bool{}, func(t *testing.T, b *Builder) {
			writeBuildFile(t, b.dir, "frontend.env", "REACT_CARE_API_URL=https://other.local\nREACT_MFE_REGISTERED_COMPONENTS=UserDashboard\n")
		}},
		{"frontend title is compiled", [4]bool{false, true}, func(t *testing.T, b *Builder) {
			writeBuildFile(t, b.dir, "frontend.env", "REACT_CARE_API_URL=https://other.local\nREACT_APP_TITLE=Clinic\n")
		}},
		{"frontend build setting", [4]bool{false, true}, func(t *testing.T, b *Builder) {
			writeBuildFile(t, b.dir, "frontend.env", "REACT_MFE_REGISTERED_COMPONENTS=Other\n")
		}},
		{"blank build setting", [4]bool{false, true}, func(t *testing.T, b *Builder) {
			writeBuildFile(t, b.dir, "frontend.env", "REACT_MFE_REGISTERED_COMPONENTS=\n")
		}},
		{"postgres", [4]bool{false, false, true}, func(t *testing.T, b *Builder) { b.set.PostgresImage = "postgres:18-alpine" }},
		{"backup Dockerfile", [4]bool{false, false, true}, func(t *testing.T, b *Builder) {
			writeBuildFile(t, b.dir, "backup.Dockerfile", "ARG POSTGRES_IMAGE\nFROM ${POSTGRES_IMAGE}\nRUN apk add openssl tar\n")
		}},
		{"prebuilt backup", [4]bool{false, false, true}, func(t *testing.T, b *Builder) { b.set.BackupPrebuilt = digestRef("backup", 'c') }},
		{"caddy", [4]bool{false, false, false, true}, func(t *testing.T, b *Builder) { b.set.CaddyImage = "caddy:2.11" }},
		{"coraza", [4]bool{false, false, false, true}, func(t *testing.T, b *Builder) { b.set.CorazaVersion = "v2.7.0" }},
		{"caddy Dockerfile", [4]bool{false, false, false, true}, func(t *testing.T, b *Builder) {
			writeBuildFile(t, b.dir, "caddy.Dockerfile", "ARG CADDY_IMAGE\nFROM ${CADDY_IMAGE}\nARG CORAZA_VERSION\nRUN caddy version\n")
		}},
		{"prebuilt caddy", [4]bool{false, false, false, true}, func(t *testing.T, b *Builder) { b.set.CaddyWafPrebuilt = digestRef("caddy", 'c') }},
		{"runtime files", [4]bool{}, func(t *testing.T, b *Builder) {
			writeBuildFile(t, b.dir, "backend.env", "POSTGRES_HOST=other-db\nBUCKET_KEY=custom-user\nFILE_UPLOAD_BUCKET=records\nADDITIONAL_PLUGS='[{\"name\":\"one\",\"package_name\":\"pkg_one\"}]'\n")
			writeBuildFile(t, b.dir, "Caddyfile", "other.local:443 {}\n")
			writeBuildFile(t, b.dir, "scripts/backup.sh", "exit 0\n")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := builderFixture(t)
			before := imageKeys(t, b)
			tt.mutate(t, b)
			after := imageKeys(t, b)
			for i, wantChanged := range tt.changed {
				if gotChanged := before[i] != after[i]; gotChanged != wantChanged {
					t.Errorf("image %d changed = %v, want %v (%s -> %s)", i, gotChanged, wantChanged, before[i], after[i])
				}
			}
		})
	}
}

func TestPlainImagesUseThePublishedDigest(t *testing.T) {
	b := builderFixture(t)
	writeBuildFile(t, b.dir, "backend.env", "POSTGRES_HOST=db\n")
	if got := imageKeys(t, b); got[0] != digestRef("backend", 'a') || got[1] != digestRef("frontend", 'a') {
		t.Fatalf("keys = %v", got)
	}
}

func TestUnsupportedCAREVersionIsAnError(t *testing.T) {
	b := builderFixture(t)
	b.set.CAREVersion = "3.0.0"
	if _, err := b.backendPlan(); err == nil {
		t.Fatal("an unsupported CARE version produced an image")
	}
}

func TestUnsignedCAREImagesAreRefused(t *testing.T) {
	b := builderFixture(t)
	writeBuildFile(t, b.dir, "fixture-updates.json.sig", "untrusted")
	if _, err := b.frontendPlan(); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("unsigned images were accepted: %v", err)
	}
}

func TestBuildSettingsMergeOnlyCompileTimeValues(t *testing.T) {
	rel := versionFixture('a').Frontend
	merged, changed := BuildSettings(rel, map[string]string{
		"REACT_CARE_API_URL":              "https://x",
		"REACT_MFE_REGISTERED_COMPONENTS": "UserDashboard",
		"REACT_CUSTOM_DESCRIPTION":        "",
	})
	if !changed || len(merged) != 2 || merged["REACT_CUSTOM_DESCRIPTION"] != "" {
		t.Fatalf("explicit blank value did not force a rebuild: %v %v", merged, changed)
	}
	merged, changed = BuildSettings(rel, map[string]string{"REACT_CUSTOM_DESCRIPTION": "Ours"})
	if !changed || merged["REACT_CUSTOM_DESCRIPTION"] != "Ours" || len(merged) != 1 {
		t.Fatalf("merge = %v %v", merged, changed)
	}
	if rel.BuildEnv["REACT_CUSTOM_DESCRIPTION"] != "" {
		t.Fatal("merge mutated the release")
	}
	merged, changed = BuildSettings(rel, map[string]string{
		"REACT_MFE_REGISTERED_COMPONENTS": "UserDashboard", "REACT_CARE_API_URL": "https://north.local",
	})
	if changed || len(merged) != 1 {
		t.Fatal("equal compiled values or changed API hostname forced a rebuild")
	}
	merged, changed = BuildSettings(rel, map[string]string{})
	if !changed || len(merged) != 0 {
		t.Fatal("removing a compiled value retained the release's old value")
	}
}

func dockerFixture(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX command fixture")
	}
	dir := t.TempDir()
	trace := filepath.Join(dir, "calls")
	t.Setenv("CARE_BUILD_TRACE", trace)
	script := `#!/bin/sh
printf '%s\n' "$@" >> "$CARE_BUILD_TRACE"
printf '%s\n' '--' >> "$CARE_BUILD_TRACE"
case "$1 $2" in
  'image ls')
    [ "$CARE_FAIL_LIST" != yes ] || exit 21
    [ "$CARE_IMAGE_EXISTS" != yes ] || printf 'image-id\n'
    ;;
  'image inspect')
    if [ "$3" = --format ]; then
      [ "$CARE_REFS_LOCAL" = yes ] || exit 1
      printf 'sha256:local\n'
      exit 0
    fi
    [ "$CARE_FAIL_INSPECT" != yes ] || exit 22
    printf '%s\n' "$CARE_IMAGE_KEY"
    ;;
  'image prune'|'image tag') ;;
  pull*)
    [ -z "$CARE_FAIL_PULL_MATCH" ] || case "$*" in *"$CARE_FAIL_PULL_MATCH"*) exit 24;; esac
    [ "$CARE_FAIL_PULL" != yes ] || exit 24
    ;;
  build*)
    for arg do context=$arg; done
    [ -d "$context" ] || exit 23
    [ "$2" = -f ] && cat "$3" >> "$CARE_BUILD_TRACE.dockerfiles"
    [ ! -f "$context/clinic.json" ] || cp "$context/clinic.json" "$CARE_BUILD_TRACE.json"
    ;;
  *) exit 99 ;;
esac
`
	program := filepath.Join(dir, "engine-docker")
	if err := os.WriteFile(program, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	agent := enginetest.New(t, nil).Install(t)
	agent.SetHandler(agent.Exec(program))
	return trace
}

func readTrace(t *testing.T, trace string) string {
	t.Helper()
	data, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestEnsureDistinguishesMissingImagesFromInspectionFailures(t *testing.T) {
	for _, tt := range []struct {
		name    string
		exists  string
		key     string
		failure string
		builds  int
	}{
		{"missing", "", "", "", 1},
		{"current", "yes", "wanted", "", 0},
		{"stale", "yes", "older", "", 1},
		{"unrecorded", "yes", "<no value>", "", 1},
		{"list failure", "yes", "", "CARE_FAIL_LIST", 0},
		{"inspect failure", "yes", "", "CARE_FAIL_INSPECT", 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dockerFixture(t)
			t.Setenv("CARE_IMAGE_EXISTS", tt.exists)
			t.Setenv("CARE_IMAGE_KEY", tt.key)
			if tt.failure != "" {
				t.Setenv(tt.failure, "yes")
			}
			builds := 0
			err := builderFixture(t).ensure("care:clinic", "wanted", "backend", func() error {
				builds++
				return nil
			})
			if (err != nil) != (tt.failure != "") {
				t.Errorf("unexpected inspection result: %v", err)
			}
			if builds != tt.builds {
				t.Errorf("built %d images, want %d", builds, tt.builds)
			}
		})
	}
}

func TestBuildAndEnsurePropagateInputReadErrors(t *testing.T) {
	for _, name := range []string{"backend.env", "frontend.env", "backup.Dockerfile", "caddy.Dockerfile"} {
		for _, failure := range []string{"missing", "directory"} {
			t.Run(name+"/"+failure, func(t *testing.T) {
				trace := dockerFixture(t)
				b := builderFixture(t)
				path := filepath.Join(b.dir, name)
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if failure == "directory" {
					if err := os.Mkdir(path, 0o700); err != nil {
						t.Fatal(err)
					}
				}
				operations := map[string][]func() error{
					"backend.env":       {b.BuildBackend, b.EnsureBackendImage},
					"frontend.env":      {b.BuildFrontend, b.EnsureFrontendImage},
					"backup.Dockerfile": {b.BuildBackup, b.EnsureBackupImage},
					"caddy.Dockerfile":  {b.BuildCaddy, b.EnsureCaddyImage},
				}
				for _, operation := range operations[name] {
					if err := operation(); err == nil {
						t.Fatal("unreadable build input was ignored")
					}
				}
				if _, err := os.Stat(trace); !os.IsNotExist(err) {
					t.Fatalf("Docker was called before inputs were readable: %v", err)
				}
			})
		}
	}
	t.Run("malformed plugins", func(t *testing.T) {
		trace := dockerFixture(t)
		b := builderFixture(t)
		writeBuildFile(t, b.dir, "backend.env", "ADDITIONAL_PLUGS='unterminated\n")
		for _, operation := range []func() error{b.BuildBackend, b.EnsureBackendImage} {
			if err := operation(); err == nil {
				t.Fatal("malformed build inputs were accepted")
			}
		}
		if _, err := os.Stat(trace); !os.IsNotExist(err) {
			t.Fatalf("Docker was called with malformed inputs: %v", err)
		}
	})
}

func TestBuildLabelsMatchConsumedInputs(t *testing.T) {
	trace := dockerFixture(t)
	b := builderFixture(t)
	writeBuildFile(t, b.dir, "frontend.env", "REACT_CARE_API_URL=https://care.local\nREACT_CUSTOM_DESCRIPTION=Our clinic\n")
	keys := imageKeys(t, b)
	for _, build := range []func() error{b.BuildBackend, b.BuildFrontend, b.BuildBackup, b.BuildCaddy} {
		if err := build(); err != nil {
			t.Fatal(err)
		}
	}
	data := readTrace(t, trace)
	for _, key := range keys {
		if !strings.Contains(data, builtFromLabel+"="+key+"\n") {
			t.Errorf("build did not label its inputs: %s", key)
		}
	}
	rel := versionFixture('a')
	for _, want := range []string{
		"pull\n" + rel.Backend.Builder + "\n",
		"BACKEND_BUILDER=" + rel.Backend.Builder + "\n",
		"BACKEND_IMAGE=" + rel.Backend.Image + "\n",
		`ADDITIONAL_PLUGS=[{"name":"one","package_name":"pkg_one"}]` + "\n",
		"FRONTEND_BUILDER=" + rel.Frontend.Builder + "\n",
	} {
		if !strings.Contains(data, want) {
			t.Errorf("docker was not called with %q", want)
		}
	}
	dockerfiles := readTrace(t, trace+".dockerfiles")
	for _, name := range []string{backendPluginsDockerfile, frontendRebuildDockerfile} {
		want, err := os.ReadFile(filepath.Join(b.dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(dockerfiles, string(want)) {
			t.Errorf("build did not upload %s", name)
		}
	}
	var env map[string]string
	if err := json.Unmarshal([]byte(readTrace(t, trace+".json")), &env); err != nil {
		t.Fatal(err)
	}
	if env["REACT_CARE_API_URL"] != "https://care-clinic.invalid/__CARE_CLINIC_REACT_CARE_API_URL__" || env["REACT_CUSTOM_DESCRIPTION"] != "Our clinic" || len(env) != 2 {
		t.Fatalf("frontend rebuild did not receive exact effective values: %v", env)
	}
	contexts, err := filepath.Glob(filepath.Join(b.dir, ".care-buildctx-*"))
	if err != nil || len(contexts) != 0 {
		t.Fatalf("empty build contexts were not cleaned up: %v, %v", contexts, err)
	}
}

func TestLocalImagesAreNotDownloadedAgain(t *testing.T) {
	trace := dockerFixture(t)
	t.Setenv("CARE_REFS_LOCAL", "yes")
	b := builderFixture(t)
	writeBuildFile(t, b.dir, "backend.env", "POSTGRES_HOST=db\n")
	if err := b.BuildBackend(); err != nil {
		t.Fatal(err)
	}
	if data := readTrace(t, trace); strings.Contains(data, "pull\n") {
		t.Fatal("an image already on this computer was downloaded again")
	}
}

func TestFailedDownloadIsReported(t *testing.T) {
	dockerFixture(t)
	t.Setenv("CARE_FAIL_PULL", "yes")
	b := builderFixture(t)
	if err := b.EnsureFrontendImage(); err == nil || !strings.Contains(err.Error(), "network") {
		t.Fatalf("download failure = %v", err)
	}
	if ReadLock(b.dir).Frontend.Current.Image != "" {
		t.Fatal("a failed download was recorded as installed")
	}
}
