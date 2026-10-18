package main

import (
	"archive/tar"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
	"github.com/ohcnetwork/care_desktop/app/internal/feed"
)

// The demo feed walks a client through every update scenario; see the table
// in demo/README.md. Times are fixed so the output is reproducible.
var (
	demoBase    = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	demoExpires = time.Date(2036, 1, 1, 0, 0, 0, 0, time.UTC)
)

// demoPublished is when demo revision directory n is published.
func demoPublished(n int) time.Time { return demoBase.Add(time.Duration(n-1) * 24 * time.Hour) }

// Demo CARE versions use these backend/frontend revs (3.1.0+be.A.fe.A ...).
const (
	demoCareCore = "3.1.0"
	demoFrontend = "A"
)

type demoOptions struct {
	Key        ed25519.PrivateKey
	Out        string
	EngineDist string // engine/build.sh output (dist); optional
	ImagesDist string // image tarballs (<name>.tar); optional
	BaseURL    string
}

type demo struct {
	demoOptions
	insecure bool
	notes    []string // one line per artifact: real or placeholder
	images   map[string]feed.ImageRef
}

func cmdDemo(args []string, stdout io.Writer) error {
	fs := newFlags("demo")
	keyPath := fs.String("key", "demo/dev.key", "signing key (the committed dev key)")
	out := fs.String("out", "demo/feed", "output directory")
	engineDist := fs.String("engine-dist", "dist", "engine/build.sh output directory (missing: placeholders)")
	imagesDist := fs.String("images-dist", "dist/images", "image tarball directory (missing: placeholders)")
	baseURL := fs.String("base-url", "http://localhost:8787", "URL the output directory is served at")
	if err := parse(fs, args); err != nil {
		return err
	}
	key, err := loadPrivateKey(*keyPath)
	if err != nil {
		return err
	}
	d, err := runDemo(demoOptions{Key: key, Out: *out, EngineDist: *engineDist, ImagesDist: *imagesDist, BaseURL: *baseURL})
	if err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "wrote %s/rev-1 .. rev-7 (signed by %s); current -> rev-1\n", *out, feed.KeyID(key.Public().(ed25519.PublicKey)))
	for _, n := range d.notes {
		fmt.Fprintf(&b, "  %s\n", n)
	}
	fmt.Fprintf(&b, "serve with: demo/serve.sh <rev>\n")
	_, err = io.WriteString(stdout, b.String())
	return err
}

