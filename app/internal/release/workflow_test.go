package release

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
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
		"ci/releasectl verify --manifest previous.json", "--signature previous.json.sig",
		"secrets.UPDATES_SIGNING_KEY", "--unsigned-development",
		"vars.UPDATES_SIGNING_ENABLED",
		"environment: ${{ needs.plan.outputs.update_trust == 'signed' && 'release-signing' || 'unsigned-development' }}",
		"pipeline.py publication", "path: public",
		"needs: [plan, required]",
	)
	if strings.Contains(publish, "pull_request_target") {
		t.Fatal("untrusted PR code must not run in a privileged event")
	}
	planJob := strings.Split(publish, "\n  care:\n")[0]
	if strings.Contains(planJob, "secrets.UPDATES_SIGNING_KEY") || strings.Contains(planJob, "environment:") {
		t.Fatal("planning must select trust without accessing the protected key or requiring approval")
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

func TestCAREImagesBuildBothArchitecturesNatively(t *testing.T) {
	data := workflow(t, "care-images.yml")
	requireText(t, data, "arch: amd64", "runner: ubuntu-24.04",
		"arch: arm64", "runner: ubuntu-24.04-arm", "runs-on: ${{ matrix.runner }}",
		"IMAGE_ARCH: ${{ matrix.arch }}", "fail-fast: false",
		"name: output-images-amd64", "name: output-images-arm64",
		"build-images.py --assemble", "name: output-images", "needs: build")
	if strings.Contains(data, "setup-qemu-action") {
		t.Fatal("CARE's native bundler must not execute under QEMU in image CI")
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

func TestReusableWorkflowPermissionCeilingsIncludeSkippedJobs(t *testing.T) {
	type job struct {
		Uses        string            `yaml:"uses"`
		Permissions map[string]string `yaml:"permissions"`
	}
	type definition struct {
		Permissions map[string]string `yaml:"permissions"`
		Jobs        map[string]job    `yaml:"jobs"`
	}
	definitions := map[string]definition{}
	for _, name := range workflowNames {
		var d definition
		if err := yaml.Unmarshal([]byte(workflow(t, name)), &d); err != nil {
			t.Fatal(err)
		}
		definitions[name] = d
	}
	rank := map[string]int{"none": 0, "read": 1, "write": 2}
	seen := map[string]bool{}
	var visit func(string, map[string]bool, int)
	visit = func(name string, ancestors map[string]bool, depth int) {
		t.Helper()
		if depth > 10 || ancestors[name] {
			t.Fatalf("reusable workflow nesting exceeds ten levels or contains a cycle at %s", name)
		}
		ancestors[name] = true
		defer delete(ancestors, name)
		for _, call := range definitions[name].Jobs {
			if strings.HasPrefix(call.Uses, "./.github/workflows/") {
				target := strings.TrimPrefix(call.Uses, "./.github/workflows/")
				seen[target] = true
				visit(target, ancestors, depth+1)
			}
		}
	}
	visit("publish.yml", map[string]bool{}, 1)
	if len(seen) > 50 {
		t.Fatal("publication exceeds GitHub's fifty unique reusable workflow limit")
	}
	for name, caller := range definitions {
		for id, call := range caller.Jobs {
			if !strings.HasPrefix(call.Uses, "./.github/workflows/") {
				continue
			}
			target := strings.TrimPrefix(call.Uses, "./.github/workflows/")
			callee, ok := definitions[target]
			if !ok {
				t.Fatalf("%s/%s calls an untested local workflow %s", name, id, target)
			}
			ceiling := call.Permissions
			if ceiling == nil {
				ceiling = caller.Permissions
			}
			for nestedID, nested := range callee.Jobs {
				requested := nested.Permissions
				if requested == nil {
					requested = callee.Permissions
				}
				// GitHub validates declarations regardless of if/phase.
				for scope, permission := range requested {
					if rank[permission] > rank[ceiling[scope]] {
						t.Errorf("%s/%s allows %s:%s but nested %s/%s requests %s:%s",
							name, id, scope, ceiling[scope], target, nestedID, scope, permission)
					}
				}
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
		"SIGN_MACOS: ${{ needs.signing.outputs.macos }}",
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
		"pipeline.py signing",
	)
	data := workflow(t, "desktop.yml")
	release := strings.Split(data, "\n  release:\n")[1]
	requireText(t, release, "if: ${{ !cancelled() && needs.package.result == 'success' }}")
	requireText(t, data, "release.updateTrust=$UPDATE_TRUST", "signed|unsigned-development")
	requireText(t, workflow(t, "publish.yml"), "update_trust: ${{ needs.plan.outputs.update_trust }}",
		"pipeline.py reuse-engine", "name: output-engine-reused", "needs: [plan, engine, desktop]",
		"if: ${{ !cancelled() && needs.plan.result == 'success' && needs.engine.result == 'success' && needs.desktop.result == 'success' }}")
	requireText(t, workflow(t, "engine.yml"), "if: inputs.build", "PUBLISH: ${{ inputs.publish }}",
		`if [ "$PUBLISH" = true ]; then publish+=(--publish); fi`, "name: output-engine")
	if strings.Count(data, "run: python3 .github/scripts/pipeline.py bootstrap") != 1 {
		t.Fatal("native build staging must resolve measured bootstrap hashes")
	}
	requireText(t, data, "name: desktop-bootstrap-config", "pattern: output-engine*")
	packageJob := strings.Split(strings.Split(data, "\n  package:\n")[1], "\n  release:\n")[0]
	requireText(t, packageJob, "name: desktop-bootstrap-config", "cp versions.toml release-assets/versions.toml")
}

func TestDesktopChecksStartAlongsideEngineWithoutArtifactPolling(t *testing.T) {
	type job struct {
		Needs any              `yaml:"needs"`
		If    string           `yaml:"if"`
		With  map[string]any   `yaml:"with"`
		Steps []map[string]any `yaml:"steps"`
	}
	parse := func(name string) map[string]job {
		t.Helper()
		var data struct {
			Jobs map[string]job `yaml:"jobs"`
		}
		if err := yaml.Unmarshal([]byte(workflow(t, name)), &data); err != nil {
			t.Fatal(err)
		}
		return data.Jobs
	}
	publish := parse("publish.yml")
	for _, name := range []string{"care", "engine", "desktop"} {
		if publish[name].Needs != "plan" {
			t.Fatalf("%s must start immediately after planning, got needs=%v", name, publish[name].Needs)
		}
	}
	if publish["desktop"].With["phase"] != "checks" || publish["desktop-package"].With["phase"] != "package" {
		t.Fatal("desktop checks and installer packaging must use separate workflow calls")
	}
	packagingNeeds, _ := json.Marshal(publish["desktop-package"].Needs)
	if string(packagingNeeds) != `["plan","engine","desktop"]` {
		t.Fatalf("packaging must wait for engine measurements and desktop checks: %s", packagingNeeds)
	}
	requireText(t, workflow(t, "publish.yml"), "needs: [plan, care, engine, desktop, desktop-package]")
	desktop := parse("desktop.yml")
	for _, name := range []string{"lint", "go", "frontend"} {
		if desktop[name].Needs != nil || desktop[name].If != "inputs.phase == 'checks'" {
			t.Fatalf("%s must run independently in checks phase", name)
		}
		for _, step := range desktop[name].Steps {
			serialized, _ := json.Marshal(step)
			if strings.Contains(string(serialized), "download-artifact") || strings.Contains(string(serialized), "pipeline.py bootstrap") {
				t.Fatalf("%s must not wait for engine artifacts", name)
			}
		}
	}
	if desktop["native"].Needs != nil || desktop["native"].If != "inputs.phase == 'package' && inputs.build" {
		t.Fatal("native build must run only in the dependency-gated package call")
	}
	if desktop["signing"].Needs != "native" {
		t.Fatal("signing must wait for native build success without skipped check ancestors")
	}
	requireText(t, workflow(t, "desktop.yml"), "npm run build:frontend", "pattern: output-engine*",
		"name: desktop-frontend", "needs: [lint, go, frontend]")
	data := repoFile(t, "app/frontend/package.json")
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		t.Fatal(err)
	}
	requireText(t, pkg.Scripts["build"], "stage-install.mjs", "npm run build:frontend")
	requireText(t, pkg.Scripts["build:frontend"], "check-bindings.mjs", "tsc", "vite build", "prune-fonts.mjs")
	if strings.Contains(pkg.Scripts["build:frontend"], "stage-install") {
		t.Fatal("frontend-only validation must not stage unresolved engine pins")
	}
	gate, ok := desktop["required"].Steps[0]["run"].(string)
	if !ok || desktop["required"].If != "always()" {
		t.Fatal("both desktop phases need an always-run status gate, including no-build runs")
	}
	for _, tc := range []struct {
		phase, build string
		expected     map[string]map[string]string
	}{
		{"checks", "false", map[string]map[string]string{
			"lint": {"result": "success"}, "go": {"result": "success"},
			"frontend": {"result": "success"},
		}},
		{"package", "true", map[string]map[string]string{
			"lint": {"result": "skipped"}, "go": {"result": "skipped"},
			"frontend": {"result": "skipped"},
		}},
		{"package", "false", map[string]map[string]string{
			"lint": {"result": "skipped"}, "go": {"result": "skipped"},
			"frontend": {"result": "skipped"},
		}},
	} {
		run := func(wantSuccess bool) {
			t.Helper()
			results, err := json.Marshal(tc.expected)
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("bash", "-c", gate)
			cmd.Env = append(os.Environ(), "PHASE="+tc.phase, "BUILD="+tc.build, "RESULTS="+string(results))
			if output, err := cmd.CombinedOutput(); (err == nil) != wantSuccess {
				t.Fatalf("phase=%s build=%s results=%s error=%v output=%s", tc.phase, tc.build, results, err, output)
			}
		}
		run(true)
		for _, status := range tc.expected {
			old := status["result"]
			for _, invalid := range []string{"success", "failure", "cancelled", "skipped"} {
				if invalid == old {
					continue
				}
				status["result"] = invalid
				run(false)
			}
			status["result"] = old
		}
	}
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
