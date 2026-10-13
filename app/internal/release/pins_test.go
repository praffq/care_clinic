package release

import (
	"os"
	"strings"
	"testing"
)

func deploymentPins(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("../../../deployments/.env")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestDeploymentNamesAReleaseLine(t *testing.T) {
	data := string(deploymentPins(t))
	pins, err := Load([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if pins.LinesURL == "" || pins.DefaultLine == "" {
		t.Fatal("deployment names no release line to follow")
	}
	digest := "ghcr.io/ohcnetwork/care-clinic-caddy@sha256:" + strings.Repeat("a", 64)
	for _, tt := range []struct {
		name, from, to string
		ok             bool
	}{
		{"http lines", "CARE_LINES_URL=" + pins.LinesURL, "CARE_LINES_URL=http://example.com/lines.json", false},
		{"bad line", "CARE_DEFAULT_LINE=" + pins.DefaultLine, "CARE_DEFAULT_LINE=../x", false},
		{"http QEMU", "QEMU_WINDOWS_URL=" + pins.QEMUWindowsURL, "QEMU_WINDOWS_URL=http://example.com/qemu.exe", false},
		{"http extractor", "SEVENZIP_WINDOWS_URL=" + pins.SevenZipWindowsURL, "SEVENZIP_WINDOWS_URL=http://example.com/7zip.exe", false},
		{"missing bootstrap", "SEVENZIP_BOOTSTRAP_URL=" + pins.SevenZipBootstrapURL, "SEVENZIP_BOOTSTRAP_URL=", false},
		{"bad bootstrap checksum", "SEVENZIP_BOOTSTRAP_SHA256=" + pins.SevenZipBootstrapSHA256, "SEVENZIP_BOOTSTRAP_SHA256=unknown", false},
		{"prebuilt caddy", "CADDY_WAF_PREBUILT=" + pins.CaddyWafPrebuilt, "CADDY_WAF_PREBUILT=" + digest, true},
		{"tagged caddy", "CADDY_WAF_PREBUILT=" + pins.CaddyWafPrebuilt, "CADDY_WAF_PREBUILT=caddy:latest", false},
		{"tagged backup", "BACKUP_PREBUILT=" + pins.BackupPrebuilt, "BACKUP_PREBUILT=postgres:17", false},
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
