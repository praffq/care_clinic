package logdir

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func noEnv(string) string { return "" }

func Prepare(home, dir string, owner *Owner) error {
	if !filepath.IsAbs(home) || !filepath.IsAbs(dir) {
		return fmt.Errorf("log folder %q is not an absolute path", dir)
	}
	rel, err := filepath.Rel(home, dir)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("log folder %s is not inside the home folder %s", dir, home)
	}
	if err := checkComponent(home, owner); err != nil {
		return err
	}
	cur := home
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		_, err := os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			if err := os.Mkdir(cur, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
				return err
			}
			if owner != nil {
				_ = chownDir(cur, owner)
			}
		} else if err != nil {
			return err
		}
		if err := checkComponent(cur, owner); err != nil {
			return err
		}
	}
	return nil
}

func checkComponent(path string, owner *Owner) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if fi.Mode().Type() != fs.ModeDir {
		return fmt.Errorf("%s is not a plain folder (symlinks and junctions are refused)", path)
	}
	if !ownedBy(fi, owner) {
		return fmt.Errorf("%s belongs to another account", path)
	}
	return nil
}
