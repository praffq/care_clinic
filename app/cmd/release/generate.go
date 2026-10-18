package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
	"github.com/ohcnetwork/care_desktop/app/internal/feed"
	"github.com/ohcnetwork/care_desktop/app/internal/versions"
)

// keepPerComponent is how many releases per component a generated feed
// keeps; it matches the resolver's feed.MaxCandidates.
const keepPerComponent = feed.MaxCandidates

// Pins are the versions and requirements a release publishes; generate
// reads them from versions.yml.
type Pins struct {
	DesktopVersion        string
	DesktopRequiresEngine string
	EngineVersion         string
	EngineRequiresDesktop string
	// CareVersion is the semver core; the build metadata
	// (+be.<rev>.fe.<rev>) comes from the artifacts file.
	CareVersion         string
	CareRequiresDesktop string
	CareRequiresEngine  string
	// SupportImages are the tagged refs from versions.yml, used for the
	// repository name when the artifacts file gives only a digest.
	SupportImages map[protocol.ImageRole]string
}

func pinsFromVersions(f *versions.File) Pins {
	return Pins{
		DesktopVersion:        f.Desktop.Version.String(),
		DesktopRequiresEngine: f.Desktop.Requires.Engine.String(),
		EngineVersion:         f.Engine.Version.String(),
		EngineRequiresDesktop: f.Engine.Requires.Desktop.String(),
		CareVersion:           f.Care.Version.String(),
		CareRequiresDesktop:   f.Care.Requires.Desktop.String(),
		CareRequiresEngine:    f.Care.Requires.Engine.String(),
		SupportImages: map[protocol.ImageRole]string{
			protocol.ImageRolePostgres: f.Images.Postgres,
			protocol.ImageRoleRedis:    f.Images.Redis,
			protocol.ImageRoleSilo:     f.Images.Silo,
		},
	}
}

// Artifacts is the artifacts file passed to generate: what CI built for this
// release. Each section is optional; a missing section publishes no new
// entry for that component (earlier entries come from --previous).
type Artifacts struct {
	Desktop       *DesktopArtifacts                    `json:"desktop,omitempty"`
	Engine        *EngineArtifacts                     `json:"engine,omitempty"`
	Care          *CareArtifacts                       `json:"care,omitempty"`
	SupportImages map[protocol.ImageRole]feed.ImageRef `json:"support_images,omitempty"`
}

// DesktopArtifacts are the desktop installers, keyed by feed.Platform*.
type DesktopArtifacts struct {
	Assets map[string]feed.Asset `json:"assets"`
}

// EngineArtifacts are the engine bundles, keyed by feed.Arch*.
type EngineArtifacts struct {
	DataSchema int                   `json:"data_schema"`
	Assets     map[string]feed.Asset `json:"assets"`
}

// CareArtifacts are the CARE images of one backend/frontend commit pair.
// Image refs may omit the @digest (it is appended) and a tag (it is dropped).
type CareArtifacts struct {
	BackendRev  string                               `json:"backend_rev"`
	FrontendRev string                               `json:"frontend_rev"`
	ReleasedAt  time.Time                            `json:"released_at"`
	Migrations  bool                                 `json:"migrations"`
	Images      map[protocol.ImageRole]feed.ImageRef `json:"images"`
	BuildEnv    map[string]string                    `json:"build_env,omitempty"`
}

// BuildOptions are the inputs of Build.
type BuildOptions struct {
	Pins        Pins
	Artifacts   Artifacts
	Previous    *feed.Manifest
	Revision    uint64
	PublishedAt time.Time
	Expires     time.Time
	Block       []string
}

