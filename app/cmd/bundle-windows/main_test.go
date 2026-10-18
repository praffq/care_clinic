package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestCopySubset(t *testing.T) {
	src, dst := t.TempDir(), filepath.Join(t.TempDir(), "qemu")
	for _, f := range []string{
		"qemu-system-x86_64.exe", "qemu-system-x86_64w.exe", "qemu-system-aarch64.exe", "qemu-img.exe",
		"libglib-2.0-0.dll", "SDL2.dll", "COPYING", "COPYING.LIB", "VERSION", "README.rst",
		"$PLUGINSDIR/System.dll", "qemu-uninstall.exe",
		"share/bios-256k.bin", "share/kvmvapic.bin", "share/efi-virtio.rom", "share/vgabios-stdvga.bin",
		"share/edk2-aarch64-code.fd", "share/keymaps/en-us", "share/doc/qemu/index.html", "lib/x.dll",
	} {
		p := filepath.Join(src, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(f), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	n, err := copySubset(src, dst)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	_ = filepath.Walk(dst, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			rel, _ := filepath.Rel(dst, p)
			got = append(got, filepath.ToSlash(rel))
		}
		return err
	})
	sort.Strings(got)
	want := "COPYING COPYING.LIB README.rst SDL2.dll VERSION libglib-2.0-0.dll qemu-system-x86_64.exe share/bios-256k.bin share/efi-virtio.rom share/kvmvapic.bin share/vgabios-stdvga.bin"
	if strings.Join(got, " ") != want || n != len(got) {
		t.Fatalf("copied %d:\n%s\nwant:\n%s", n, strings.Join(got, " "), want)
	}
}

func writeFile(t *testing.T, p, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRun(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "versions.yml"), "unused")
	bin := filepath.Join(repo, "app", "build", "bin")
	writeFile(t, filepath.Join(bin, "payload", "stale"), "x")
	writeFile(t, filepath.Join(repo, "svc.exe"), "service")
	writeFile(t, filepath.Join(repo, "qemu", "qemu-system-x86_64.exe"), "qemu")
	writeFile(t, filepath.Join(repo, "qemu", "share", "bios.bin"), "bios")
	writeFile(t, filepath.Join(repo, "dist", "care-engine-0.1.3-amd64.tar.gz"), "old")
	writeFile(t, filepath.Join(repo, "dist", "care-engine-0.1.4-amd64.tar.gz"), "engine")
	writeFile(t, filepath.Join(repo, "dist", "care-engine-0.1.4-arm64.tar.gz"), "arm")
	writeFile(t, filepath.Join(repo, "dist", "SHA256SUMS"), strings.Repeat("ab", 32)+"  care-engine-0.1.4-amd64.tar.gz\n")
	t.Setenv("CARE_CLINIC_SERVICE_BINARY", filepath.Join(repo, "svc.exe"))
	t.Setenv("CARE_CLINIC_QEMU_DIR", filepath.Join(repo, "qemu"))
	t.Setenv("CARE_CLINIC_ENGINE_BUNDLE", "")
	t.Chdir(bin)
	if err := run(context.Background(), filepath.Join(bin, "CARE Clinic.exe")); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"care-clinic-service.exe":               "service",
		"qemu/qemu-system-x86_64.exe":           "qemu",
		"qemu/share/bios.bin":                   "bios",
		"engine/care-engine-0.1.4-amd64.tar.gz": "engine",
		"engine/SHA256SUMS":                     strings.Repeat("ab", 32) + "  care-engine-0.1.4-amd64.tar.gz\n",
	}
	got := map[string]string{}
	payload := filepath.Join(bin, "payload")
	_ = filepath.Walk(payload, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			rel, _ := filepath.Rel(payload, p)
			data, _ := os.ReadFile(p)
			got[filepath.ToSlash(rel)] = string(data)
		}
		return err
	})
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("payload = %v", got)
	}
}

func TestStageEngine(t *testing.T) {
	dir := t.TempDir()
	engine := filepath.Join(dir, "care-engine-0.1.4-amd64.tar.gz")
	writeFile(t, engine, "engine")
	out := filepath.Join(dir, "out")
	if err := stageEngine(engine, out); err != nil {
		t.Fatal(err)
	}
	sums, _ := os.ReadFile(filepath.Join(out, "SHA256SUMS"))
	h := sha256.Sum256([]byte("engine"))
	if want := hex.EncodeToString(h[:]) + "  care-engine-0.1.4-amd64.tar.gz\n"; string(sums) != want {
		t.Errorf("SHA256SUMS = %q, want %q", sums, want)
	}
	arm := filepath.Join(dir, "care-engine-0.1.4-arm64.tar.gz")
	writeFile(t, arm, "arm")
	if err := stageEngine(arm, filepath.Join(dir, "out2")); err == nil {
		t.Error("staged an arm64 engine")
	}
}
