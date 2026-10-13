package vm

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func writeQEMUFixture(t *testing.T, root string) {
	t.Helper()
	for _, name := range []string{"qemu-system-x86_64.exe", "libslirp-0.dll", "dependency.dll",
		"share/bios-256k.bin", "share/firmware.bin", "lib/data", "COPYING", "COPYING.LIB",
		"README.rst", "VERSION", "qemu-system-arm.exe", "$PLUGINSDIR/plugin.dll"} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		content := name
		if name == "VERSION" {
			content = "10.1.0\n"
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestUpstreamQEMUExtractionUsesOnlyPrivateExtractors(t *testing.T) {
	root := t.TempDir()
	bootstrap := filepath.Join(root, "download")
	if err := os.WriteFile(bootstrap, []byte("verified extractor"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := EngineFiles{QEMU: "qemu.download", QEMUBootstrap: bootstrap, QEMUExtractor: "7zip.download", QEMUVersion: "10.1.0"}
	dest := filepath.Join(root, "staged")
	calls := 0
	err := stageQEMUInstallerWith(f, dest, func(ctx context.Context, name string, args ...string) error {
		calls++
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("extraction has no timeout")
		}
		if !strings.HasPrefix(name, root+string(filepath.Separator)) ||
			(calls == 1 && filepath.Base(name) != "7zr.exe") ||
			(calls == 2 && filepath.Base(name) != "7z.exe") {
			t.Fatalf("installer or global executable invoked: %s %v", name, args)
		}
		output := strings.TrimPrefix(args[2], "-o")
		if calls == 1 {
			if args[1] != f.QEMUExtractor || len(args) != 6 {
				t.Fatalf("unexpected bootstrap invocation: %v", args)
			}
			if err := os.MkdirAll(output, 0o700); err != nil {
				return err
			}
			for _, name := range []string{"7z.exe", "7z.dll"} {
				if err := os.WriteFile(filepath.Join(output, name), []byte("extractor"), 0o600); err != nil {
					return err
				}
			}
		} else {
			if args[1] != f.QEMU || len(args) != 4 {
				t.Fatalf("unexpected QEMU invocation: %v", args)
			}
			writeQEMUFixture(t, output)
		}
		return nil
	})
	if err != nil || calls != 2 {
		t.Fatalf("stage failed: %v, calls=%d", err, calls)
	}
	for _, name := range []string{"qemu-system-x86_64.exe", "dependency.dll", "share/firmware.bin", "lib/data", "COPYING"} {
		if _, err := os.Stat(filepath.Join(dest, name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"qemu-system-arm.exe", "$PLUGINSDIR"} {
		if _, err := os.Stat(filepath.Join(dest, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unneeded installer payload copied: %s", name)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 2 {
		t.Fatalf("temporary extractors retained: %v %v", entries, err)
	}
}

func TestUpstreamQEMUExtractionFailureIsExplicitAndCleaned(t *testing.T) {
	root := t.TempDir()
	bootstrap := filepath.Join(root, "download")
	if err := os.WriteFile(bootstrap, []byte("extractor"), 0o600); err != nil {
		t.Fatal(err)
	}
	want := errors.New("extractor failed")
	err := stageQEMUInstallerWith(EngineFiles{QEMU: "qemu", QEMUBootstrap: bootstrap, QEMUExtractor: "7zip", QEMUVersion: "10.1.0"},
		filepath.Join(root, "stage"), func(context.Context, string, ...string) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("lost failure: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 {
		t.Fatalf("failed extractor retained: %v %v", entries, err)
	}
}

func TestUpstreamQEMURejectsIncompleteOrWrongVersion(t *testing.T) {
	for _, name := range []string{"VERSION", "libslirp-0.dll", "share/bios-256k.bin", "COPYING"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeQEMUFixture(t, root)
			if err := os.Remove(filepath.Join(root, name)); err != nil {
				t.Fatal(err)
			}
			if err := copyQEMUFiles(root, filepath.Join(t.TempDir(), "dest"), "10.1.0"); err == nil {
				t.Fatal("incomplete upstream installer accepted")
			}
		})
	}
	root := t.TempDir()
	writeQEMUFixture(t, root)
	if err := copyQEMUFiles(root, filepath.Join(t.TempDir(), "dest"), "10.2.0"); err == nil {
		t.Fatal("wrong QEMU version accepted")
	}
}

func TestUpstreamQEMURejectsLinks(t *testing.T) {
	root := t.TempDir()
	writeQEMUFixture(t, root)
	if err := os.Symlink(filepath.Join(root, "COPYING"), filepath.Join(root, "share", "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if err := copyQEMUFiles(root, filepath.Join(t.TempDir(), "dest"), "10.1.0"); err == nil {
		t.Fatal("QEMU symbolic link accepted")
	}
}

// Optional real upstream archive validation, without executing Windows code.
func TestUpstreamQEMURealInstallerLayout(t *testing.T) {
	installer := os.Getenv("CARE_TEST_QEMU_INSTALLER")
	extractor := os.Getenv("CARE_TEST_SEVENZIP_INSTALLER")
	bootstrap := os.Getenv("CARE_TEST_SEVENZIP_BOOTSTRAP")
	if installer == "" || extractor == "" || bootstrap == "" {
		t.Skip("set CARE_TEST_QEMU_INSTALLER, CARE_TEST_SEVENZIP_INSTALLER and CARE_TEST_SEVENZIP_BOOTSTRAP for upstream archive validation")
	}
	sevenzip, err := exec.LookPath("7zz")
	if err != nil {
		t.Fatal("7zz is required for non-Windows archive validation")
	}
	dest := filepath.Join(t.TempDir(), "qemu")
	f := EngineFiles{QEMU: installer, QEMUExtractor: extractor, QEMUBootstrap: bootstrap, QEMUVersion: "10.1.0"}
	err = stageQEMUInstallerWith(f, dest, func(ctx context.Context, _ string, args ...string) error {
		output, err := exec.CommandContext(ctx, sevenzip, args...).CombinedOutput()
		if err != nil {
			t.Logf("extractor: %s", output)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := requireQEMUFile(dest, "qemu-system-x86_64.exe"); err != nil {
		t.Fatal(err)
	}
}
