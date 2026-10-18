package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// qemuDirName is the QEMU build shipped next to the Windows service binary.
const qemuDirName = "qemu"

// installBinaries replaces binDir with a fresh copy of exe (as name) and,
// when withQEMU is set, the qemu directory next to exe. The service runs
// from binDir, so app upgrades and uninstalls never pull files out from
// under it, and only administrators can change what it runs.
func installBinaries(exe, binDir, name string, withQEMU bool) error {
	if rel, err := filepath.Rel(binDir, exe); err == nil && filepath.IsLocal(rel) {
		return fmt.Errorf("%s is the installed copy; run the one shipped with the app", exe)
	}
	qemuSrc := filepath.Join(filepath.Dir(exe), qemuDirName)
	if withQEMU {
		if st, err := os.Stat(qemuSrc); err != nil || !st.IsDir() {
			return fmt.Errorf("QEMU not found at %s: reinstall CARE Clinic", qemuSrc)
		}
	}
	if err := os.RemoveAll(binDir); err != nil {
		return err
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}
	if err := copyFile(exe, filepath.Join(binDir, name), 0o755); err != nil {
		return err
	}
	if withQEMU {
		return copyTree(qemuSrc, filepath.Join(binDir, qemuDirName))
	}
	return nil
}

// copyTree copies the regular files and directories under src to dst;
// links and other special files are refused.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(out, 0o755)
		case d.Type().IsRegular():
			return copyFile(path, out, 0o644)
		default:
			return fmt.Errorf("%s: not a regular file", path)
		}
	})
}

func copyFile(src, dst string, perm os.FileMode) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, out.Close()) }()
	_, err = io.Copy(out, in)
	return err
}
