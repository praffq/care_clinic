package release

import (
	"os"
	"strings"
	"testing"
)

func repoFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile("../../../" + path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// deploymentPins is the .env a build of this checkout embeds.
func deploymentPins(t *testing.T) []byte {
	t.Helper()
	data, err := StageEnv(repoFile(t, ConfigFile), repoFile(t, "deployments/.env"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestDeploymentPinsOneCARERelease(t *testing.T) {
	data := string(deploymentPins(t))
	pins, err := Load([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if pins.CAREVersion == "" || pins.UpdatesURL == "" {
		t.Fatal("deployment pins no CARE release or update source")
	}
	if pins.BackendImage == "" || pins.CaddyWafImage == "" || pins.CareVMBaseURL == "" {
		t.Fatal("staged pins are incomplete")
	}
	digest := "ghcr.io/ohcnetwork/care-clinic-caddy@sha256:" + strings.Repeat("a", 64)
	for _, tt := range []struct {
		name, from, to string
		ok             bool
	}{
		{"http updates", "CARE_UPDATES_URL=" + pins.UpdatesURL, "CARE_UPDATES_URL=http://example.com/manifest.json", false},
		{"bad CARE version", "CARE_VERSION=" + pins.CAREVersion, "CARE_VERSION=3.1", false},
		{"missing CARE version", "CARE_VERSION=" + pins.CAREVersion, "CARE_VERSION=", false},
		{"retired line", "CARE_VERSION=" + pins.CAREVersion, "CARE_VERSION=" + pins.CAREVersion + "\nCARE_DEFAULT_LINE=3", false},
		{"retired lines url", "CARE_VERSION=" + pins.CAREVersion, "CARE_VERSION=" + pins.CAREVersion + "\nCARE_LINES_URL=https://example.com/lines.json", false},
		{"http QEMU", "QEMU_WINDOWS_URL=" + pins.QEMUWindowsURL, "QEMU_WINDOWS_URL=http://example.com/qemu.exe", false},
		{"http extractor", "SEVENZIP_WINDOWS_URL=" + pins.SevenZipWindowsURL, "SEVENZIP_WINDOWS_URL=http://example.com/7zip.exe", false},
		{"missing bootstrap", "SEVENZIP_BOOTSTRAP_URL=" + pins.SevenZipBootstrapURL, "SEVENZIP_BOOTSTRAP_URL=", false},
		{"bad bootstrap checksum", "SEVENZIP_BOOTSTRAP_SHA256=" + pins.SevenZipBootstrapSHA256, "SEVENZIP_BOOTSTRAP_SHA256=unknown", false},
		{"prebuilt caddy", "CARE_VERSION=" + pins.CAREVersion, "CARE_VERSION=" + pins.CAREVersion + "\nCADDY_WAF_PREBUILT=" + digest, true},
		{"tagged caddy", "CARE_VERSION=" + pins.CAREVersion, "CARE_VERSION=" + pins.CAREVersion + "\nCADDY_WAF_PREBUILT=caddy:latest", false},
		{"tagged backup", "CARE_VERSION=" + pins.CAREVersion, "CARE_VERSION=" + pins.CAREVersion + "\nBACKUP_PREBUILT=postgres:17", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			changed := strings.Replace(data, tt.from+"\n", tt.to+"\n", 1)
			if changed == data {
				t.Fatalf("fixture %q not found", tt.from)
			}
			got, err := Load([]byte(changed))
			if (err == nil) != tt.ok {
				t.Fatalf("Load() error = %v", err)
			}
			if tt.ok && got.CaddyWafPrebuilt != digest {
				t.Fatal("prebuilt image was not read")
			}
		})
	}
}

func TestLoadValidatesVersions(t *testing.T) {
	data := string(deploymentPins(t))
	pins, err := Load([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"0.0", "v0.1.0", "0.1.0-release", "01.1.0"} {
		t.Run(version, func(t *testing.T) {
			invalid := strings.Replace(data, "CARE_CLINIC_VERSION="+pins.AppVersion, "CARE_CLINIC_VERSION="+version, 1)
			if _, err := Load([]byte(invalid)); err == nil {
				t.Fatal("invalid version was accepted")
			}
		})
	}
}

func TestLoadSourceMatchesConfig(t *testing.T) {
	config := repoFile(t, ConfigFile)
	c, err := ReadConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	pins, err := LoadSource(config, repoFile(t, "deployments/.env"))
	if err != nil {
		t.Fatal(err)
	}
	for name, pair := range map[string][2]string{
		"desktop":  {pins.AppVersion, c.Desktop.Version},
		"engine":   {pins.CareVMVersion, c.Engine.Version},
		"care":     {pins.CAREVersion, c.Care.Version},
		"updates":  {pins.UpdatesURL, c.Desktop.UpdatesURL},
		"postgres": {pins.PostgresImage, c.SupportImages["postgres"]},
		"redis":    {pins.RedisImage, c.SupportImages["redis"]},
		"caddy":    {pins.CaddyImage, c.SupportImages["caddy"]},
		"silo":     {pins.MinioImage, c.SupportImages["silo"]},
		"coraza":   {pins.CorazaVersion, c.Caddy.Coraza},
		"qemu":     {pins.QEMUWindowsURL, c.Tools["qemu_windows_x86_64"].URL},
		"vm":       {pins.CareVMBaseURL + "/care-engine-" + c.Engine.Version + "-linux-arm64.tar.gz", c.Tools["care_engine_aarch64"].URL},
	} {
		if pair[0] != pair[1] {
			t.Errorf("%s: staged %q, versions.toml %q", name, pair[0], pair[1])
		}
	}
}
