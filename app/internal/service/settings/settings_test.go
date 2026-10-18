package settings

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/service/api"
)

func TestDefaultsAndPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	var applied []api.ServiceConfig
	s, err := Open(path, func(c api.ServiceConfig) { applied = append(applied, c) })
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Get().MDNSName; got != DefaultMDNSName {
		t.Fatalf("default name = %q", got)
	}
	got, err := s.Update(api.ServiceConfigUpdate{MDNSName: ptr(" Clinic-2.local. ")})
	if err != nil {
		t.Fatal(err)
	}
	if got.MDNSName != "clinic-2" || s.Get().MDNSName != "clinic-2" || len(applied) != 1 || applied[0].MDNSName != "clinic-2" {
		t.Fatalf("set = %+v, get = %+v, applied = %+v", got, s.Get(), applied)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o644 {
		t.Fatalf("config.json: %v %v", fi, err)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".config-*")); len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}
	got, err = s.Update(api.ServiceConfigUpdate{ProxyProtocol: ptr(true)})
	if err != nil || got != (api.ServiceConfig{MDNSName: "clinic-2", ProxyProtocol: true}) {
		t.Fatalf("partial update = %+v, %v", got, err)
	}
	s2, err := Open(path, nil)
	if err != nil || s2.Get() != got {
		t.Fatalf("reopen: %+v %v", s2.Get(), err)
	}
}

func ptr[T any](v T) *T { return &v }

func TestSetRejectsInvalidName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	s, err := Open(path, func(api.ServiceConfig) { t.Fatal("applied an invalid config") })
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", "bad_name", "-care"} {
		if _, err := s.Update(api.ServiceConfigUpdate{MDNSName: ptr(name)}); !errors.Is(err, api.ErrInvalidConfig) {
			t.Errorf("Update(%q) = %v", name, err)
		}
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid config was written: %v", err)
	}
}

func TestOpenRejectsCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	for _, body := range []string{"{", `{"mdns_name":"no spaces"}`} {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		s, err := Open(path, nil)
		if err == nil {
			t.Errorf("Open accepted %q", body)
		}
		if s == nil || s.Get().MDNSName != DefaultMDNSName {
			t.Errorf("no default store for %q: %+v", body, s)
		}
	}
}
