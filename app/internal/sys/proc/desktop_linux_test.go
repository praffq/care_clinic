//go:build linux

package proc

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDesktopDirUsesXDGUserDirectory(t *testing.T) {
	home := t.TempDir()
	config := filepath.Join(t.TempDir(), "xdg")
	if err := os.MkdirAll(config, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, "user-dirs.dirs"),
		[]byte(`XDG_DESKTOP_DIR="$HOME/Schreibtisch"`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", config)
	got, err := DesktopDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, "Schreibtisch"); got != want {
		t.Fatalf("DesktopDir = %q, want %q", got, want)
	}
}

func TestDesktopDirFallsBackForInvalidUserDirectory(t *testing.T) {
	home := t.TempDir()
	config := filepath.Join(t.TempDir(), "xdg")
	if err := os.MkdirAll(config, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, "user-dirs.dirs"),
		[]byte(`XDG_DESKTOP_DIR="$(touch /tmp/file)"`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", config)
	got, err := DesktopDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, "Desktop"); got != want {
		t.Fatalf("DesktopDir = %q, want fallback %q", got, want)
	}
}

func TestXDGDesktopDir(t *testing.T) {
	home := "/home/example"
	for _, tc := range []struct {
		name string
		data string
		want string
		ok   bool
	}{
		{name: "localized", data: `XDG_DESKTOP_DIR="$HOME/Schreibtisch"`, want: "/home/example/Schreibtisch", ok: true},
		{name: "custom home", data: `XDG_DESKTOP_DIR="${HOME}/My Desktop"`, want: "/home/example/My Desktop", ok: true},
		{name: "relative rejected", data: `XDG_DESKTOP_DIR="Desktop"`, want: "", ok: false},
		{name: "shell expansion rejected", data: `XDG_DESKTOP_DIR="$(touch /tmp/file)"`, want: "", ok: false},
		{name: "missing setting", data: `XDG_DOWNLOAD_DIR="$HOME/Downloads"`, want: "", ok: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := xdgDesktopDir(tc.data, home)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("xdgDesktopDir = (%q, %v), want (%q, %v)", got, ok, tc.want, tc.ok)
			}
		})
	}
}