func runDemo(o demoOptions) (*demo, error) {
	u, err := url.Parse(o.BaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("--base-url %q must be an absolute http(s) URL", o.BaseURL)
	}
	o.BaseURL = strings.TrimSuffix(o.BaseURL, "/")
	d := &demo{demoOptions: o, insecure: u.Scheme == "http", images: map[string]feed.ImageRef{}}

	if err := os.MkdirAll(o.Out, 0o755); err != nil {
		return nil, err
	}
	old, err := filepath.Glob(filepath.Join(o.Out, "rev-*"))
	if err != nil {
		return nil, err
	}
	for _, p := range append(old, filepath.Join(o.Out, "files"), filepath.Join(o.Out, "current"), filepath.Join(o.Out, "ARTIFACTS.txt")) {
		if err := os.RemoveAll(p); err != nil {
			return nil, err
		}
	}

	pins := Pins{
		DesktopVersion:        "0.2.0",
		DesktopRequiresEngine: ">=0.1.0 <0.2.0",
		EngineVersion:         "0.1.0",
		EngineRequiresDesktop: ">=0.2.0",
		CareVersion:           demoCareCore,
		CareRequiresDesktop:   ">=0.2.0",
		CareRequiresEngine:    ">=0.1.0",
	}
	build := func(prev *feed.Manifest, rev uint64, dir int, p Pins, a Artifacts) (*feed.Manifest, []byte, error) {
		m, err := Build(BuildOptions{Pins: p, Artifacts: a, Previous: prev, Revision: rev, PublishedAt: demoPublished(dir), Expires: demoExpires})
		if err != nil {
			return nil, nil, fmt.Errorf("rev-%d: %w", dir, err)
		}
		data, err := marshalValidated(m, d.insecure)
		if err != nil {
			return nil, nil, fmt.Errorf("rev-%d: %w", dir, err)
		}
		return m, data, nil
	}

	// rev-1: desktop 0.2.0, engine 0.1.0, CARE be.A (fresh install).
	support, err := d.supportImages()
	if err != nil {
		return nil, err
	}
	desktop := d.desktopAssets("0.2.0")
	engine010, err := d.engineAssets("0.1.0")
	if err != nil {
		return nil, err
	}
	careA, err := d.care("A", "care-backend-A", demoPublished(1).Add(-time.Hour), true)
	if err != nil {
		return nil, err
	}
	m1, data1, err := build(nil, 1, 1, pins, Artifacts{
		Desktop:       &DesktopArtifacts{Assets: desktop},
		Engine:        &EngineArtifacts{DataSchema: 1, Assets: engine010},
		Care:          careA,
		SupportImages: support,
	})
	if err != nil {
		return nil, err
	}

	// rev-2: + CARE be.B, same semver core, newer released_at.
	careB, err := d.care("B", "care-backend-B", demoPublished(2).Add(-time.Hour), false)
	if err != nil {
		return nil, err
	}
	m2, data2, err := build(m1, 2, 2, pins, Artifacts{Care: careB})
	if err != nil {
		return nil, err
	}

	// rev-3: + engine 0.1.1.
	p3 := pins
	p3.EngineVersion = "0.1.1"
	engine011, err := d.engineAssets("0.1.1")
	if err != nil {
		return nil, err
	}
	m3, data3, err := build(m2, 3, 3, p3, Artifacts{Engine: &EngineArtifacts{DataSchema: 1, Assets: engine011}})
	if err != nil {
		return nil, err
	}

	// rev-4: + CARE be.C, whose backend image is intentionally broken (it
	// will fail its health check once the app applies updates).
	careC, err := d.care("C", "care-backend-C-broken", demoPublished(4).Add(-time.Hour), false)
	if err != nil {
		return nil, err
	}
	m4, data4, err := build(m3, 4, 4, pins, Artifacts{Care: careC})
	if err != nil {
		return nil, err
	}

	// rev-5: rev-3's content republished later but with revision 3: a
	// replayed feed that clients which saw revision 4 reject as a rollback.
	m5 := *m3
	m5.PublishedAt = demoPublished(5)
	data5, err := marshalValidated(&m5, d.insecure)
	if err != nil {
		return nil, fmt.Errorf("rev-5: %w", err)
	}

	// rev-7: + engine 0.2.0 requiring a desktop 0.3.0 that is not published.
	p7 := pins
	p7.EngineVersion = "0.2.0"
	p7.EngineRequiresDesktop = ">=0.3.0"
	engine020, err := d.engineAssets("0.2.0")
	if err != nil {
		return nil, err
	}
	_, data7, err := build(m4, 7, 7, p7, Artifacts{Engine: &EngineArtifacts{DataSchema: 2, Assets: engine020}})
	if err != nil {
		return nil, err
	}

	revs := []struct {
		data []byte
		sig  []byte
	}{
		{data1, nil}, {data2, nil}, {data3, nil}, {data4, nil}, {data5, nil},
		// rev-6: rev-4 with one signature byte flipped.
		{data4, flipSignatureByte(feed.Sign(data4, o.Key))},
		{data7, nil},
	}
	for i, r := range revs {
		dir := filepath.Join(o.Out, fmt.Sprintf("rev-%d", i+1))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
		sig := r.sig
		if sig == nil {
			sig = feed.Sign(r.data, o.Key).Marshal()
		}
		if err := os.WriteFile(filepath.Join(dir, "updates.json"), r.data, 0o644); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(dir, "updates.json.sig"), sig, 0o644); err != nil {
			return nil, err
		}
	}
	if err := os.Symlink("rev-1", filepath.Join(o.Out, "current")); err != nil {
		d.notes = append(d.notes, fmt.Sprintf("could not create current symlink: %v", err))
	}
	sort.Strings(d.notes)
	report := "Demo feed artifacts (generated by `release demo`; see demo/README.md).\n" +
		"Placeholders have made-up digests and no file behind their URL.\n\n" +
		strings.Join(d.notes, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(o.Out, "ARTIFACTS.txt"), []byte(report), 0o644); err != nil {
		return nil, err
	}
	return d, nil
}