// Build assembles a manifest: the previous feed's entries, plus the new
// entries described by Artifacts, trimmed to the newest keepPerComponent per
// component, with Block appended to blocked. The result is not validated;
// see marshalValidated.
func Build(o BuildOptions) (*feed.Manifest, error) {
	if o.Revision == 0 {
		return nil, errors.New("revision must be >= 1")
	}
	m := &feed.Manifest{
		Schema:        feed.SchemaVersion,
		Revision:      o.Revision,
		PublishedAt:   o.PublishedAt.UTC(),
		Expires:       o.Expires.UTC(),
		SupportImages: map[protocol.ImageRole]feed.ImageRef{},
	}
	if p := o.Previous; p != nil {
		if o.Revision <= p.Revision {
			return nil, fmt.Errorf("revision %d must be greater than the previous feed's %d", o.Revision, p.Revision)
		}
		m.Desktop = slices.Clone(p.Desktop)
		m.Engine = slices.Clone(p.Engine)
		m.Care = slices.Clone(p.Care)
		maps.Copy(m.SupportImages, p.SupportImages)
		m.Blocked = slices.Clone(p.Blocked)
	}
	a := o.Artifacts

	if a.Desktop != nil {
		v, err := feed.NewVersion(o.Pins.DesktopVersion)
		if err != nil {
			return nil, fmt.Errorf("desktop version: %w", err)
		}
		req, err := feed.NewConstraint(o.Pins.DesktopRequiresEngine)
		if err != nil {
			return nil, fmt.Errorf("desktop requires engine: %w", err)
		}
		r := feed.DesktopRelease{Version: v, Requires: feed.Requires{Engine: req}, Assets: a.Desktop.Assets}
		m.Desktop = slices.DeleteFunc(m.Desktop, func(d feed.DesktopRelease) bool { return d.Version.Compare(v.Semver) == 0 })
		m.Desktop = append(m.Desktop, r)
	}

	if a.Engine != nil {
		v, err := feed.NewVersion(o.Pins.EngineVersion)
		if err != nil {
			return nil, fmt.Errorf("engine version: %w", err)
		}
		req, err := feed.NewConstraint(o.Pins.EngineRequiresDesktop)
		if err != nil {
			return nil, fmt.Errorf("engine requires desktop: %w", err)
		}
		ds := a.Engine.DataSchema
		if ds == 0 {
			ds = 1
		}
		r := feed.EngineRelease{Version: v, Requires: feed.Requires{Desktop: req}, DataSchema: ds, Assets: a.Engine.Assets}
		m.Engine = slices.DeleteFunc(m.Engine, func(e feed.EngineRelease) bool { return e.Version.Compare(v.Semver) == 0 })
		m.Engine = append(m.Engine, r)
	}

	if c := a.Care; c != nil {
		if strings.ContainsAny(o.Pins.CareVersion, "+-") {
			return nil, fmt.Errorf("care version %q must be a plain semver core; build metadata comes from the artifacts", o.Pins.CareVersion)
		}
		if c.BackendRev == "" || c.FrontendRev == "" {
			return nil, errors.New("artifacts care: backend_rev and frontend_rev are required")
		}
		v, err := feed.NewVersion(fmt.Sprintf("%s+be.%s.fe.%s", o.Pins.CareVersion, c.BackendRev, c.FrontendRev))
		if err != nil {
			return nil, fmt.Errorf("care version: %w", err)
		}
		reqD, err := feed.NewConstraint(o.Pins.CareRequiresDesktop)
		if err != nil {
			return nil, fmt.Errorf("care requires desktop: %w", err)
		}
		reqE, err := feed.NewConstraint(o.Pins.CareRequiresEngine)
		if err != nil {
			return nil, fmt.Errorf("care requires engine: %w", err)
		}
		images := map[protocol.ImageRole]feed.ImageRef{}
		for role, img := range c.Images {
			ref, err := pinRef(img.Ref, img.Digest)
			if err != nil {
				return nil, fmt.Errorf("artifacts care.images.%s: %w", role, err)
			}
			img.Ref = ref
			images[role] = img
		}
		r := feed.CareRelease{
			Version:    v,
			ReleasedAt: c.ReleasedAt.UTC(),
			Requires:   feed.Requires{Desktop: reqD, Engine: reqE},
			Migrations: c.Migrations,
			Images:     images,
			BuildEnv:   c.BuildEnv,
		}
		m.Care = slices.DeleteFunc(m.Care, func(x feed.CareRelease) bool { return x.Version.String() == v.String() })
		m.Care = append(m.Care, r)
	}

	for role, img := range a.SupportImages {
		ref := img.Ref
		if ref == "" {
			ref = o.Pins.SupportImages[role]
		}
		pinned, err := pinRef(ref, img.Digest)
		if err != nil {
			return nil, fmt.Errorf("artifacts support_images.%s: %w", role, err)
		}
		img.Ref = pinned
		m.SupportImages[role] = img
	}

	for _, b := range o.Block {
		comp, ver, ok := strings.Cut(b, ":")
		if !ok || (comp != feed.ComponentDesktop && comp != feed.ComponentEngine && comp != feed.ComponentCare) {
			return nil, fmt.Errorf("--block %q: want <desktop|engine|care>:<version>", b)
		}
		if _, err := versions.ParseSemver(ver); err != nil {
			return nil, fmt.Errorf("--block %q: %w", b, err)
		}
		if !slices.Contains(m.Blocked, b) {
			m.Blocked = append(m.Blocked, b)
		}
	}

	slices.SortStableFunc(m.Desktop, func(x, y feed.DesktopRelease) int { return x.Version.Compare(y.Version.Semver) })
	slices.SortStableFunc(m.Engine, func(x, y feed.EngineRelease) int { return x.Version.Compare(y.Version.Semver) })
	slices.SortStableFunc(m.Care, func(x, y feed.CareRelease) int {
		switch {
		case x.Newer(y):
			return 1
		case y.Newer(x):
			return -1
		}
		return 0
	})
	m.Desktop = keepNewest(m.Desktop)
	m.Engine = keepNewest(m.Engine)
	m.Care = keepNewest(m.Care)
	if m.Blocked == nil {
		m.Blocked = []string{}
	}
	return m, nil
}

