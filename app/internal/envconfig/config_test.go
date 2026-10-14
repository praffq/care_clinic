package envconfig

import (
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/plugins"
)

func put(t *testing.T, dir, name, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	data, err := os.ReadFile("../../../deployments/env-defaults.yml")
	if err != nil {
		t.Fatal(err)
	}
	put(t, dir, "env-defaults.yml", string(data))
	if err := EnsureSecrets(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestEffectiveLayersEmptyAndReset(t *testing.T) {
	dir := fixture(t)
	put(t, dir, "backend.overrides.env", "DJANGO_DEBUG=True\nEXTRA=''\n")
	put(t, dir, "frontend.overrides.env", "REACT_MFE_REGISTERED_COMPONENTS=''\nREACT_APP_TITLE='North Clinic'\n")
	be, fe, err := Effective(dir, "north.local")
	if err != nil {
		t.Fatal(err)
	}
	if be["DJANGO_DEBUG"] != "True" || be["EXTRA"] != "" || fe["REACT_MFE_REGISTERED_COMPONENTS"] != "" || fe["REACT_APP_TITLE"] != "North Clinic" {
		t.Fatal("overrides did not win, including explicit empties")
	}
	if _, ok := be["EXTRA"]; !ok {
		t.Fatal("empty became absent")
	}
	put(t, dir, "frontend.overrides.env", "REACT_MFE_REGISTERED_COMPONENTS=UserDashboard\n")
	if err := Generate(dir, "north.local"); err != nil {
		t.Fatal(err)
	}
	overrides, err := Overrides(dir, "frontend")
	if err != nil || overrides["REACT_MFE_REGISTERED_COMPONENTS"] != "UserDashboard" {
		t.Fatal("equal-to-default override was lost")
	}
	defaults, err := os.ReadFile(filepath.Join(dir, "env-defaults.yml"))
	if err != nil {
		t.Fatal(err)
	}
	put(t, dir, "env-defaults.yml", strings.Replace(string(defaults), `"UserDashboard"`, `"NewDefault"`, 1))
	_, fe, err = Effective(dir, "north.local")
	if err != nil || fe["REACT_MFE_REGISTERED_COMPONENTS"] != "UserDashboard" {
		t.Fatal("changed release defaults overwrote an explicit override")
	}
	put(t, dir, "frontend.overrides.env", "")
	_, fe, err = Effective(dir, "north.local")
	if err != nil || fe["REACT_MFE_REGISTERED_COMPONENTS"] != "NewDefault" {
		t.Fatal("reset did not restore release default")
	}
	if _, present := fe["REACT_APP_TITLE"]; present {
		t.Fatal("absent value did not fall through to CARE's defaults")
	}
}

func TestGeneratedSecretsModesDeterminismAndHostname(t *testing.T) {
	dir := fixture(t)
	before, err := os.ReadFile(filepath.Join(dir, "secrets.env"))
	if err != nil {
		t.Fatal(err)
	}
	if err := EnsureSecrets(dir); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(dir, "secrets.env"))
	if err != nil || string(before) != string(after) {
		t.Fatal("setup retry rotated secrets")
	}
	secrets, err := Read(filepath.Join(dir, "secrets.env"))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, key := range secretKeys {
		if len(secrets[key]) < 50 || seen[secrets[key]] {
			t.Fatal("secret was short or reused")
		}
		seen[secrets[key]] = true
	}
	if err := Generate(dir, "north.local"); err != nil {
		t.Fatal(err)
	}
	be, fe, err := Effective(dir, "north.local")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(be["DATABASE_URL"])
	if err != nil {
		t.Fatal(err)
	}
	password, ok := u.User.Password()
	if !ok || password != secrets["POSTGRES_PASSWORD"] || u.Host != "db:5432" || u.Path != "/care" {
		t.Fatal("database URL was not derived from secrets")
	}
	if fe["REACT_CARE_API_URL"] != "https://north.local" || be["BUCKET_EXTERNAL_ENDPOINT"] != "https://north.local" || be["CSRF_TRUSTED_ORIGINS"] != `["https://north.local"]` {
		t.Fatal("hostname settings were not derived")
	}
	for name, expected := range map[string]map[string]string{"backend.env": be, "frontend.env": fe, "secrets.env": secrets} {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil || string(data) != Render(expected) {
			t.Fatalf("%s generation was not deterministic: %v", name, err)
		}
		info, err := os.Stat(path)
		if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
			t.Fatalf("%s is not private: %v", name, err)
		}
	}
	if err := Generate(dir, "south.local"); err != nil {
		t.Fatal(err)
	}
	renamed, err := Read(filepath.Join(dir, "backend.env"))
	if err != nil || renamed["DJANGO_SECRET_KEY"] != be["DJANGO_SECRET_KEY"] || renamed["CSRF_TRUSTED_ORIGINS"] != `["https://south.local"]` {
		t.Fatal("hostname edit changed secrets or failed to update derived values")
	}
}