func flipSignatureByte(s feed.Signature) []byte {
	raw, _ := base64.StdEncoding.DecodeString(s.Sig)
	raw[0] ^= 0x01
	s.Sig = base64.StdEncoding.EncodeToString(raw)
	return s.Marshal()
}

// placeholderSHA is a deterministic made-up SHA-256 for a missing artifact.
func placeholderSHA(kind, name string) string {
	sum := sha256.Sum256([]byte("care-demo-placeholder " + kind + " " + name))
	return hex.EncodeToString(sum[:])
}

func (d *demo) url(parts ...string) string {
	return d.BaseURL + "/files/" + strings.Join(parts, "/")
}

func (d *demo) desktopAssets(version string) map[string]feed.Asset {
	assets := map[string]feed.Asset{}
	for platform, ext := range map[string]string{
		feed.PlatformDarwinARM64:  "dmg",
		feed.PlatformDarwinAMD64:  "dmg",
		feed.PlatformWindowsAMD64: "exe",
	} {
		name := fmt.Sprintf("CARE-Clinic-%s-%s.%s", version, platform, ext)
		assets[platform] = feed.Asset{URL: d.url("desktop", name), SHA256: placeholderSHA("desktop", name), Size: 1}
		d.notes = append(d.notes, "placeholder desktop "+name)
	}
	return assets
}

func (d *demo) engineAssets(version string) (map[string]feed.Asset, error) {
	assets := map[string]feed.Asset{}
	for _, arch := range []string{feed.ArchARM64, feed.ArchAMD64} {
		name := fmt.Sprintf("care-engine-%s-%s.tar.gz", version, arch)
		a := feed.Asset{URL: d.url("engine", name), SHA256: placeholderSHA("engine", name), Size: 1}
		if d.EngineDist != "" {
			src := filepath.Join(d.EngineDist, name)
			sum, size, err := hashFile(src)
			switch {
			case err == nil:
				a.SHA256, a.Size = sum, size
				if err := d.publish(src, "engine", name); err != nil {
					return nil, err
				}
				d.notes = append(d.notes, "real        engine "+name)
				assets[arch] = a
				continue
			case !errors.Is(err, fs.ErrNotExist):
				return nil, err
			}
		}
		d.notes = append(d.notes, "placeholder engine "+name)
		assets[arch] = a
	}
	return assets, nil
}

// image pins the image tarball <name>.tar from ImagesDist when present.
// Its digest comes from <name>.tar.digest, else the tarball's OCI index.json
// (docker save), else the tarball's own SHA-256.
func (d *demo) image(repo, name string) (feed.ImageRef, error) {
	if img, ok := d.images[name]; ok {
		return img, nil
	}
	img, err := d.loadImage(repo, name)
	if err == nil {
		d.images[name] = img
	}
	return img, err
}

func (d *demo) loadImage(repo, name string) (feed.ImageRef, error) {
	file := name + ".tar"
	if d.ImagesDist != "" {
		src := filepath.Join(d.ImagesDist, file)
		if _, err := os.Stat(src); err == nil {
			dg, err := imageDigest(src)
			if err != nil {
				return feed.ImageRef{}, err
			}
			if err := d.publish(src, "images", file); err != nil {
				return feed.ImageRef{}, err
			}
			d.notes = append(d.notes, "real        image "+file+" "+string(dg))
			return feed.ImageRef{Ref: repo + "@" + string(dg), Digest: dg, Tar: d.url("images", file)}, nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return feed.ImageRef{}, err
		}
	}
	dg := protocol.Digest("sha256:" + placeholderSHA("image", name))
	d.notes = append(d.notes, "placeholder image "+file)
	return feed.ImageRef{Ref: repo + "@" + string(dg), Digest: dg}, nil
}

