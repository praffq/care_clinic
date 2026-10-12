package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPersistConfigRollsBackWhenSideEffectFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	previous := Config{Role: roleServer, MDNSName: "old-clinic"}
	next := Config{Role: roleServer, SetupDone: true, MDNSName: "new-clinic"}
	if err := persistConfig(path, previous, Config{}, nil); err != nil {
		t.Fatal(err)
	}

	err := persistConfig(path, next, previous, func() error {
		return os.ErrPermission
	})
	if err == nil || !strings.Contains(err.Error(), "settings were not changed") {
		t.Fatalf("side-effect failure was not reported as an uncommitted config change: %v", err)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	var got Config
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got != previous {
		t.Fatalf("failed config write persisted %+v, want previous settings %+v", got, previous)
	}
}