func TestRenderRoundTrip(t *testing.T) {
	env := map[string]string{"EMPTY": "", "QUOTES": "it's a \"quote\" and `tick`", "DOLLAR": "$HOME ${UNSET} # x", "LINES": "first\nsecond", "SLASH": `C:\clinic\path`, "ENDING": `a\'`}
	dir := t.TempDir()
	put(t, dir, "test.env", Render(env))
	got, err := Read(filepath.Join(dir, "test.env"))
	if err != nil || !maps.Equal(got, env) {
		t.Fatalf("dotenv roundtrip: got %#v, want %#v, err %v", got, env, err)
	}
}

func TestDefaultsRejectInvalidAndManagedValues(t *testing.T) {
	dir := t.TempDir()
	for _, text := range []string{
		"be: {}\nfe:\n  REACT_FLAG: true\n",
		"be: {}\nfe:\n  REACT_FLAG: 0\n",
		"be: {}\nfe: {}\nunknown: {}\n",
		"be: {}\nfe: {}\n---\nbe: {}\n",
		"be:\n  DJANGO_SECRET_KEY: 'bad'\nfe: {}\n",
		"be: {}\nfe:\n  REACT_CARE_API_URL: 'https://example.local'\n",
		"be: {}\nfe:\n  REACT_APP_TITLE: 'Not a clinic setting'\n",
		"be: {}\n",
		"be: {}\nfe:\n  REACT_FLAG: 'a'\n  REACT_FLAG: 'b'\n",
	} {
		put(t, dir, "defaults.yml", text)
		if _, err := LoadDefaults(filepath.Join(dir, "defaults.yml")); err == nil {
			t.Fatalf("accepted invalid defaults: %s", text)
		}
	}
}

func TestSecretsCannotBeOverridesAndMissingSecretsFailClosed(t *testing.T) {
	dir := fixture(t)
	for _, key := range append(append([]string{}, secretKeys...), "DATABASE_URL", "CSRF_TRUSTED_ORIGINS", "BUCKET_EXTERNAL_ENDPOINT", "REACT_CARE_API_URL") {
		if err := ValidateOverrides("backend", map[string]string{key: "bad"}); err == nil {
			t.Fatalf("accepted managed override %s", key)
		}
	}
	put(t, dir, "secrets.env", "DJANGO_SECRET_KEY=existing\n")
	if err := EnsureSecrets(dir); err == nil {
		t.Fatal("silently replaced incomplete secrets")
	}
	if err := Generate(dir, "care.local"); err == nil {
		t.Fatal("generated effective settings with missing credentials")
	}
}

func TestPluginOverridesSurviveConfigurationRegeneration(t *testing.T) {
	dir := fixture(t)
	if err := Generate(dir, "care.local"); err != nil {
		t.Fatal(err)
	}
	manager := plugins.New(dir)
	list := []plugins.Plugin{{ID: "example", Backend: &plugins.Backend{
		Name: "example", PackageName: "example", Configs: map[string]any{"name": "O'Connor $literal"},
	}}}
	if err := manager.SavePlugins(list); err != nil {
		t.Fatal(err)
	}
	before, err := manager.AdditionalPlugs()
	if err != nil {
		t.Fatal(err)
	}
	if err := Generate(dir, "renamed.local"); err != nil {
		t.Fatal(err)
	}
	after, err := manager.AdditionalPlugs()
	if err != nil || after != before || !strings.Contains(after, "O'Connor $literal") {
		t.Fatalf("plugin configuration lost during regeneration: %s %v", after, err)
	}
}
