package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstallBinaries(t *testing.T) {
	src := t.TempDir()
	exe := filepath.Join(src, "svc.exe")
	write := func(p, s string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(exe, "service")
	write(filepath.Join(src, "qemu", "qemu-system-x86_64.exe"), "qemu")
	write(filepath.Join(src, "qemu", "share", "bios.bin"), "bios")

	bin := filepath.Join(t.TempDir(), "bin")
	write(filepath.Join(bin, "stale.dll"), "planted")
	if err := installBinaries(exe, bin, "care-clinic-service.exe", true); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]string{
		"care-clinic-service.exe":     "service",
		"qemu/qemu-system-x86_64.exe": "qemu",
		"qemu/share/bios.bin":         "bios",
	} {
		b, err := os.ReadFile(filepath.Join(bin, filepath.FromSlash(p)))
		if err != nil || string(b) != want {
			t.Fatalf("%s = %q, %v", p, b, err)
		}
	}
	if _, err := os.Stat(filepath.Join(bin, "stale.dll")); !os.IsNotExist(err) {
		t.Fatalf("old bin contents kept: %v", err)
	}

	if err := os.RemoveAll(filepath.Join(src, "qemu")); err != nil {
		t.Fatal(err)
	}
	if err := installBinaries(exe, bin, "care-clinic-service.exe", true); err == nil {
		t.Fatal("installed without QEMU")
	}
	if err := installBinaries(exe, bin, "care-clinic-service.exe", false); err != nil {
		t.Fatal(err)
	}
	if err := installBinaries(filepath.Join(bin, "care-clinic-service.exe"), bin, "care-clinic-service.exe", false); err == nil {
		t.Fatal("reinstalled from the installed copy")
	}
}
