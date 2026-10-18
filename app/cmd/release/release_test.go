package main

import (
	"archive/tar"
	"bytes"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
	"github.com/ohcnetwork/care_desktop/app/internal/feed"
	"github.com/ohcnetwork/care_desktop/app/internal/versions"
)

const (
	devKeyPath = "../../../demo/dev.key"
	devPubPath = "../../internal/feed/devkeys/dev.pub"
)

func devKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	k, err := loadPrivateKey(devKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestDevKeyMatchesDevPub(t *testing.T) {
	pub, err := loadPublicKey(devPubPath)
	if err != nil {
		t.Fatal(err)
	}
	if !pub.Equal(devKey(t).Public()) {
		t.Fatal("demo/dev.key does not match app/internal/feed/devkeys/dev.pub")
	}
}

func readRev(t *testing.T, dir string, n int) (data, sig []byte) {
	t.Helper()
	p := filepath.Join(dir, fmt.Sprintf("rev-%d", n), "updates.json")
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if sig, err = os.ReadFile(p + ".sig"); err != nil {
		t.Fatal(err)
	}
	return data, sig
}

func semver(t *testing.T, s string) versions.Semver {
	t.Helper()
	v, err := versions.ParseSemver(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func careVersion(t *testing.T, m *feed.Manifest, v string) *feed.CareVersion {
	t.Helper()
	for _, c := range m.Care {
		if c.Version.String() == v {
			return &feed.CareVersion{Version: c.Version.Semver, ReleasedAt: c.ReleasedAt}
		}
	}
	t.Fatalf("care %s not in feed", v)
	return nil
}

func steps(p *feed.Plan) string {
	var s []string
	for _, st := range p.Steps {
		s = append(s, st.Component+" "+st.From+"->"+st.To)
	}
	return strings.Join(s, ", ")
}

// TestDemo checks the expected client outcome of every demo revision, as
// listed in demo/README.md.
func TestDemo(t *testing.T) {
	out := t.TempDir()
	if _, err := runDemo(demoOptions{Key: devKey(t), Out: out, BaseURL: "http://localhost:8787"}); err != nil {
		t.Fatal(err)
	}
	pub, err := loadPublicKey(devPubPath)
	if err != nil {
		t.Fatal(err)
	}
	opts := func(minRev uint64) feed.VerifyOptions {
		return feed.VerifyOptions{Keys: feed.NewTrustedKeys(pub), Now: demoPublished(8), MinRevision: minRev, AllowInsecureURLs: true}
	}
	verify := func(n int, minRev uint64) *feed.Manifest {
		t.Helper()
		data, sig := readRev(t, out, n)
		m, err := feed.Verify(data, sig, opts(minRev))
		if err != nil {
			t.Fatalf("rev-%d: %v", n, err)
		}
		return m
	}
	installed := func(m *feed.Manifest, engine, care string) feed.Installed {
		in := feed.Installed{Desktop: semver(t, "0.2.0"), DesktopAsset: feed.PlatformDarwinARM64, EngineArch: feed.ArchARM64}
		if engine != "" {
			in.Engine = semver(t, engine)
		}
		if care != "" {
			in.Care = careVersion(t, m, care)
		}
		return in
	}
	plan := func(n int, in feed.Installed, m *feed.Manifest, want string) *feed.Plan {
		t.Helper()
		p, err := feed.ResolvePlan(in, m)
		if err != nil {
			t.Fatalf("rev-%d: %v", n, err)
		}
		if got := steps(p); got != want {
			t.Fatalf("rev-%d: steps = %q, want %q", n, got, want)
		}
		return p
	}
	const (
		careA = "3.1.0+be.A.fe.A"
		careB = "3.1.0+be.B.fe.A"
		careC = "3.1.0+be.C.fe.A"
	)

	m1 := verify(1, 0)
	plan(1, installed(m1, "", ""), m1, "engine ->0.1.0, care ->"+careA)

	m2 := verify(2, 1)
	plan(2, installed(m2, "0.1.0", careA), m2, "care "+careA+"->"+careB)

	m3 := verify(3, 2)
	plan(3, installed(m3, "0.1.0", careB), m3, "engine 0.1.0->0.1.1")

	m4 := verify(4, 3)
	plan(4, installed(m4, "0.1.1", careB), m4, "care "+careB+"->"+careC)
	if m4.Care[len(m4.Care)-1].Images[protocol.ImageRoleBackend].Digest == m3.Care[len(m3.Care)-1].Images[protocol.ImageRoleBackend].Digest {
		t.Fatal("rev-4: broken backend image must have its own digest")
	}

	data5, sig5 := readRev(t, out, 5)
	if _, err := feed.Verify(data5, sig5, opts(4)); !errors.Is(err, feed.ErrRollback) {
		t.Fatalf("rev-5 with MinRevision 4: err = %v, want ErrRollback", err)
	}
	verify(5, 3)

	data6, sig6 := readRev(t, out, 6)
	if _, err := feed.Verify(data6, sig6, opts(4)); !errors.Is(err, feed.ErrBadSignature) {
		t.Fatalf("rev-6: err = %v, want ErrBadSignature", err)
	}

	m7 := verify(7, 4)
	p := plan(7, installed(m7, "0.1.0", careB), m7, "engine 0.1.0->0.1.1, care "+careB+"->"+careC)
	if len(p.Held) != 1 || !strings.Contains(p.Held[0], "engine 0.2.0") || !strings.Contains(p.Held[0], "desktop >=0.3.0") {
		t.Fatalf("rev-7 held = %q", p.Held)
	}
	_, err = feed.ResolvePlan(installed(m7, "0.1.1", careC), m7)
	var np *feed.NoPathError
	if !errors.As(err, &np) || !strings.Contains(err.Error(), "desktop >=0.3.0") {
		t.Fatalf("rev-7 up to date: err = %v, want NoPathError mentioning desktop >=0.3.0", err)
	}

	if target, err := os.Readlink(filepath.Join(out, "current")); err != nil || target != "rev-1" {
		t.Fatalf("current -> %q, %v", target, err)
	}

	// Regenerating is deterministic and replaces the previous output.
	before, _ := os.ReadFile(filepath.Join(out, "rev-7", "updates.json"))
	if _, err := runDemo(demoOptions{Key: devKey(t), Out: out, BaseURL: "http://localhost:8787"}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(out, "rev-7", "updates.json"))
	if string(before) != string(after) {
		t.Fatal("demo output is not deterministic")
	}
}

func TestDemoRealArtifacts(t *testing.T) {
	dist := t.TempDir()
	images := filepath.Join(dist, "images")
	if err := os.MkdirAll(images, 0o755); err != nil {
		t.Fatal(err)
	}
	engine := filepath.Join(dist, "care-engine-0.1.0-arm64.tar.gz")
	if err := os.WriteFile(engine, []byte("engine bundle"), 0o644); err != nil {
		t.Fatal(err)
	}
	backend := filepath.Join(images, "care-backend-A.tar")
	if err := os.WriteFile(backend, []byte("image"), 0o644); err != nil {
		t.Fatal(err)
	}
	dg := "sha256:" + strings.Repeat("ab", 32)
	if err := os.WriteFile(backend+".digest", []byte(dg+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	idxDigest := "sha256:" + strings.Repeat("cd", 32)
	writeTar(t, filepath.Join(images, "redis.tar"), "index.json", `{"schemaVersion":2,"manifests":[{"digest":"`+idxDigest+`"}]}`)

	out := t.TempDir()
	if _, err := runDemo(demoOptions{Key: devKey(t), Out: out, EngineDist: dist, ImagesDist: images, BaseURL: "http://localhost:8787/"}); err != nil {
		t.Fatal(err)
	}
	data, sig := readRev(t, out, 1)
	m, err := feed.Verify(data, sig, feed.VerifyOptions{Keys: feed.NewTrustedKeys(devKey(t).Public().(ed25519.PublicKey)), Now: demoPublished(8), AllowInsecureURLs: true})
	if err != nil {
		t.Fatal(err)
	}
	sum, size, _ := hashFile(engine)
	a := m.Engine[0].Assets[feed.ArchARM64]
	if a.SHA256 != sum || a.Size != size || a.URL != "http://localhost:8787/files/engine/care-engine-0.1.0-arm64.tar.gz" {
		t.Fatalf("engine asset = %+v", a)
	}
	if m.Engine[0].Assets[feed.ArchAMD64].SHA256 != placeholderSHA("engine", "care-engine-0.1.0-amd64.tar.gz") {
		t.Fatal("missing amd64 bundle should be a placeholder")
	}
	img := m.Care[0].Images[protocol.ImageRoleBackend]
	if string(img.Digest) != dg || img.Ref != demoRegistry+"care-backend@"+dg || img.Tar != "http://localhost:8787/files/images/care-backend-A.tar" {
		t.Fatalf("backend image = %+v", img)
	}
	if r := m.SupportImages[protocol.ImageRoleRedis]; string(r.Digest) != idxDigest || r.Ref != "docker.io/library/redis@"+idxDigest {
		t.Fatalf("redis image = %+v", r)
	}
	if m.Care[0].Images[protocol.ImageRoleFrontend].Tar != "" {
		t.Fatal("placeholder image must not have a tar URL")
	}
	for _, f := range []string{"engine/care-engine-0.1.0-arm64.tar.gz", "images/care-backend-A.tar"} {
		b, err := os.ReadFile(filepath.Join(out, "files", f))
		if err != nil {
			t.Fatal(err)
		}
		if len(b) == 0 {
			t.Fatalf("%s is empty", f)
		}
	}
	// A tarball whose digest cannot be determined is an error, not a guess.
	writeTar(t, filepath.Join(images, "care-caddy.tar"), "manifest.json", "[]")
	if _, err := runDemo(demoOptions{Key: devKey(t), Out: out, EngineDist: dist, ImagesDist: images, BaseURL: "http://localhost:8787"}); err == nil || !strings.Contains(err.Error(), "care-caddy.tar: no image digest") {
		t.Fatalf("tarball without digest: %v", err)
	}
	if err := os.Remove(filepath.Join(images, "care-caddy.tar")); err != nil {
		t.Fatal(err)
	}
	// Re-running must not truncate the sources behind the published links.
	if _, err := runDemo(demoOptions{Key: devKey(t), Out: out, EngineDist: dist, ImagesDist: images, BaseURL: "http://localhost:8787"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(engine); string(b) != "engine bundle" {
		t.Fatalf("source modified: %q", b)
	}
}

func writeTar(t *testing.T, path, name, content string) {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeJSON(t *testing.T, path, s string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

const testArtifacts = `{
  "desktop": {"assets": {"darwin-arm64": {"url": "https://example.com/d.dmg", "sha256": "` + "1111111111111111111111111111111111111111111111111111111111111111" + `", "size": 10}}},
  "engine": {"data_schema": 1, "assets": {"arm64": {"url": "https://example.com/e.tar.gz", "sha256": "` + "2222222222222222222222222222222222222222222222222222222222222222" + `", "size": 20}}},
  "care": {
    "backend_rev": "4f2a1c", "frontend_rev": "9b33d0", "released_at": "2026-09-30T00:00:00Z", "migrations": true,
    "images": {
      "backend": {"ref": "ghcr.io/praffq/care_clinic/care-backend:main", "digest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
      "frontend": {"ref": "ghcr.io/praffq/care_clinic/care-frontend", "digest": "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
      "caddy": {"ref": "ghcr.io/praffq/care_clinic/caddy", "digest": "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"},
      "backup": {"ref": "ghcr.io/praffq/care_clinic/backup", "digest": "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"}
    }
  },
  "support_images": {
    "postgres": {"digest": "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"},
    "redis": {"digest": "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"},
    "silo": {"digest": "sha256:1111111111111111111111111111111111111111111111111111111111111111"}
  }
}`

// TestCommands runs keygen, generate (twice, merging), sign and verify.
func TestCommands(t *testing.T) {
	dir := t.TempDir()
	p := func(name string) string { return filepath.Join(dir, name) }
	if err := run([]string{"keygen", "--out", p("k.key")}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(p("k.key")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key mode: %v %v", fi, err)
	}
	if err := run([]string{"keygen", "--out", p("k.key")}, io.Discard); err == nil {
		t.Fatal("keygen overwrote an existing key")
	}

	writeJSON(t, p("arts.json"), testArtifacts)
	gen := []string{"generate", "--versions", "../../../versions.yml", "--artifacts", p("arts.json"),
		"--revision", "1", "--published-at", "2026-10-01T00:00:00Z", "--expires", "2026-11-01T00:00:00Z", "--out", p("u1.json")}
	if err := run(gen, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"sign", "--key", p("k.key"), "--in", p("u1.json")}, io.Discard); err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	if err := run([]string{"verify", "--pub", p("k.pub"), "--in", p("u1.json")}, &sb); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sb.String(), "revision 1") || !strings.Contains(sb.String(), "care    3.1.0+be.4f2a1c.fe.9b33d0") {
		t.Fatalf("verify output:\n%s", sb.String())
	}
	data, _ := os.ReadFile(p("u1.json"))
	if !strings.Contains(string(data), `"ref": "docker.io/library/postgres@sha256:eeee`) ||
		!strings.Contains(string(data), `"ref": "ghcr.io/praffq/care_clinic/care-backend@sha256:aaaa`) {
		t.Fatalf("image refs not pinned:\n%s", data)
	}
	if err := run([]string{"verify", "--pub", p("k.pub"), "--in", p("u1.json"), "--min-revision", "2"}, io.Discard); !errors.Is(err, feed.ErrRollback) {
		t.Fatalf("min-revision: %v", err)
	}

	// Second release: a new CARE build only, merged with the first, blocking the first.
	writeJSON(t, p("arts2.json"), `{"care": {"backend_rev": "7c91e0", "frontend_rev": "9b33d0", "released_at": "2026-10-01T12:00:00Z",
	  "images": {
	    "backend": {"ref": "ghcr.io/praffq/care_clinic/care-backend", "digest": "sha256:3333333333333333333333333333333333333333333333333333333333333333"},
	    "frontend": {"ref": "ghcr.io/praffq/care_clinic/care-frontend", "digest": "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	    "caddy": {"ref": "ghcr.io/praffq/care_clinic/caddy", "digest": "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"},
	    "backup": {"ref": "ghcr.io/praffq/care_clinic/backup", "digest": "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"}}}}`)
	gen2 := []string{"generate", "--versions", "../../../versions.yml", "--artifacts", p("arts2.json"), "--previous", p("u1.json"),
		"--revision", "2", "--published-at", "2026-10-02T00:00:00Z", "--expires", "2026-11-02T00:00:00Z",
		"--block", "care:3.1.0+be.4f2a1c.fe.9b33d0", "--out", p("u2.json")}
	if err := run(gen2, io.Discard); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(p("u2.json"))
	m, err := validateUnsigned(data, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Desktop) != 1 || len(m.Engine) != 1 || len(m.Care) != 2 || m.Care[1].Version.String() != "3.1.0+be.7c91e0.fe.9b33d0" ||
		len(m.Blocked) != 1 || len(m.SupportImages) != 3 {
		t.Fatalf("merged manifest: %s", data)
	}

	// The revision must increase over --previous.
	gen2[8] = "1"
	if err := run(gen2, io.Discard); err == nil || !strings.Contains(err.Error(), "must be greater") {
		t.Fatalf("non-increasing revision: %v", err)
	}
	// Invalid output is rejected by feed validation (http URL without --allow-insecure).
	writeJSON(t, p("bad.json"), strings.Replace(testArtifacts, "https://example.com/d.dmg", "http://example.com/d.dmg", 1))
	gen[4] = p("bad.json")
	if err := run(gen, io.Discard); !errors.Is(err, feed.ErrInvalid) {
		t.Fatalf("insecure URL: %v", err)
	}
	if err := run(append(gen, "--allow-insecure"), io.Discard); err != nil {
		t.Fatal(err)
	}
}

func TestBuildKeepsNewest(t *testing.T) {
	pins := Pins{EngineRequiresDesktop: ">=0.2.0"}
	var prev *feed.Manifest
	for i := 1; i <= 7; i++ {
		pins.EngineVersion = fmt.Sprintf("0.1.%d", i)
		m, err := Build(BuildOptions{
			Pins:      pins,
			Artifacts: Artifacts{Engine: &EngineArtifacts{Assets: map[string]feed.Asset{"arm64": {URL: "https://e", SHA256: strings.Repeat("a", 64), Size: 1}}}},
			Previous:  prev, Revision: uint64(i), PublishedAt: time.Unix(0, 0), Expires: time.Unix(1, 0),
		})
		if err != nil {
			t.Fatal(err)
		}
		prev = m
	}
	var got []string
	for _, e := range prev.Engine {
		got = append(got, e.Version.String())
	}
	if want := "0.1.3 0.1.4 0.1.5 0.1.6 0.1.7"; strings.Join(got, " ") != want {
		t.Fatalf("engine = %v, want %s", got, want)
	}
	if prev.Engine[0].DataSchema != 1 {
		t.Fatal("data_schema should default to 1")
	}
}

func TestPinRef(t *testing.T) {
	dg := protocol.Digest("sha256:" + strings.Repeat("a", 64))
	other := "sha256:" + strings.Repeat("b", 64)
	for _, tc := range []struct{ ref, want string }{
		{"postgres:17.10-alpine", "docker.io/library/postgres@" + string(dg)},
		{"pgsty/silo:RELEASE.1", "docker.io/pgsty/silo@" + string(dg)},
		{"docker.io/pgsty/silo:RELEASE.1", "docker.io/pgsty/silo@" + string(dg)},
		{"localhost:5000/care", "localhost:5000/care@" + string(dg)},
		{"ghcr.io/x/care@" + string(dg), "ghcr.io/x/care@" + string(dg)},
		{"ghcr.io/x/care@" + other, ""},
		{"", ""},
	} {
		got, err := pinRef(tc.ref, dg)
		if (err != nil) != (tc.want == "") || got != tc.want {
			t.Errorf("pinRef(%q) = %q, %v; want %q", tc.ref, got, err, tc.want)
		}
	}
	if _, err := pinRef("x", "sha256:nope"); err == nil {
		t.Error("invalid digest accepted")
	}
}