const demoRegistry = "ghcr.io/praffq/care_clinic/"

func (d *demo) care(backendRev, backendImage string, releasedAt time.Time, migrations bool) (*CareArtifacts, error) {
	images := map[protocol.ImageRole]feed.ImageRef{}
	for role, src := range map[protocol.ImageRole]struct{ repo, file string }{
		protocol.ImageRoleBackend:  {"care-backend", backendImage},
		protocol.ImageRoleFrontend: {"care-frontend", "care-frontend-" + demoFrontend},
		protocol.ImageRoleCaddy:    {"caddy", "care-caddy"},
		protocol.ImageRoleBackup:   {"backup", "care-backup"},
	} {
		img, err := d.image(demoRegistry+src.repo, src.file)
		if err != nil {
			return nil, err
		}
		images[role] = img
	}
	return &CareArtifacts{
		BackendRev:  backendRev,
		FrontendRev: demoFrontend,
		ReleasedAt:  releasedAt,
		Migrations:  migrations,
		Images:      images,
	}, nil
}

func (d *demo) supportImages() (map[protocol.ImageRole]feed.ImageRef, error) {
	images := map[protocol.ImageRole]feed.ImageRef{}
	for role, repo := range map[protocol.ImageRole]string{
		protocol.ImageRolePostgres: "docker.io/library/postgres",
		protocol.ImageRoleRedis:    "docker.io/library/redis",
		protocol.ImageRoleSilo:     "docker.io/pgsty/silo",
	} {
		img, err := d.image(repo, string(role))
		if err != nil {
			return nil, err
		}
		images[role] = img
	}
	return images, nil
}

// publish exposes src at <Out>/files/<dir>/<name>: a symlink, or a copy where
// symlinks are unavailable.
func (d *demo) publish(src, dir, name string) error {
	dstDir := filepath.Join(d.Out, "files", dir)
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return err
	}
	abs, err := filepath.Abs(src)
	if err != nil {
		return err
	}
	dst := filepath.Join(dstDir, name)
	if err := os.Remove(dst); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Symlink(abs, dst); err == nil {
		return nil
	}
	return copyFile(abs, dst)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func imageDigest(path string) (protocol.Digest, error) {
	if b, err := os.ReadFile(path + ".digest"); err == nil {
		dg := protocol.Digest(strings.TrimSpace(string(b)))
		if !dg.Valid() {
			return "", fmt.Errorf("%s.digest: %q is not sha256:<64 hex>", path, dg)
		}
		return dg, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if dg, err := ociIndexDigest(path); err != nil {
		return "", err
	} else if dg != "" {
		return dg, nil
	}
	sum, _, err := hashFile(path)
	if err != nil {
		return "", err
	}
	return protocol.Digest("sha256:" + sum), nil
}

// ociIndexDigest returns the first manifest digest in the tarball's
// index.json, or "" when it has none.
func ociIndexDigest(path string) (protocol.Digest, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	tr := tar.NewReader(f)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return "", nil
		}
		if err != nil {
			return "", fmt.Errorf("%s: %w", path, err)
		}
		if strings.TrimPrefix(h.Name, "./") != "index.json" {
			continue
		}
		var idx struct {
			Manifests []struct {
				Digest protocol.Digest `json:"digest"`
			} `json:"manifests"`
		}
		if err := json.NewDecoder(tr).Decode(&idx); err != nil {
			return "", fmt.Errorf("%s: index.json: %w", path, err)
		}
		if len(idx.Manifests) > 0 && idx.Manifests[0].Digest.Valid() {
			return idx.Manifests[0].Digest, nil
		}
		return "", nil
	}
}
