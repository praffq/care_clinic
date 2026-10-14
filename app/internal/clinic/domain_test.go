package clinic

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/envconfig"
)

func writeDomainFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readDomainFile(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func domainClinic(t *testing.T) *Clinic {
	t.Helper()
	dir := t.TempDir()
	defaults, err := os.ReadFile("../../../deployments/env-defaults.yml")
	if err != nil {
		t.Fatal(err)
	}
	writeDomainFile(t, dir, "env-defaults.yml", string(defaults))
	if err := envconfig.EnsureSecrets(dir); err != nil {
		t.Fatal(err)
	}
	return &Clinic{InstallDir: dir, MDNSName: "first"}
}

func TestApplyDomainPreservesOverridesAndUpdatesDerivedValues(t *testing.T) {
	e := domainClinic(t)
	overrides := "PLUGIN_API='https://scanner.local/api'\nPLUGIN_CLINIC='https://example.local/api'\n"
	writeDomainFile(t, e.InstallDir, "backend.overrides.env", overrides)
	caddy, err := os.ReadFile("../../../deployments/Caddyfile")
	if err != nil {
		t.Fatal(err)
	}
	writeDomainFile(t, e.InstallDir, "Caddyfile", string(caddy))
	writeDomainFile(t, e.InstallDir, "setup/index.html", `<a id="clinic-address" href="https://example.local">Open</a>`)
	for _, host := range []string{"first", "first", "renamed"} {
		e.MDNSName = host
		if err := e.ApplyDomain(); err != nil {
			t.Fatal(err)
		}
		if got := readDomainFile(t, e.InstallDir, "Caddyfile"); got != strings.ReplaceAll(string(caddy), "example.local", host+".local") {
			t.Fatal("proxy configuration did not update the clinic host")
		}
		if got := readDomainFile(t, e.InstallDir, "backend.overrides.env"); got != overrides {
			t.Fatal("hostname edit changed explicit overrides")
		}
		be, err := envconfig.Read(filepath.Join(e.InstallDir, "backend.env"))
		if err != nil || be["BUCKET_EXTERNAL_ENDPOINT"] != "https://"+host+".local" || be["PLUGIN_CLINIC"] != "https://example.local/api" {
			t.Fatal("generated backend lost derived hostname or plugin settings")
		}
		fe, err := envconfig.Read(filepath.Join(e.InstallDir, "frontend.env"))
		if err != nil || fe["REACT_CARE_API_URL"] != "https://"+host+".local" {
			t.Fatal("frontend hostname not regenerated")
		}
	}
}

func TestApplyDomainHandlesIncompleteProxySetup(t *testing.T) {
	e := domainClinic(t)
	if err := e.ApplyDomain(); err != nil {
		t.Fatal(err)
	}
	writeDomainFile(t, e.InstallDir, "Caddyfile", "example.local:443 {\n\ttls internal\n\timport bootstrap\n\timport site\n}\n")
	e.MDNSName = "retry"
	if err := e.ApplyDomain(); err != nil {
		t.Fatal(err)
	}
	if got := readDomainFile(t, e.InstallDir, "Caddyfile"); !strings.HasPrefix(got, "retry.local:443") {
		t.Fatal("partial setup retained old hostname")
	}
}

func TestApplyDomainRejectsInvalidInputsBeforeWriting(t *testing.T) {
	for _, failure := range []string{"label", "read", "parse", "secrets"} {
		t.Run(failure, func(t *testing.T) {
			e := domainClinic(t)
			writeDomainFile(t, e.InstallDir, "Caddyfile", "example.local:443 {}\n")
			switch failure {
			case "label":
				e.MDNSName = "not a label"
			case "read":
				if err := os.Remove(filepath.Join(e.InstallDir, "backend.overrides.env")); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(e.InstallDir, "backend.overrides.env"), 0o700); err != nil {
					t.Fatal(err)
				}
			case "parse":
				writeDomainFile(t, e.InstallDir, "backend.overrides.env", "PLUGIN=\"unterminated\n")
			case "secrets":
				writeDomainFile(t, e.InstallDir, "secrets.env", "")
			}
			if err := e.ApplyDomain(); err == nil {
				t.Fatal("invalid input was accepted")
			}
			if got := readDomainFile(t, e.InstallDir, "Caddyfile"); got != "example.local:443 {}\n" {
				t.Fatal("proxy changed before input validation")
			}
		})
	}
}
