package release

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var workflowNames = []string{"care-images.yml", "engine.yml", "desktop.yml", "publish.yml"}

func workflow(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile("../../../.github/workflows/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func requireText(t *testing.T, data string, required ...string) {
	t.Helper()
	for _, text := range required {
		if !strings.Contains(data, text) {
			t.Errorf("release pipeline is missing %q", text)
		}
	}
}

func TestReleaseWorkflowValidatesIdentity(t *testing.T) {
	files, err := filepath.Glob("../../../.github/workflows/*.yml")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != len(workflowNames) {
		t.Fatalf("expected exactly four release workflows, found %v", files)
	}
	publish := workflow(t, "publish.yml")
	requireText(t, publish,
		"versions.toml", "env-defaults.yml", "schedule:", "workflow_dispatch:",
		"cancel-in-progress: false", "name: CI", "if: always()",
		"ci/releasectl plan --root .", "--plan plan.json",
		"ci/releasectl verify --manifest previous.json --signature previous.json.sig",
		"environment: release-signing", "secrets.UPDATES_SIGNING_KEY",
		"public/updates.json", "public/updates.json.sig", "path: public",
		"needs: [plan, required]",
	)
	if strings.Contains(publish, "pull_request_target") {
		t.Fatal("untrusted PR code must not run in a privileged event")
	}
	requireText(t, publish, "github.event_name != 'pull_request'", "github.event_name != 'merge_group'",
		"github.ref == 'refs/heads/main'")
}

func TestCIRequiredGateRejectsIncompleteChecks(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("Python is not installed")
	}
	cmd := exec.Command("python3", "-m", "unittest", "discover", "-s", ".github/scripts", "-p", "test_*.py")
	cmd.Dir = "../../.."
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("pipeline safety tests failed: %v\n%s", err, output)
	}
}

func TestCIIncludesGuestAndQEMUPackagingRegressions(t *testing.T) {
	requireText(t, workflow(t, "engine.yml"),
		"working-directory: vm/agent", "go build ./...", "go vet ./...", "go test -race -count=1 ./...",
		"care-engine-agent-linux-$ARCH", "python3 -m unittest discover -s vm",
		`printf '%s\n' "$VERSION" > vm/VERSION`, "nix build", "inputs.version",
	)
	requireText(t, workflow(t, "desktop.yml"),
		"GOOS=windows GOARCH=amd64 go vet ./...",
		"name: Vet on the native desktop host",
		"go test -race -count=1", "npm run build",
	)
	if strings.Contains(workflow(t, "desktop.yml"), "GOOS=darwin") {
		t.Fatal("Darwin vet requires the native macOS runner and its cgo frameworks")
	}
}

func TestThirdPartyActionsUseImmutableSHAs(t *testing.T) {
	for _, name := range workflowNames {
		data := workflow(t, name)
		for _, match := range regexp.MustCompile(`(?m)^\s*(?:-\s+)?uses:\s+([^#\s]+)`).FindAllStringSubmatch(data, -1) {
			ref := match[1]
			if strings.HasPrefix(ref, "./") {
				continue
			}
			parts := strings.Split(ref, "@")
			if len(parts) != 2 || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(parts[1]) {
				t.Errorf("%s uses mutable action ref %q", name, ref)
			}
		}
	}
}

func TestReleaseManifestChecksumsEveryAsset(t *testing.T) {
	data, err := os.ReadFile("../../../.github/scripts/pipeline.py")
	if err != nil {
		t.Fatal(err)
	}
	requireText(t, string(data), `"sha256": digest(path)`, `"size": path.stat().st_size`,
		`f"{digest(p)}  {p.name}\n"`, "SHA256SUMS",
		"existing immutable asset differs", "existing immutable release has a different asset set")
	if strings.Contains(string(data), "--clobber") {
		t.Fatal("published assets must never be overwritten")
	}
}

func TestMacSigningRetainsExistingConfiguration(t *testing.T) {
	requireText(t, workflow(t, "desktop.yml"),
		"secrets.MACOS_CERT_P12", "secrets.MACOS_CERT_PASSWORD", "secrets.MACOS_SIGN_IDENTITY",
		"secrets.APPSTORE_PRIVATE_KEY", "secrets.APPSTORE_KEY_ID", "secrets.APPSTORE_ISSUER_ID",
		"secrets.SIGNPATH_API_TOKEN", "Get-AuthenticodeSignature", "SIGNED_UNINSTALLER=",
		"SIGN_MACOS: ${{ secrets.MACOS_CERT_P12 != '' }}",
		"wait-for-completion-timeout-in-seconds: \"3600\"",
		"output-artifact-directory: signed-app",
		"output-artifact-directory: signed-installer",
	)
	if strings.Contains(workflow(t, "desktop.yml"), "environment:") {
		t.Fatal("native signing must retain its existing repository credential scope")
	}
	data, err := os.ReadFile("../../../.github/scripts/package-macos.sh")
	if err != nil {
		t.Fatal(err)
	}
	requireText(t, string(data), "--options runtime", `notarize "$dmg"`,
		`xcrun stapler validate "$app"`, `xcrun stapler validate "$dmg"`,
		"trap cleanup EXIT", `keychain="$RUNNER_TEMP/signing.keychain-db"`,
		"security list-keychains -d user -s", "security find-identity -v -p codesigning",
		`if [ "$SIGN_MACOS" = "true" ]`, "--no-wait --output-format json",
		"for attempt in 1 2 3 4 5 6", `notarytool wait "$id" "${notary[@]}" --timeout 2h`,
		`notarytool info "$id"`, `notarytool log "$id"`,
	)
	if strings.Contains(string(data), "--entitlements") || strings.Contains(string(data), "--wait --timeout") {
		t.Fatal("native macOS signing arguments must not change during pipeline redesign")
	}
}

func TestUnsignedReleaseCannotBypassFailedSigning(t *testing.T) {
	requireText(t, workflow(t, "desktop.yml"),
		"!cancelled() && inputs.build && inputs.publish",
		"needs.signing.result == 'success'",
		"needs.sign-windows-installer.result == 'success'",
		"needs.sign-windows-installer.result == 'skipped'",
		"SignPath is partially configured",
	)
}

func TestWindowsInstallerRejectsDamagedDownloads(t *testing.T) {
	data, err := os.ReadFile("../../build/windows/installer/project.nsi")
	if err != nil {
		t.Fatal(err)
	}
	var checks []string
	for _, line := range strings.Split(string(data), "\n") {
		if fields := strings.Fields(line); len(fields) > 0 && strings.EqualFold(fields[0], "CRCCheck") {
			checks = append(checks, strings.Join(fields, " "))
		}
	}
	if len(checks) != 1 || checks[0] != "CRCCheck force" {
		t.Fatalf("installer must verify itself (CRCCheck force), found %q", checks)
	}
}
