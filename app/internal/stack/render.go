// Package stack installs and drives the CARE compose stack inside the engine
// VM. Everything goes through typed agent ops relayed by the host service;
// nothing here runs docker on the host.
//
// The deployment kit (the repo's deployments/ directory) is uploaded mostly
// verbatim to the agent's config file area, plus generated files:
//
//   - stack.env   compose interpolation: digest-pinned images, the host name
//     and hashes of bind-mounted files (so changes recreate the container)
//   - clinic.env  origins the clinic is served on (CSRF, CORS, bucket URL)
//   - secrets.env generated once on the host and never regenerated
//   - plugins.env ADDITIONAL_PLUGS, the backend plugins CARE loads (it may
//     hold plugin secrets)
//   - backup-cert.pem the certificate backups are encrypted to, generated
//     once with its recovery key, which stays on the host
package stack

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/url"
	"slices"
	"strings"

	"github.com/compose-spec/compose-go/v2/dotenv"
	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
	"github.com/ohcnetwork/care_desktop/app/internal/plugins"
)

// kitFiles maps deployment kit paths to their flat upload names.
var kitFiles = []struct{ src, dst string }{
	{"docker-compose.yml", "compose.yml"},
	{"backend.env", "backend.env"},
	{"Caddyfile", "Caddyfile"},
	{"minio/entrypoint.sh", "minio-entrypoint.sh"},
	{"setup/index.html", "setup.html"},
	{"scripts/backup.sh", "backup.sh"},
}

// Generated file names in the config area.
const (
	StackEnv   = "stack.env"
	ClinicEnv  = "clinic.env"
	SecretsEnv = "secrets.env"
	PluginsEnv = "plugins.env"
)

// RequiredRoles are the images the stack runs.
var RequiredRoles = []protocol.ImageRole{
	protocol.ImageRoleBackend, protocol.ImageRoleFrontend, protocol.ImageRoleCaddy,
	protocol.ImageRolePostgres, protocol.ImageRoleRedis, protocol.ImageRoleSilo, protocol.ImageRoleBackup,
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
	// BackupCert is the PEM certificate backups are encrypted to.
	BackupCert []byte
	// BackupTZ is the POSIX TZ the backup container's midnight is in; empty
	// means UTC.
	BackupTZ string
	// AdditionalPlugs is CARE's ADDITIONAL_PLUGS JSON (plugins.AdditionalPlugs);
	// empty means the catalog defaults. The backend image must have every
	// plugin it lists installed.
	AdditionalPlugs string
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
	if _, err := ParseBackupCert(c.BackupCert); err != nil {
		return nil, err
	}
	tz := c.BackupTZ
	if tz == "" {
		tz = "UTC0"
	}
	if strings.ContainsAny(tz, " \t\r\n\"'$") {
		return nil, fmt.Errorf("invalid backup TZ %q", tz)
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
	fmt.Fprintf(&env, "BACKUP_TZ=%s\n", tz)
	fmt.Fprintf(&env, "BACKUP_CONFIG_SHA=%s\n", digest(files["backup.sh"], c.BackupCert))
	files[StackEnv] = []byte(env.String())
	files[BackupCert] = c.BackupCert

	clinic, err := clinicEnv(c.Host, c.Origins)
	if err != nil {
		return nil, err
	}
	files[ClinicEnv] = clinic
	files[SecretsEnv] = c.Secrets.Env()
	pe, err := pluginsEnv(c.AdditionalPlugs)
	if err != nil {
		return nil, err
	}
	files[PluginsEnv] = pe
	return files, nil
}

// pluginsEnv renders plugins.env. The value is single-quoted so compose
// takes it literally (no interpolation, no comments); a quote inside JSON
// strings is written as its \u0027 escape.
func pluginsEnv(plugs string) ([]byte, error) {
	if plugs == "" {
		var err error
		if plugs, err = plugins.AdditionalPlugs(plugins.Defaults()); err != nil {
			return nil, err
		}
	}
	var v []map[string]any
	if err := json.Unmarshal([]byte(plugs), &v); err != nil {
		return nil, fmt.Errorf("ADDITIONAL_PLUGS is not a JSON list: %w", err)
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(plugs)); err != nil {
		return nil, err
	}
	val := strings.ReplaceAll(buf.String(), "'", `\u0027`)
	return []byte("ADDITIONAL_PLUGS='" + val + "'\n"), nil
}

// ParsePluginsEnv returns the ADDITIONAL_PLUGS value of a plugins.env.
func ParsePluginsEnv(data []byte) (string, error) {
	env, err := dotenv.UnmarshalBytesWithLookup(data, nil)
	if err != nil {
		return "", err
	}
	v, ok := env["ADDITIONAL_PLUGS"]
	if !ok {
		return "", fmt.Errorf("%s: no ADDITIONAL_PLUGS", PluginsEnv)
	}
	return v, nil
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
