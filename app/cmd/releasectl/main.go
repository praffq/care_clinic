// releasectl is the shared build-planning and signed-publication entrypoint.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/compose-spec/compose-go/v2/dotenv"
	"github.com/ohcnetwork/care_desktop/app/internal/release"
	"go.yaml.in/yaml/v3"
)

type plan struct {
	Desktop        bool              `json:"desktop"`
	Engine         bool              `json:"engine"`
	Backend        bool              `json:"backend"`
	Frontend       bool              `json:"frontend"`
	Support        bool              `json:"support"`
	DesktopVersion string            `json:"desktop_version"`
	EngineVersion  string            `json:"engine_version"`
	CareVersion    string            `json:"care_version"`
	BackendRepo    string            `json:"backend_repo"`
	BackendBranch  string            `json:"backend_branch"`
	BackendCommit  string            `json:"backend_commit"`
	FrontendRepo   string            `json:"frontend_repo"`
	FrontendBranch string            `json:"frontend_branch"`
	FrontendCommit string            `json:"frontend_commit"`
	Hashes         map[string]string `json:"hashes"`
	BuildEnv       map[string]string `json:"build_env"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("expected plan, manifest, sign, verify or asset")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	root := flags.String("root", "..", "repository root")
	previous := flags.String("previous", "", "previous verified manifest; absent for first publication")
	out := flags.String("out", "", "output path (stdout if empty)")
	outputs := flags.String("outputs", "", "complete collected build outputs")
	manifest := flags.String("manifest", "", "manifest to authenticate")
	signature := flags.String("signature", "", "detached signature")
	file := flags.String("file", "", "asset path")
	url := flags.String("url", "", "immutable release base URL")
	planPath := flags.String("plan", "", "resolved run-start plan")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	switch args[0] {
	case "sign", "verify":
		data, err := os.ReadFile(*manifest)
		if err != nil {
			return err
		}
		if args[0] == "verify" {
			sig, err := os.ReadFile(*signature)
			if err != nil {
				return err
			}
			_, err = release.VerifyManifest(data, sig, 0)
			return err
		}
		sig, err := release.SignManifest(data, os.Getenv("UPDATES_SIGNING_KEY"))
		if err != nil {
			return err
		}
		if *out == "" {
			return fmt.Errorf("sign requires --out")
		}
		return os.WriteFile(*out, sig, 0o644)
	case "asset":
		asset, err := release.AssetForFile(*file, *url)
		if err != nil {
			return err
		}
		return writeJSON(*out, asset)
	case "plan", "manifest":
		data, err := os.ReadFile(filepath.Join(*root, "versions.toml"))
		if err != nil {
			return err
		}
		config, err := release.ReadConfig(data)
		if err != nil {
			return err
		}
		var old release.Manifest
		if *previous != "" {
			data, err := os.ReadFile(*previous)
			if err != nil && !os.IsNotExist(err) {
				return err
			}
			if err == nil {
				old, err = release.DecodeManifest(data)
				if err != nil {
					return err
				}
			}
		}
		if args[0] == "plan" {
			p, err := makePlan(*root, config, old)
			if err != nil {
				return err
			}
			return writeJSON(*out, p)
		}
		if *planPath == "" {
			return fmt.Errorf("manifest requires run-start --plan")
		}
		var p plan
		if err := readJSON(*planPath, &p); err != nil {
			return err
		}
		var m release.Manifest
		if err := readJSON(*outputs, &m); err != nil {
			return err
		}
		if old.Revision == ^uint64(0) {
			return fmt.Errorf("manifest revision exhausted")
		}
		m.Schema, m.Revision = 1, old.Revision+1
		m.PublishedAt = time.Now().UTC().Format(time.RFC3339)
		m.Desktop.Version, m.Desktop.RequiresEngine = config.Desktop.Version, config.Desktop.RequiresEngine
		m.Engine.Version, m.Engine.RequiresDesktop = config.Engine.Version, config.Engine.RequiresDesktop
		m.Care.Version = config.Care.Version
		m.Care.RequiresDesktop, m.Care.RequiresEngine = config.Care.RequiresDesktop, config.Care.RequiresEngine
		if m.Care.Backend.Commit != p.BackendCommit || m.Care.Frontend.Commit != p.FrontendCommit {
			return fmt.Errorf("build outputs do not match run-start branch snapshots")
		}
		m.Care.Backend.SourceHash, m.Care.Frontend.SourceHash = p.Hashes["backend"], p.Hashes["frontend"]
		m.Care.Frontend.BuildEnv = p.BuildEnv
		m.SourceHashes = map[string]string{"desktop": p.Hashes["desktop"], "engine": p.Hashes["engine"], "support": p.Hashes["support"]}
		if m.Blocked == nil {
			m.Blocked = []string{}
		}
		if err := m.Validate(); err != nil {
			return err
		}
		return writeJSON(*out, m)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func writeJSON(path string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if path == "" {
		_, err = os.Stdout.Write(b)
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

func readJSON(path string, value any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	return d.Decode(value)
}

func resolve(source release.Source) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "ls-remote", "--exit-code", "https://github.com/"+source.Repo+".git", "refs/heads/"+source.Branch).Output()
	if err != nil {
		return "", fmt.Errorf("resolve %s/%s: %w", source.Repo, source.Branch, err)
	}
	fields := strings.Fields(string(out))
	if len(fields) != 2 || len(fields[0]) != 40 || fields[1] != "refs/heads/"+source.Branch {
		return "", fmt.Errorf("unexpected branch resolution for %s", source.Repo)
	}
	if _, err := hex.DecodeString(fields[0]); err != nil {
		return "", err
	}
	return fields[0], nil
}

func makePlan(root string, c release.Config, old release.Manifest) (plan, error) {
	p := plan{
		DesktopVersion: c.Desktop.Version, EngineVersion: c.Engine.Version, CareVersion: c.Care.Version,
		BackendRepo: c.Care.Backend.Repo, BackendBranch: c.Care.Backend.Branch,
		FrontendRepo: c.Care.Frontend.Repo, FrontendBranch: c.Care.Frontend.Branch,
		Hashes: map[string]string{},
	}
	var err error
	p.BackendCommit, err = resolve(c.Care.Backend)
	if err != nil {
		return p, err
	}
	p.FrontendCommit, err = resolve(c.Care.Frontend)
	if err != nil {
		return p, err
	}
	p.BuildEnv, err = frontendEnv(root)
	if err != nil {
		return p, err
	}
	for _, component := range []struct {
		name   string
		paths  []string
		config any
	}{
		{"desktop", []string{"app", "deployments"}, c},
		{"engine", []string{"vm"}, struct{ Version string }{c.Engine.Version}},
		{"backend", []string{"images/backend"}, c.Care.Backend},
		{"frontend", []string{"images/frontend", "images/scripts/frontend-env.mjs"}, struct {
			Source release.Source
			Env    map[string]string
		}{c.Care.Frontend, p.BuildEnv}},
		{"support", []string{"deployments/caddy.Dockerfile", "deployments/backup.Dockerfile", "deployments/scripts/backup.sh"}, struct {
			Images map[string]string
			Coraza string
		}{c.SupportImages, c.Caddy.Coraza}},
	} {
		source, err := release.SourceHash(root, component.paths...)
		if err != nil {
			return p, err
		}
		config, err := json.Marshal(component.config)
		if err != nil {
			return p, err
		}
		sum := sha256.Sum256(append([]byte(source), config...))
		p.Hashes[component.name] = hex.EncodeToString(sum[:])
	}
	p.Desktop = old.Desktop.Version != c.Desktop.Version || old.SourceHashes["desktop"] != p.Hashes["desktop"]
	p.Engine = old.Engine.Version != c.Engine.Version || old.SourceHashes["engine"] != p.Hashes["engine"]
	p.Backend = old.Care.Version != c.Care.Version || old.Care.Backend.Commit != p.BackendCommit || old.Care.Backend.SourceHash != p.Hashes["backend"]
	p.Frontend = old.Care.Version != c.Care.Version || old.Care.Frontend.Commit != p.FrontendCommit || old.Care.Frontend.SourceHash != p.Hashes["frontend"]
	p.Support = old.SourceHashes["support"] != p.Hashes["support"]
	return p, nil
}

func frontendEnv(root string) (map[string]string, error) {
	data, err := os.ReadFile(filepath.Join(root, "deployments", "env-defaults.yml"))
	if err == nil {
		var defaults struct {
			BE map[string]string `yaml:"be"`
			FE map[string]string `yaml:"fe"`
		}
		d := yaml.NewDecoder(strings.NewReader(string(data)))
		d.KnownFields(true)
		if err := d.Decode(&defaults); err != nil {
			return nil, err
		}
		if defaults.FE == nil {
			return map[string]string{}, nil
		}
		return defaults.FE, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	// Phase 3 introduces env-defaults.yml. Until then the existing published
	// frontend defaults remain build inputs, not a second release version file.
	data, err = os.ReadFile(filepath.Join(root, "deployments", "frontend.env"))
	if err != nil {
		return nil, err
	}
	return dotenv.Parse(strings.NewReader(string(data)))
}
