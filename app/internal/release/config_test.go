package release

import (
	"strings"
	"testing"

	"github.com/Masterminds/semver/v3"
)

func TestRepositoryConfigIsValid(t *testing.T) {
	c, err := ReadConfig(repoFile(t, ConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	if c.Schema != ConfigSchema || c.Desktop.Version != "0.0.1" || c.Engine.Version != "0.1.3" || c.Care.Version != "3.1.0" {
		t.Fatalf("unexpected config: schema %d, desktop %s, engine %s, CARE %s", c.Schema, c.Desktop.Version, c.Engine.Version, c.Care.Version)
	}
	if c.Engine.RequiresDesktop != ">=0.0.1" || c.Care.RequiresDesktop != ">=0.0.1" {
		t.Fatalf("engine and CARE must support desktop %s: engine %q, CARE %q", c.Desktop.Version, c.Engine.RequiresDesktop, c.Care.RequiresDesktop)
	}
	if c.Care.Backend.Repo == "" || c.Care.Frontend.Branch == "" {
		t.Fatal("CARE sources are missing")
	}
	constraint, err := semver.NewConstraint(c.Desktop.RequiresEngine)
	if err != nil || constraint.Check(semver.MustParse("0.1.2")) {
		t.Fatal("desktop must exclude engines predating the jobs protocol")
	}
}

func TestUnresolvedEngineSourceCannotBeEmbedded(t *testing.T) {
	config := repoFile(t, ConfigFile)
	c, err := ReadConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"care_engine_aarch64", "care_engine_x86_64"} {
		if c.Tools[tool].SHA256 != "" {
			t.Fatal("reset source must not contain stale or invented engine hashes")
		}
	}
	if _, err := LoadSource(config, repoFile(t, "deployments/.env")); err == nil ||
		!strings.Contains(err.Error(), "engine bootstrap hashes are unresolved") {
		t.Fatalf("unresolved bootstrap source was accepted: %v", err)
	}
}

func TestReadConfigIsStrict(t *testing.T) {
	data := string(repoFile(t, ConfigFile))
	c, err := ReadConfig([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	set := func(field, value string) [2]string {
		return [2]string{field + " = ", field + " = " + value + "\n#"}
	}
	q := func(s string) string { return `"` + s + `"` }
	sha := c.Tools["qemu_windows_x86_64"].SHA256
	docker := c.Tools["docker_linux_x86_64"].URL
	for _, tt := range []struct {
		name    string
		replace [2]string
		ok      bool
	}{
		{"dev desktop", [2]string{`version = "` + c.Desktop.Version + `"`, `version = "` + c.Desktop.Version + `-dev"`}, true},
		{"unknown top-level", [2]string{"schema = 1", "schema = 1\nchannel = \"beta\""}, false},
		{"unknown nested", [2]string{"[care.backend]", "[care.backend]\nref = \"main\""}, false},
		{"legacy lines", [2]string{"[desktop]", "[desktop]\nlines_url = \"https://example.com/lines.json\""}, false},
		{"duplicate key", [2]string{"schema = 1", "schema = 1\nschema = 1"}, false},
		{"malformed", [2]string{"schema = 1", "schema = "}, false},
		{"future schema", [2]string{"schema = 1", "schema = 2"}, false},
		{"missing schema", [2]string{"schema = 1", ""}, false},
		{"v prefix", [2]string{`version = "` + c.Desktop.Version + `"`, `version = "v` + c.Desktop.Version + `"`}, false},
		{"leading zero", [2]string{`version = "` + c.Desktop.Version + `"`, `version = "00.0.1"`}, false},
		{"short version", [2]string{`version = "` + c.Desktop.Version + `"`, `version = "0.1"`}, false},
		{"prerelease", [2]string{`version = "` + c.Desktop.Version + `"`, `version = "` + c.Desktop.Version + `-rc.1"`}, false},
		{"build metadata", [2]string{`version = "` + c.Desktop.Version + `"`, `version = "` + c.Desktop.Version + `+abc"`}, false},
		{"dev care", [2]string{`version = "` + c.Care.Version + `"`, `version = "` + c.Care.Version + `-dev"`}, false},
		{"missing care version", [2]string{`version = "` + c.Care.Version + `"`, ``}, false},
		{"requirement excludes engine", set("requires_engine", q(">=9.0.0")), false},
		{"requirement or", set("requires_engine", q(">=0.1.0 || >=0.2.0")), false},
		{"requirement tilde", set("requires_engine", q("~0.1.2")), false},
		{"requirement wildcard", set("requires_engine", q("0.1.x")), false},
		{"requirement prerelease bound", set("requires_engine", q(">=0.1.2-rc.1")), false},
		{"requirement empty", set("requires_engine", q("")), false},
		{"requirement garbage", set("requires_engine", q("soon")), false},
		{"desktop requirement excludes", set("requires_desktop", q("<0.0.1")), false},
		{"http updates", set("updates_url", q("http://example.com/manifest.json")), false},
		{"updates with query", set("updates_url", q("https://example.com/m.json?x=1")), false},
		{"bad repo", set("repo", q("https://github.com/praffq/care")), false},
		{"bad branch", set("branch", q("../main")), false},
		{"branch with space", set("branch", q("my branch")), false},
		{"branch lock", set("branch", q("main.lock")), false},
		{"latest image", set("postgres", q("postgres:latest")), false},
		{"untagged image", set("redis", q("redis")), false},
		{"registry port without tag", set("redis", q("localhost:5000/redis")), false},
		{"digest image", set("redis", q("redis:8.8.0-alpine@sha256:"+strings.Repeat("b", 64))), true},
		{"bad digest", set("redis", q("redis:8.8.0-alpine@sha256:abc")), false},
		{"unknown image", [2]string{"[support_images]", "[support_images]\nminio = \"minio:1.0.0\""}, false},
		{"missing image", [2]string{`silo = "`, `#silo = "`}, false},
		{"bad coraza", set("coraza", q("2.6.1")), false},
		{"http tool", [2]string{q(docker), q(strings.Replace(docker, "https://", "http://", 1))}, false},
		{"moved tool", [2]string{q(docker), q(strings.Replace(docker, "29.8.1", "29.8.2", 1))}, false},
		{"bad sha", [2]string{q(sha), q(strings.ToUpper(sha))}, false},
		{"short sha", [2]string{q(sha), q(sha[:63])}, false},
		{"unknown tool", [2]string{"[tools.qemu_windows_x86_64]", "[tools.podman]\nurl = \"https://example.com/p\"\nsha256 = \"" + sha + "\"\n\n[tools.qemu_windows_x86_64]"}, false},
		{"unknown tool field", [2]string{"[tools.qemu_windows_x86_64]", "[tools.qemu_windows_x86_64]\nmirror = \"https://example.com\""}, false},
		{"unused tool version", [2]string{"[tools.sevenzip_windows_x86_64]", "[tools.sevenzip_windows_x86_64]\nversion = \"26.4.0\""}, false},
		{"missing tool version", [2]string{"[tools.qemu_windows_x86_64]\nversion = ", "[tools.qemu_windows_x86_64]\n#version = "}, false},
		{"missing tool", [2]string{"[tools.qemu_windows_x86_64]", "[tools.qemu_unused]"}, false},
		{"engine tool disagrees", [2]string{"version = \"" + c.Engine.Version + "\"\nurl =", "version = \"9.0.0\"\nurl ="}, false},
		{"engine name", [2]string{"care-engine-" + c.Engine.Version + "-linux-arm64.tar.gz", "care-engine-" + c.Engine.Version + "-linux-amd64.tar.gz"}, false},
		{"engine base", [2]string{"care-engine-v" + c.Engine.Version + "/care-engine-" + c.Engine.Version + "-linux-arm64", "other/care-engine-" + c.Engine.Version + "-linux-arm64"}, false},
		{"qemu not exe", [2]string{"qemu-w64-setup-20250826.exe", "qemu-w64-setup-20250826.msi"}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			changed := strings.Replace(data, tt.replace[0], tt.replace[1], 1)
			if changed == data {
				t.Fatalf("fixture %q not found", tt.replace[0])
			}
			if _, err := ReadConfig([]byte(changed)); (err == nil) != tt.ok {
				t.Fatalf("ReadConfig() error = %v", err)
			}
		})
	}
}

func TestStageEnvRejectsReleaseValuesInDeploymentEnv(t *testing.T) {
	config := repoFile(t, ConfigFile)
	base := string(repoFile(t, "deployments/.env"))
	if _, err := StageEnv(config, []byte(base)); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"CARE_CLINIC_VERSION", "POSTGRES_IMAGE", "CORAZA_VERSION", "QEMU_WINDOWS_URL",
		"CARE_VM_VERSION", "CARE_LINES_URL", "CARE_DEFAULT_LINE", "CADDY_WAF_PREBUILT", "BACKUP_PREBUILT",
		"DOCKER_CLI_VERSION", "DOCKER_CLI_MACOS_AARCH64_SHA256", "COMPOSE_WINDOWS_X86_64_SHA256",
		"COMPOSE_LINUX_VERSION"} {
		t.Run(key, func(t *testing.T) {
			if _, err := StageEnv(config, []byte(base+key+"=x\n")); err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("release value %s in deployments/.env was accepted: %v", key, err)
			}
		})
	}
	if _, err := StageEnv(append(config, "\nextra = 1\n"...), []byte(base)); err == nil {
		t.Fatal("invalid versions.toml was staged")
	}
	staged, err := StageEnv(config, []byte(strings.TrimSuffix(base, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(staged), "FRONTEND_IMAGE=care_fe:clinic\n") {
		t.Fatalf("base defaults were not kept:\n%s", staged)
	}
}
