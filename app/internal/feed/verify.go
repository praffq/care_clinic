package feed

import (
	"errors"
	"fmt"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
	"github.com/ohcnetwork/care_desktop/app/internal/versions"
)

var (
	// ErrBadSignature means the signature is malformed or does not match.
	ErrBadSignature = errors.New("feed: bad signature")
	// ErrUnknownKey means the signature names a key that is not trusted.
	ErrUnknownKey = errors.New("feed: signed by untrusted key")
	// ErrRollback means the feed revision is older than one already seen.
	ErrRollback = errors.New("feed: revision rollback")
	// ErrExpired means the feed's expires time has passed.
	ErrExpired = errors.New("feed: expired")
	// ErrInvalid wraps every manifest content error.
	ErrInvalid = errors.New("feed: invalid manifest")
)

// VerifyOptions controls Verify.
type VerifyOptions struct {
	// Keys are the trusted signing keys; see DefaultKeys.
	Keys TrustedKeys
	// Now is the current time; zero means time.Now().
	Now time.Time
	// MinRevision is the highest revision previously verified (State).
	MinRevision uint64
	// AllowInsecureURLs permits http URLs; set it only in devfeed builds.
	AllowInsecureURLs bool
	// AllowUnsigned accepts a feed with no signature (empty sig), or one
	// whose signature cannot be checked because Keys is empty; the manifest
	// is then marked Unsigned. A signature present with Keys set is always
	// checked. Set it only when UnsignedFeedAllowed.
	AllowUnsigned bool
}

