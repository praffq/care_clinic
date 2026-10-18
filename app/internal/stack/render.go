// Package stack installs and drives the CARE compose stack inside the engine
// VM. Everything goes through typed agent ops relayed by the host service;
// nothing here runs docker on the host.
//
// The deployment kit (the repo's deployments/ directory) is uploaded mostly
// verbatim to the agent's config file area, plus three generated files:
//
//   - stack.env   compose interpolation: digest-pinned images, the host name
//     and hashes of bind-mounted files (so changes recreate the container)
//   - clinic.env  origins the clinic is served on (CSRF, CORS, bucket URL)
//   - secrets.env generated once on the host and never regenerated
package stack

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/url"
	"slices"
	"strings"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
)

// kitFiles maps deployment kit paths to their flat upload names.
var kitFiles = []struct{ src, dst string }{
	{"docker-compose.yml", "compose.yml"},
	{"backend.env", "backend.env"},
	{"Caddyfile", "Caddyfile"},
	{"minio/entrypoint.sh", "minio-entrypoint.sh"},
	{"setup/index.html", "setup.html"},
}

// Generated file names in the config area.
const (
	StackEnv   = "stack.env"
	ClinicEnv  = "clinic.env"
	SecretsEnv = "secrets.env"
)

// RequiredRoles are the images the stack runs.
var RequiredRoles = []protocol.ImageRole{
	protocol.ImageRoleBackend, protocol.ImageRoleFrontend, protocol.ImageRoleCaddy,
	protocol.ImageRolePostgres, protocol.ImageRoleRedis, protocol.ImageRoleSilo,
}

// homeAccessOrigin is where Tailscale serve exposes the clinic.
const homeAccessOrigin = "https://*.ts.net:8443"

// Image is one image of the stack, pinned by digest. Tarball, when set, is a
// "docker save" archive to load; otherwise the image is pulled.
type Image struct {
	Role    protocol.ImageRole
	Repo    string
	Digest  protocol.Digest
	Tarball string
}

// Ref is the digest-pinned reference compose runs.
func (i Image) Ref() string { return i.Repo + "@" + string(i.Digest) }

// Config is everything Render needs besides the kit.
type Config struct {
	// Host is the clinic's mDNS name, e.g. "care.local".
	Host string
	// Origins are further https origins the clinic is served on.
	Origins []string
	Images  []Image
	Secrets Secrets
}

// Render returns the config area files: kit files under their upload names
// plus the generated ones.
func Render(kit fs.FS, c Config) (map[string][]byte, error) {
	if c.Host == "" || strings.ContainsAny(c.Host, " \t\r\n\"'$/") {
		return nil, fmt.Errorf("invalid clinic host %q", c.Host)
	}
	if err := c.Secrets.validate(); err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	for _, f := range kitFiles {
		b, err := fs.ReadFile(kit, f.src)
		if err != nil {
			return nil, fmt.Errorf("deployment kit: %w", err)
		}
		files[f.dst] = b
	}
	images := map[protocol.ImageRole]Image{}
	for _, img := range c.Images {
		images[img.Role] = img
	}
	var env strings.Builder
	for _, role := range RequiredRoles {
		img, ok := images[role]
		if !ok {
			return nil, fmt.Errorf("no %s image", role)
		}
		if !img.Digest.Valid() {
			return nil, fmt.Errorf("%s image: invalid digest %q", role, img.Digest)
		}
		fmt.Fprintf(&env, "IMAGE_%s=%s\n", strings.ToUpper(string(role)), img.Ref())
	}
	fmt.Fprintf(&env, "CARE_HOST=%s\n", c.Host)
	fmt.Fprintf(&env, "CADDY_CONFIG_SHA=%s\n", digest(files["Caddyfile"], files["setup.html"]))
	fmt.Fprintf(&env, "MINIO_CONFIG_SHA=%s\n", digest(files["minio-entrypoint.sh"]))
	files[StackEnv] = []byte(env.String())

	clinic, err := clinicEnv(c.Host, c.Origins)
	if err != nil {
		return nil, err
	}
	files[ClinicEnv] = clinic
	files[SecretsEnv] = c.Secrets.Env()
	return files, nil
}

// clinicEnv lists every origin the clinic is served on.
func clinicEnv(host string, extra []string) ([]byte, error) {
	origins := []string{"https://" + host, "https://localhost"}
	for _, o := range extra {
		if u, err := url.Parse(o); err != nil || u.Scheme != "https" || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.User != nil || strings.ContainsAny(o, " \t\r\n") {
			return nil, fmt.Errorf("invalid origin %q: want https://host[:port]", o)
		}
		if !slices.Contains(origins, o) {
			origins = append(origins, o)
		}
	}
	cors, err := json.Marshal(origins)
	if err != nil {
		return nil, err
	}
	csrf, err := json.Marshal(append(slices.Clone(origins), homeAccessOrigin))
	if err != nil {
		return nil, err
	}
	return fmt.Appendf(nil, "CSRF_TRUSTED_ORIGINS=%s\nCORS_ALLOWED_ORIGINS=%s\nBUCKET_EXTERNAL_ENDPOINT=https://%s\n", csrf, cors, host), nil
}

func digest(parts ...[]byte) string {
	h := sha256.New()
	for _, p := range parts {
		_, _ = fmt.Fprintf(h, "%d:", len(p))
		_, _ = h.Write(p)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}
