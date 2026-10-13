package vm

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/sys/proc"
)

type qemuExtract func(context.Context, string, ...string) error

func stageQEMUInstaller(f EngineFiles, dest string) error {
	return stageQEMUInstallerWith(f, dest, func(ctx context.Context, name string, args ...string) error {
		return (proc.Runner{Ctx: ctx}).Run(name, args...)
	})
}

// Execute only the checksum-verified extractors, never either installer's code.
// Both installers are treated as archives; no registry, PATH or system install.
func stageQEMUInstallerWith(f EngineFiles, dest string, run qemuExtract) (err error) {
	if f.QEMUBootstrap == "" || f.QEMUExtractor == "" || f.QEMU == "" || f.QEMUVersion == "" {
		return errors.New("the QEMU installer or extraction tools are missing")
	}
	parent := filepath.Dir(dest)
	temp, err := os.MkdirTemp(parent, ".qemu-extract-")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(temp)) }()
	// Downloads have generated names without an .exe suffix. Use a private
	// executable name so Windows CreateProcess does not append the wrong suffix.
	bootstrap := filepath.Join(temp, "7zr.exe")
	if err := installFile(f.QEMUBootstrap, bootstrap); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	extractorDir := filepath.Join(temp, "extractor")
	if err := run(ctx, bootstrap, "x", f.QEMUExtractor, "-o"+extractorDir, "-y", "7z.exe", "7z.dll"); err != nil {
		return fmt.Errorf("could not prepare the QEMU extractor: %w", err)
	}
	for _, name := range []string{"7z.exe", "7z.dll"} {
		if err := requireQEMUFile(extractorDir, name); err != nil {
			return err
		}
	}
	unpacked := filepath.Join(temp, "qemu")
	if err := run(ctx, filepath.Join(extractorDir, "7z.exe"), "x", f.QEMU, "-o"+unpacked, "-y"); err != nil {
		return fmt.Errorf("could not extract the upstream QEMU installer: %w", err)
	}
	if err := copyQEMUFiles(unpacked, dest, f.QEMUVersion); err != nil {
		return fmt.Errorf("the upstream QEMU installer is incomplete: %w", err)
	}
	return nil
}

func requireQEMUFile(root, name string) error {
	info, err := os.Lstat(filepath.Join(root, name))
	if err != nil {
		return fmt.Errorf("QEMU file %s: %w", name, err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return fmt.Errorf("QEMU file %s is not a nonempty regular file", name)
	}
	return nil
}

func copyQEMUFiles(root, dest, version string) error {
	for _, name := range []string{
		"qemu-system-x86_64.exe", "libslirp-0.dll", "share/bios-256k.bin",
		"COPYING", "COPYING.LIB", "README.rst", "VERSION",
	} {
		if err := requireQEMUFile(root, name); err != nil {
			return err
		}
	}
	data, err := os.ReadFile(filepath.Join(root, "VERSION"))
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(data)) != version {
		return fmt.Errorf("QEMU version is %q, expected %s", strings.TrimSpace(string(data)), version)
	}
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == "." {
			return err
		}
		top := strings.Split(filepath.ToSlash(rel), "/")[0]
		selected := top == "share" || top == "lib" ||
			rel == "qemu-system-x86_64.exe" || rel == "VERSION" ||
			rel == "COPYING" || rel == "COPYING.LIB" || rel == "README.rst" ||
			!strings.Contains(filepath.ToSlash(rel), "/") && strings.HasSuffix(strings.ToLower(rel), ".dll")
		if !selected {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("QEMU contains a symbolic link: %s", rel)
		}
		target := filepath.Join(dest, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("QEMU contains a non-regular file: %s", rel)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		return installFile(path, target)
	})
}