// Verify checks the detached signature over data and then decodes and
// validates the manifest. The signature is checked before any parsing.
func Verify(data, sig []byte, opts VerifyOptions) (*Manifest, error) {
	unsigned := opts.AllowUnsigned && (len(sig) == 0 || len(opts.Keys) == 0)
	if !unsigned {
		if len(opts.Keys) == 0 {
			return nil, fmt.Errorf("%w: no trusted keys", ErrUnknownKey)
		}
		if err := opts.Keys.verify(data, sig); err != nil {
			return nil, err
		}
	}
	var m Manifest
	if err := decodeStrict(data, &m); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if m.Schema != SchemaVersion {
		return nil, fmt.Errorf("%w: schema: must be %d, got %d", ErrInvalid, SchemaVersion, m.Schema)
	}
	if m.Revision < opts.MinRevision {
		return nil, fmt.Errorf("%w: revision %d < %d", ErrRollback, m.Revision, opts.MinRevision)
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	if !m.Expires.IsZero() && !now.Before(m.Expires) {
		return nil, fmt.Errorf("%w: at %s", ErrExpired, m.Expires.Format(time.RFC3339))
	}
	if err := m.validate(opts.AllowInsecureURLs); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	m.Unsigned = unsigned
	return &m, nil
}

var (
	sha256RE = regexp.MustCompile(`^[a-f0-9]{64}$`)
	gitRevRE = regexp.MustCompile(`^[a-f0-9]{40}$`)
	repoRE   = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	pluginRE = regexp.MustCompile(`^[a-z][a-z0-9]*(?:_[a-z0-9]+)*$`)

	desktopPlatforms = []string{PlatformDarwinARM64, PlatformDarwinAMD64, PlatformWindowsAMD64}
	engineArches     = []string{ArchARM64, ArchAMD64}
	careImageRoles   = []protocol.ImageRole{
		protocol.ImageRoleBackend, protocol.ImageRoleFrontend, protocol.ImageRoleCaddy, protocol.ImageRoleBackup,
	}
	supportImageRoles = []protocol.ImageRole{
		protocol.ImageRolePostgres, protocol.ImageRoleRedis, protocol.ImageRoleSilo,
	}
	components = []string{ComponentDesktop, ComponentEngine, ComponentCare}
)

type validator struct {
	errs     []error
	insecure bool
}

func (v *validator) add(path, format string, args ...any) {
	v.errs = append(v.errs, fmt.Errorf("%s: %s", path, fmt.Sprintf(format, args...)))
}

func (m *Manifest) validate(insecure bool) error {
	v := &validator{insecure: insecure}
	if m.Revision == 0 {
		v.add("revision", "must be >= 1")
	}
	if m.PublishedAt.IsZero() {
		v.add("published_at", "required")
	}
	if m.Expires.IsZero() {
		v.add("expires", "required")
	} else if m.PublishedAt.After(m.Expires) {
		v.add("published_at", "must not be after expires")
	}

	var seen []versions.Semver
	for i := range m.Desktop {
		r := &m.Desktop[i]
		p := fmt.Sprintf("desktop[%d]", i)
		seen = v.version(p+".version", &r.Version, seen)
		v.requires(p+".requires", &r.Requires, ComponentDesktop)
		v.assets(p+".assets", r.Assets, desktopPlatforms)
	}
	seen = nil
	for i := range m.Engine {
		r := &m.Engine[i]
		p := fmt.Sprintf("engine[%d]", i)
		seen = v.version(p+".version", &r.Version, seen)
		v.requires(p+".requires", &r.Requires, ComponentEngine)
		if r.DataSchema < 1 {
			v.add(p+".data_schema", "must be >= 1")
		}
		v.assets(p+".assets", r.Assets, engineArches)
	}
	seenCare := map[string]bool{}
	seenReleased := map[time.Time]bool{}
	for i := range m.Care {
		r := &m.Care[i]
		p := fmt.Sprintf("care[%d]", i)
		v.version(p+".version", &r.Version, nil)
		if seenCare[r.Version.raw] {
			v.add(p+".version", "duplicate version %q", r.Version.raw)
		}
		seenCare[r.Version.raw] = true
		switch {
		case r.ReleasedAt.IsZero():
			v.add(p+".released_at", "required")
		case !m.PublishedAt.IsZero() && r.ReleasedAt.After(m.PublishedAt):
			v.add(p+".released_at", "must not be after published_at")
		case seenReleased[r.ReleasedAt.UTC()]:
			v.add(p+".released_at", "duplicate %s", r.ReleasedAt.Format(time.RFC3339Nano))
		}
		seenReleased[r.ReleasedAt.UTC()] = true
		v.requires(p+".requires", &r.Requires, ComponentCare)
		v.images(p+".images", r.Images, careImageRoles)
		if len(r.Builders) > 0 {
			v.images(p+".builders", r.Builders, protocol.BuilderRoles)
		}
		if s := r.Sources; s != nil {
			v.source(p+".sources.backend", s.Backend)
			v.source(p+".sources.frontend", s.Frontend)
			for _, id := range slices.Sorted(maps.Keys(s.Plugins)) {
				pp := p + ".sources.plugins." + id
				if !pluginRE.MatchString(id) {
					v.add(pp, "id must be a lower_snake_case Python identifier")
				}
				v.source(pp, s.Plugins[id].Source)
				if k := s.Plugins[id].Kind; k != versions.PluginBackend && k != versions.PluginFrontend {
					v.add(pp+".kind", "must be %q or %q, got %q", versions.PluginBackend, versions.PluginFrontend, k)
				}
			}
		}
		if len(r.BuildEnv) > 0 {
			b := protocol.ImageBuild{Kind: protocol.BuildFrontendRebuild, BuildEnv: r.BuildEnv}
			for _, fe := range fieldErrors(b.Validate()) {
				if strings.HasPrefix(fe.Field, "build_env") {
					v.add(p+"."+fe.Field, "%s", fe.Msg)
				}
			}
		}
	}
	v.images("support_images", m.SupportImages, supportImageRoles)

	seenBlocked := map[string]bool{}
	for i, b := range m.Blocked {
		p := fmt.Sprintf("blocked[%d]", i)
		comp, ver, ok := strings.Cut(b, ":")
		switch {
		case !ok || !slices.Contains(components, comp):
			v.add(p, "must be <desktop|engine|care>:<version>, got %q", b)
		case seenBlocked[b]:
			v.add(p, "duplicate %q", b)
		default:
			if _, err := versions.ParseSemver(ver); err != nil {
				v.add(p, "%v", err)
			}
		}
		seenBlocked[b] = true
	}
	return errors.Join(v.errs...)
}

// version parses ver and rejects one with the same precedence as an entry in
// seen (desktop and engine must bump their version); it returns seen plus ver.
func (v *validator) version(path string, ver *Version, seen []versions.Semver) []versions.Semver {
	sv, err := versions.ParseSemver(ver.raw)
	if err != nil {
		v.add(path, "%v", err)
		return seen
	}
	ver.Semver = sv
	for _, s := range seen {
		if s.Compare(sv) == 0 {
			v.add(path, "duplicate version %q", ver.raw)
			break
		}
	}
	return append(seen, sv)
}

func (v *validator) requires(path string, r *Requires, self string) {
	check := func(name string, c *Constraint) {
		p := path + "." + name
		if name == self {
			if c != nil {
				v.add(p, "a component cannot require itself")
			}
			return
		}
		if c == nil {
			v.add(p, "required")
			return
		}
		parsed, err := versions.ParseConstraint(c.raw)
		if err != nil {
			v.add(p, "%v", err)
			return
		}
		c.Constraint = parsed
	}
	check(ComponentDesktop, r.Desktop)
	check(ComponentEngine, r.Engine)
}

func (v *validator) assets(path string, a map[string]Asset, allowed []string) {
	if len(a) == 0 {
		v.add(path, "at least one asset required")
	}
	for _, k := range slices.Sorted(maps.Keys(a)) {
		p := path + "." + k
		if !slices.Contains(allowed, k) {
			v.add(p, "unknown key; must be one of %s", strings.Join(allowed, ", "))
			continue
		}
		as := a[k]
		v.url(p+".url", as.URL, true)
		if !sha256RE.MatchString(as.SHA256) {
			v.add(p+".sha256", "must be 64 lowercase hex characters")
		}
		if as.Size <= 0 {
			v.add(p+".size", "must be > 0")
		}
	}
}

func (v *validator) images(path string, imgs map[protocol.ImageRole]ImageRef, roles []protocol.ImageRole) {
	for _, r := range roles {
		if _, ok := imgs[r]; !ok {
			v.add(path+"."+string(r), "required")
		}
	}
	for _, r := range slices.Sorted(maps.Keys(imgs)) {
		p := path + "." + string(r)
		if !slices.Contains(roles, r) {
			v.add(p, "unexpected image role")
			continue
		}
		img := imgs[r]
		if !strings.Contains(img.Ref, "@") {
			v.add(p+".ref", "must embed @sha256 digest")
		}
		for _, fe := range fieldErrors(protocol.ImagePull{Role: r, Ref: img.Ref, Digest: img.Digest}.Validate()) {
			v.add(p+"."+fe.Field, "%s", fe.Msg)
		}
		v.url(p+".tar", img.Tar, false)
	}
}

func (v *validator) source(path string, s Source) {
	if !repoRE.MatchString(s.Repo) {
		v.add(path+".repo", "must be owner/name, got %q", s.Repo)
	}
	if strings.TrimSpace(s.Ref) == "" {
		v.add(path+".ref", "required")
	}
	if !gitRevRE.MatchString(s.Rev) {
		v.add(path+".rev", "must be a full 40-hex commit SHA, got %q", s.Rev)
	}
}

func (v *validator) url(path, raw string, required bool) {
	if raw == "" {
		if required {
			v.add(path, "required")
		}
		return
	}
	u, err := url.Parse(raw)
	switch {
	case err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http"):
		v.add(path, "must be an absolute URL, got %q", raw)
	case u.Scheme == "http" && !v.insecure:
		v.add(path, "must be https, got %q", raw)
	}
}

// fieldErrors reuses the engine protocol's image and build_env rules so the
// feed and the agent agree on what is valid.
func fieldErrors(err error) []protocol.FieldError {
	var ve *protocol.ValidationError
	if errors.As(err, &ve) {
		return ve.Errors
	}
	if err != nil {
		return []protocol.FieldError{{Field: "", Msg: err.Error()}}
	}
	return nil
}
