// Package envconfig generates clinic configuration without storing credentials
// in release defaults or exposing them through the settings editor.
package envconfig

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/compose-spec/compose-go/v2/dotenv"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/atomicfile"
	"go.yaml.in/yaml/v3"
)

const APIPlaceholder = "https://care-clinic.invalid/__CARE_CLINIC_REACT_CARE_API_URL__"

type Defaults struct {
	BE map[string]string `yaml:"be"`
	FE map[string]string `yaml:"fe"`
}

var keyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var secretKeys = []string{"DJANGO_SECRET_KEY", "POSTGRES_PASSWORD", "BUCKET_KEY", "BUCKET_SECRET", "SNS_ACCESS_KEY", "SNS_SECRET_KEY", "EMAIL_PASSWORD"}

func Managed(key string) bool {
	for _, k := range secretKeys {
		if key == k {
			return true
		}
	}
	switch key {
	case "DATABASE_URL", "CSRF_TRUSTED_ORIGINS", "BUCKET_EXTERNAL_ENDPOINT", "REACT_CARE_API_URL":
		return true
	}
	return false
}

func LoadDefaults(path string) (Defaults, error) {
	var result Defaults
	data, err := os.ReadFile(path)
	if err != nil {
		return result, err
	}
	// yaml's string decoding coerces booleans and numbers; require actual string
	// scalars so a release cannot accidentally change a setting's spelling.
	var node yaml.Node
	d := yaml.NewDecoder(bytes.NewReader(data))
	d.KnownFields(true)
	if err := d.Decode(&node); err != nil {
		return result, err
	}
	if len(node.Content) != 1 || node.Content[0].Kind != yaml.MappingNode {
		return result, fmt.Errorf("env-defaults.yml must contain be and fe maps")
	}
	root := node.Content[0]
	for i := 0; i < len(root.Content); i += 2 {
		name, values := root.Content[i].Value, root.Content[i+1]
		if (name != "be" && name != "fe") || values.Kind != yaml.MappingNode {
			return result, fmt.Errorf("env-defaults.yml: unknown section or non-map %q", name)
		}
		for j := 0; j < len(values.Content); j += 2 {
			key, value := values.Content[j], values.Content[j+1]
			if key.Tag != "!!str" || value.Tag != "!!str" || !keyPattern.MatchString(key.Value) {
				return result, fmt.Errorf("env-defaults.yml: %s requires string keys and values", name)
			}
			if Managed(key.Value) || key.Value == "ADDITIONAL_PLUGS" || key.Value == "CORAZA_MODE" || key.Value == "REACT_APP_TITLE" || key.Value == "REACT_ALLOWED_LOCALES" {
				return result, fmt.Errorf("env-defaults.yml: %s is clinic-managed, not a release default", key.Value)
			}
			if (name == "fe") != strings.HasPrefix(key.Value, "REACT_") {
				return result, fmt.Errorf("env-defaults.yml: %s is in the wrong section", key.Value)
			}
		}
	}
	if err := node.Decode(&result); err != nil {
		return result, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return result, fmt.Errorf("env-defaults.yml has trailing data")
	}
	if result.BE == nil || result.FE == nil {
		return result, fmt.Errorf("env-defaults.yml requires both be and fe maps")
	}
	return result, nil
}

func Read(path string) (map[string]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return dotenv.Parse(bytes.NewReader(b))
}

func Overrides(dir, section string) (map[string]string, error) {
	if section != "backend" && section != "frontend" {
		return nil, fmt.Errorf("unknown env section %q", section)
	}
	env, err := Read(filepath.Join(dir, section+".overrides.env"))
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	return env, ValidateOverrides(section, env)
}

func ValidateOverrides(section string, env map[string]string) error {
	for key := range env {
		if Managed(key) {
			return fmt.Errorf("%s is generated and protected by CARE Clinic", key)
		}
		if !keyPattern.MatchString(key) || (section == "frontend") != strings.HasPrefix(key, "REACT_") {
			return fmt.Errorf("%s is not a valid %s setting", key, section)
		}
	}
	return nil
}

// Render is deterministic and lossless for compose-go dotenv, including empty,
// multiline, quotes and dollar signs.
func Render(env map[string]string) string {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var out strings.Builder
	escape := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "$", "$$")
	for _, key := range keys {
		out.WriteString(key + `="` + escape.Replace(env[key]) + "\"\n")
	}
	return out.String()
}

func EnsureSecrets(dir string) error {
	path := filepath.Join(dir, "secrets.env")
	env, err := Read(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil {
		for _, key := range secretKeys {
			if env[key] == "" {
				return fmt.Errorf("secrets.env is missing %s; restore the clinic secrets before continuing", key)
			}
		}
	} else {
		env = map[string]string{}
		for _, key := range secretKeys {
			raw := make([]byte, 40)
			if _, err := rand.Read(raw); err != nil {
				return err
			}
			env[key] = base64.RawURLEncoding.EncodeToString(raw)
		}
	}
	if err := atomicfile.WritePrivate(path, []byte(Render(env))); err != nil {
		return err
	}
	for _, section := range []string{"backend", "frontend"} {
		path := filepath.Join(dir, section+".overrides.env")
		if _, err := os.Stat(path); os.IsNotExist(err) {
			if err := atomicfile.WritePrivate(path, nil); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
	}
	return nil
}

func Effective(dir, host string) (map[string]string, map[string]string, error) {
	defaults, err := LoadDefaults(filepath.Join(dir, "env-defaults.yml"))
	if err != nil {
		return nil, nil, err
	}
	be, fe := defaults.BE, defaults.FE
	for section, env := range map[string]map[string]string{"backend": be, "frontend": fe} {
		overrides, err := Overrides(dir, section)
		if err != nil {
			return nil, nil, err
		}
		for key, value := range overrides {
			env[key] = value
		}
	}
	secrets, err := Read(filepath.Join(dir, "secrets.env"))
	if err != nil {
		return nil, nil, err
	}
	for _, key := range secretKeys {
		if secrets[key] == "" {
			return nil, nil, fmt.Errorf("secrets.env is missing %s", key)
		}
		be[key] = secrets[key]
	}
	if host == "" || strings.ContainsAny(host, "/\\: \t\r\n") {
		return nil, nil, fmt.Errorf("invalid clinic hostname")
	}
	origin := "https://" + host
	csrf, err := json.Marshal([]string{origin})
	if err != nil {
		return nil, nil, err
	}
	be["CSRF_TRUSTED_ORIGINS"], be["BUCKET_EXTERNAL_ENDPOINT"] = string(csrf), origin
	fe["REACT_CARE_API_URL"] = origin
	for _, key := range []string{"POSTGRES_USER", "POSTGRES_HOST", "POSTGRES_PORT", "POSTGRES_DB"} {
		if be[key] == "" {
			return nil, nil, fmt.Errorf("%s must be set to derive DATABASE_URL", key)
		}
	}
	u := url.URL{Scheme: "postgres", User: url.UserPassword(be["POSTGRES_USER"], secrets["POSTGRES_PASSWORD"]),
		Host: net.JoinHostPort(be["POSTGRES_HOST"], be["POSTGRES_PORT"]), Path: "/" + be["POSTGRES_DB"]}
	be["DATABASE_URL"] = u.String()
	return be, fe, nil
}

func Generate(dir, host string) error {
	be, fe, err := Effective(dir, host)
	if err != nil {
		return err
	}
	for name, env := range map[string]map[string]string{"backend.env": be, "frontend.env": fe} {
		if err := atomicfile.WritePrivate(filepath.Join(dir, name), []byte(Render(env))); err != nil {
			return err
		}
	}
	return nil
}
