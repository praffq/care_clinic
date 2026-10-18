package plugins

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestCatalogAndDefaults(t *testing.T) {
	cat := Catalog()
	if len(cat) < 2 {
		t.Fatalf("catalog = %+v", cat)
	}
	d := Defaults()
	if len(d) != 1 || d[0].ID != "care_multihost_files_be" || !d[0].Bundled {
		t.Fatalf("defaults = %+v", d)
	}
	if err := ValidateSet(d); err != nil {
		t.Fatal(err)
	}
}

func TestEnablePresetRequiresConfigs(t *testing.T) {
	_, err := Enable(nil, Plugin{ID: "care_filly"})
	var me *MissingConfigError
	if !errors.As(err, &me) || len(me.Keys) != 2 {
		t.Fatalf("err = %v", err)
	}
	set, err := Enable(Defaults(), Plugin{ID: "care_filly", Configs: map[string]any{"MEDISPEAK_API_KEY": "sk-1", "MEDISPEAK_BASE_URL": "https://m"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(set) != 2 || !set[1].Bundled || set[1].Package == "" {
		t.Fatalf("set = %+v", set)
	}
	if len(BuildSpecs(set)) != 0 {
		t.Fatal("bundled presets need no build")
	}
	if Summary(set) != "care_filly,care_multihost_files_be" {
		t.Fatal(Summary(set))
	}
}

func TestEnablePresetKeepsDefaultConfigs(t *testing.T) {
	set, err := Enable(nil, Plugin{ID: "care_multihost_files_be", Configs: map[string]any{"EXTRA": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	if set[0].Configs["ALLOWED_HOSTS"] == nil || set[0].Configs["EXTRA"] != "1" {
		t.Fatalf("configs = %v", set[0].Configs)
	}
}

func TestEnableCustomAndReplace(t *testing.T) {
	set, err := Enable(Defaults(), Plugin{ID: "care_abdm", Package: "git+https://github.com/ohcnetwork/care_abdm.git", Version: "@v1.0"})
	if err != nil {
		t.Fatal(err)
	}
	specs := BuildSpecs(set)
	if len(specs) != 1 || specs[0].Name != "care_abdm" || specs[0].Version != "@v1.0" {
		t.Fatalf("specs = %+v", specs)
	}
	set, err = Enable(set, Plugin{ID: "care_abdm", Package: "care-abdm", Version: "==2.0"})
	if err != nil || len(set) != 2 || set[1].Package != "care-abdm" {
		t.Fatalf("set = %+v, %v", set, err)
	}
	// A preset pinned to another source is no longer bundled.
	set, err = Enable(set, Plugin{ID: "care_multihost_files_be", Package: "git+https://github.com/me/fork.git", Version: "@dev"})
	if err != nil || set[0].Bundled {
		t.Fatalf("set = %+v, %v", set, err)
	}
	// Bundled is derived: the preset's own source is bundled even when the
	// caller leaves the flag unset, and a custom plugin never is.
	pre, _ := FindPreset("care_multihost_files_be")
	set, err = Enable(set, Plugin{ID: pre.ID, Package: pre.Package, Version: pre.Version, Configs: pre.Configs})
	if err != nil || !set[0].Bundled {
		t.Fatalf("set = %+v, %v", set, err)
	}
	set, err = Enable(set, Plugin{ID: "care_abdm", Package: "care-abdm", Version: "==2.0", Bundled: true})
	if err != nil || set[1].Bundled {
		t.Fatalf("set = %+v, %v", set, err)
	}
	set, err = Disable(set, "care_abdm")
	if err != nil || len(set) != 1 {
		t.Fatalf("set = %+v, %v", set, err)
	}
	if _, err := Disable(set, "nope"); err == nil {
		t.Fatal("disabled unknown plugin")
	}
}

func TestValidate(t *testing.T) {
	bad := []Plugin{
		{ID: "Bad-Name", Package: "x"},
		{ID: "x", Package: "file:///etc"},
		{ID: "x", Package: "x", Version: "@main"},
		{ID: "x", Package: "git+https://h/x", Version: "==1"},
		{ID: "x", Package: "x", Version: "1.0; rm"},
		{ID: "x", Package: "x", Configs: map[string]any{"a b": 1}},
	}
	for _, p := range bad {
		if p.Validate() == nil {
			t.Errorf("%+v accepted", p)
		}
	}
	if err := (Plugin{ID: "x", Package: "x"}).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestAdditionalPlugsAndRedaction(t *testing.T) {
	set := []Plugin{{ID: "care_filly", Package: "care-filly", Configs: map[string]any{"MEDISPEAK_API_KEY": "sk-1", "MEDISPEAK_BASE_URL": "u"}}}
	s, err := AdditionalPlugs(set)
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	if err := json.Unmarshal([]byte(s), &got); err != nil {
		t.Fatal(err)
	}
	if got[0]["name"] != "care_filly" || got[0]["package_name"] != "care-filly" || got[0]["version"] != "" {
		t.Fatalf("plugs = %s", s)
	}
	r := RedactAll(set)
	if r[0].Configs["MEDISPEAK_API_KEY"] != "********" || r[0].Configs["MEDISPEAK_BASE_URL"] != "u" {
		t.Fatalf("redacted = %v", r[0].Configs)
	}
	if set[0].Configs["MEDISPEAK_API_KEY"] != "sk-1" {
		t.Fatal("redaction changed the original")
	}
	if s, _ := AdditionalPlugs(nil); s != "[]" {
		t.Fatal(s)
	}
}

func TestParseConfig(t *testing.T) {
	k, v, err := ParseConfig(`ALLOWED_HOSTS=["*.local"]`)
	if err != nil || k != "ALLOWED_HOSTS" || len(v.([]any)) != 1 {
		t.Fatalf("%s %v %v", k, v, err)
	}
	if _, v, _ := ParseConfig("URL=https://x"); v != "https://x" {
		t.Fatal(v)
	}
	if _, _, err := ParseConfig("novalue"); err == nil || !strings.Contains(err.Error(), "KEY=value") {
		t.Fatal(err)
	}
}

func TestParseAdditionalPlugsRoundTrip(t *testing.T) {
	set, _ := Enable(Defaults(), Plugin{ID: "x_plug", Package: "x-plug", Version: "==1"})
	s, _ := AdditionalPlugs(set)
	got, err := ParseAdditionalPlugs(s)
	if err != nil || len(got) != 2 || !got[0].Bundled || got[1].Bundled || got[1].Version != "==1" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := ParseAdditionalPlugs("nope"); err == nil {
		t.Fatal("parsed junk")
	}
}