// keepNewest returns the last keepPerComponent entries of an ascending list,
// never nil so the JSON is [] rather than null.
func keepNewest[T any](s []T) []T {
	if len(s) > keepPerComponent {
		s = s[len(s)-keepPerComponent:]
	}
	if s == nil {
		s = []T{}
	}
	return s
}

// pinRef returns ref's repository (tag dropped, Docker Hub names expanded)
// with @digest appended. A digest already in ref must equal digest.
func pinRef(ref string, digest protocol.Digest) (string, error) {
	if !digest.Valid() {
		return "", fmt.Errorf("digest %q must be sha256:<64 hex>", digest)
	}
	if ref == "" {
		return "", errors.New("ref is required")
	}
	name, existing, hasDigest := strings.Cut(ref, "@")
	if hasDigest && existing != string(digest) {
		return "", fmt.Errorf("ref %q embeds a different digest than %s", ref, digest)
	}
	if i := strings.LastIndexByte(name, ':'); i > strings.LastIndexByte(name, '/') {
		name = name[:i]
	}
	first, rest, hasSlash := strings.Cut(name, "/")
	switch {
	case !hasSlash:
		name = "docker.io/library/" + name
	case !strings.ContainsAny(first, ".:") && first != "localhost":
		name = "docker.io/" + first + "/" + rest
	}
	return name + "@" + string(digest), nil
}

// marshalValidated encodes m as updates.json and checks it with feed.Verify.
func marshalValidated(m *feed.Manifest, insecure bool) ([]byte, error) {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	if _, err := validateUnsigned(data, insecure); err != nil {
		return nil, err
	}
	return data, nil
}

// validateUnsigned runs data through feed.Verify by signing it with a
// throwaway key, evaluated at the feed's own published_at so expiry and
// rollback are not checked.
func validateUnsigned(data []byte, insecure bool) (*feed.Manifest, error) {
	var times struct {
		PublishedAt time.Time `json:"published_at"`
	}
	_ = json.Unmarshal(data, &times)
	now := times.PublishedAt
	if now.IsZero() {
		now = time.Now()
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return feed.Verify(data, feed.Sign(data, priv).Marshal(), feed.VerifyOptions{
		Keys:              feed.NewTrustedKeys(pub),
		Now:               now,
		AllowInsecureURLs: insecure,
	})
}

func decodeArtifacts(data []byte) (Artifacts, error) {
	var a Artifacts
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		return a, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return a, errors.New("trailing data after JSON value")
	}
	return a, nil
}

func cmdGenerate(args []string, stdout io.Writer) error {
	fs := newFlags("generate")
	versionsPath := fs.String("versions", "versions.yml", "versions.yml")
	artifactsPath := fs.String("artifacts", "", "artifacts JSON file (see docs/updates-feed.md)")
	previous := fs.String("previous", "", "previous updates.json to merge with")
	revision := fs.Uint64("revision", 0, "feed revision (must exceed --previous)")
	var published, expires timeFlag
	fs.Var(&published, "published-at", "publish time, RFC 3339 (default: now)")
	fs.Var(&expires, "expires", "expiry time, RFC 3339")
	var block listFlag
	fs.Var(&block, "block", "withdraw a release, component:version (repeatable)")
	insecure := fs.Bool("allow-insecure", false, "allow http:// URLs (development feeds)")
	out := fs.String("out", "updates.json", "output file")
	if err := parse(fs, args, "artifacts", "revision", "expires"); err != nil {
		return err
	}
	if published.t.IsZero() {
		published.t = time.Now().UTC().Truncate(time.Second)
	}
	vf, err := versions.Load(*versionsPath)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(*artifactsPath)
	if err != nil {
		return err
	}
	arts, err := decodeArtifacts(raw)
	if err != nil {
		return fmt.Errorf("%s: %w", *artifactsPath, err)
	}
	var prev *feed.Manifest
	if *previous != "" {
		data, err := os.ReadFile(*previous)
		if err != nil {
			return err
		}
		if prev, err = validateUnsigned(data, *insecure); err != nil {
			return fmt.Errorf("%s: %w", *previous, err)
		}
	}
	m, err := Build(BuildOptions{
		Pins:        pinsFromVersions(vf),
		Artifacts:   arts,
		Previous:    prev,
		Revision:    *revision,
		PublishedAt: published.t,
		Expires:     expires.t,
		Block:       block,
	})
	if err != nil {
		return err
	}
	data, err := marshalValidated(m, *insecure)
	if err != nil {
		return err
	}
	if err := os.WriteFile(*out, data, 0o644); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "wrote %s (revision %d: %d desktop, %d engine, %d care)\n", *out, m.Revision, len(m.Desktop), len(m.Engine), len(m.Care))
	return err
}
