package release

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/sys/proc"
)

func TestWailsStagesMissingInstallBeforeGoBuild(t *testing.T) {
	if !proc.Exists("node") {
		t.Skip("Node.js is not installed")
	}
	data, err := os.ReadFile("../../wails.json")
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		PreBuildHooks map[string]string `json:"preBuildHooks"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	command := strings.Fields(config.PreBuildHooks["*/*"])
	if len(command) == 0 {
		t.Fatal("Wails must stage the install kit before every platform's Go build")
	}
	script, err := os.ReadFile("../../frontend/scripts/stage-install.mjs")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	write := func(path, content string) {
		t.Helper()
		target := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("app/frontend/scripts/stage-install.mjs", string(script))
	write("app/wails.json", `{"name":"care-clinic","info":{"productVersion":"9.9.9","productName":"CARE Clinic"}}`)
	write("deployments/minio/entrypoint.sh", "storage entrypoint")
	write("app/main.go", `package main
import ("embed"; "fmt")
//go:embed all:install
var kit embed.FS
func main() {
	data, err := kit.ReadFile("install/.env")
	if err != nil { panic(err) }
	fmt.Print(string(data))
}
`)
	bin := filepath.Join(root, "app", "build", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ state, version string }{
		{"missing", "1.2.3"},
		{"deleted-again", "1.2.3"},
		{"stale", "1.2.3"},
		{"development", "2.3.4-dev"},
	} {
		t.Run(tc.state, func(t *testing.T) {
			write("versions.yml", "schema: 1\n\ndesktop:\n  version: \""+tc.version+"\"\n  requires: { engine: \">=0.1.0\" }\n\nengine:\n  version: \"9.9.9\"\n")
			write("deployments/.env", pinsEnv)
			manifest := "CARE_CLINIC_VERSION=" + tc.version + "\n" + pinsEnv
			install := filepath.Join(root, "app", "install")
			switch tc.state {
			case "deleted-again":
				if err := os.RemoveAll(install); err != nil {
					t.Fatal(err)
				}
			case "stale":
				write("app/install/.env", "stale")
				write("app/install/removed-file", "stale")
				write("app/install/setup/index.html", "obsolete device setup")
				write("app/install/.gitkeep", "")
			}
			// Wails executes pre-build hooks from its binary output directory.
			cmd := proc.Command(command[0], command[1:]...)
			cmd.Dir = bin
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("pre-build hook failed: %v\n%s", err, output)
			}
			metadata, err := os.ReadFile(filepath.Join(root, "app", "wails.json"))
			if err != nil {
				t.Fatal(err)
			}
			var info struct {
				Name string `json:"name"`
				Info struct {
					ProductVersion string `json:"productVersion"`
					ProductName    string `json:"productName"`
				} `json:"info"`
			}
			if err := json.Unmarshal(metadata, &info); err != nil {
				t.Fatal(err)
			}
			if info.Info.ProductVersion != strings.TrimSuffix(tc.version, "-dev") || info.Name != "care-clinic" || info.Info.ProductName != "CARE Clinic" {
				t.Fatalf("installer metadata was not derived from versions.yml: %s", metadata)
			}
			cmd = proc.Command("go", "run", "main.go")
			cmd.Dir = filepath.Join(root, "app")
			if output, err := cmd.CombinedOutput(); err != nil || string(output) != manifest {
				t.Fatalf("compiled kit is missing or stale: %v\n%s", err, output)
			}
			if data, err := os.ReadFile(filepath.Join(install, "minio", "entrypoint.sh")); err != nil || string(data) != "storage entrypoint" {
				t.Fatalf("nested kit file was not staged: %v", err)
			}
			if _, err := os.Stat(filepath.Join(install, "setup")); !os.IsNotExist(err) {
				t.Fatalf("obsolete device setup was staged: %v", err)
			}
			if tc.state == "stale" {
				if _, err := os.Stat(filepath.Join(install, "removed-file")); !os.IsNotExist(err) {
					t.Fatalf("obsolete kit file survived: %v", err)
				}
				if _, err := os.Stat(filepath.Join(install, ".gitkeep")); err != nil {
					t.Fatalf("tracked placeholder was removed: %v", err)
				}
			}
		})
	}
	for name, tc := range map[string]struct{ versions, env string }{
		"no-desktop":     {"schema: 1\nengine:\n  version: \"1.2.3\"\n", pinsEnv},
		"empty":          {"desktop:\n  version: \"\"\n", pinsEnv},
		"invalid":        {"desktop:\n  version: \"invalid\"\n", pinsEnv},
		"leading-zero":   {"desktop:\n  version: \"01.2.3\"\n", pinsEnv},
		"duplicate":      {"desktop:\n  version: \"1.2.3\"\n  version: \"2.0.0\"\n", pinsEnv},
		"engine-only":    {"desktop:\n  requires: {}\nengine:\n  version: \"1.2.3\"\n", pinsEnv},
		"version-in-env": {"desktop:\n  version: \"1.2.3\"\n", "CARE_CLINIC_VERSION=1.2.3\n" + pinsEnv},
	} {
		t.Run("invalid-"+name, func(t *testing.T) {
			write("versions.yml", tc.versions)
			write("deployments/.env", tc.env)
			cmd := proc.Command(command[0], command[1:]...)
			cmd.Dir = bin
			if output, err := cmd.CombinedOutput(); err == nil {
				t.Fatalf("invalid release version was accepted: %s", output)
			}
			data, err := os.ReadFile(filepath.Join(root, "app", "install", ".env"))
			if err != nil || string(data) != "CARE_CLINIC_VERSION=2.3.4-dev\n"+pinsEnv {
				t.Fatalf("invalid version changed the staged kit: %q, %v", data, err)
			}
		})
	}
}

const pinsEnv = "POSTGRES_IMAGE=postgres:17\n"
