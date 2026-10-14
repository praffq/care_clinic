package clinic

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path"
	"strings"
)

const caddyRootPath = "/data/caddy/pki/authorities/local/root.crt"

// agentScratchDir holds short-lived guest copies that are fetched and removed.
const agentScratchDir = "/var/lib/care/scratch"

func (e *Clinic) caddyRootPEM() string {
	if out, err := e.capture("compose", "exec", "-T", "caddy",
		"cat", caddyRootPath); err == nil && strings.Contains(out, "BEGIN CERTIFICATE") {
		return out
	}
	// A stopped caddy container can still be copied from. The copy lands in a
	// unique guest scratch file which is fetched explicitly and then removed.
	f, err := os.CreateTemp("", "care-root-*.crt")
	if err != nil {
		return ""
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if err := f.Close(); err != nil {
		return ""
	}
	id := make([]byte, 12)
	if _, err := rand.Read(id); err != nil {
		return ""
	}
	guest := path.Join(agentScratchDir, hex.EncodeToString(id), "root.crt")
	run := e.Runner()
	client, ctx := run.AgentClient(), run.Context()
	if err := client.Put(ctx, tmp, guest); err != nil {
		return ""
	}
	defer func() {
		_ = client.Remove(ctx, guest)
		_ = client.Remove(ctx, path.Dir(guest))
	}()
	if _, err := e.capture("compose", "cp", "caddy:"+caddyRootPath, guest); err != nil {
		return ""
	}
	if err := client.Get(ctx, guest, tmp); err != nil {
		return ""
	}
	b, err := os.ReadFile(tmp)
	if err != nil || !strings.Contains(string(b), "BEGIN CERTIFICATE") {
		return ""
	}
	return string(b)
}
