package logdir

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	AppFolder     = "care-clinic"
	MaxFileBytes  = 5 << 20
	KeepRotated   = 2
	MaxAge        = 7 * 24 * time.Hour
	Budget        = 100 << 20
	ContainersDir = "containers"
)

func Default(goos, home string, getenv func(string) string) string {
	if home == "" {
		return ""
	}
	switch goos {
	case "darwin":
		return filepath.Join(home, "Library", "Logs", AppFolder)
	case "windows":
		base := getenv("LOCALAPPDATA")
		if base == "" {
			base = filepath.Join(home, "AppData", "Local")
		}
		return filepath.Join(base, AppFolder, "logs")
	default:
		base := getenv("XDG_STATE_HOME")
		if base == "" {
			base = filepath.Join(home, ".local", "state")
		}
		return filepath.Join(base, AppFolder)
	}
}

func RotatedName(name string, n int) string {
	if n == 0 {
		return name
	}
	ext := filepath.Ext(name)
	return strings.TrimSuffix(name, ext) + "." + strconv.Itoa(n) + ext
}

func rotationIndex(name string) int {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	dot := strings.LastIndexByte(stem, '.')
	if dot < 0 {
		return 0
	}
	n, err := strconv.Atoi(stem[dot+1:])
	if err != nil || n < 1 {
		return 0
	}
	return n
}

type Owner struct {
	UID int
	GID int
}

type Dir struct {
	path  string
	root  *os.Root
	owner *Owner
}

func Open(path string, owner *Owner) (*Dir, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("log folder %q is not an absolute path", path)
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if fi.Mode().Type() != fs.ModeDir {
		return nil, fmt.Errorf("log folder %s is not a plain folder (symlinks are refused)", path)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	return &Dir{path: path, root: root, owner: owner}, nil
}

func OpenOrCreate(path string, owner *Owner) (*Dir, error) {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return nil, err
	}
	return Open(path, owner)
}

func (d *Dir) Path() string { return d.path }

func (d *Dir) Owner() *Owner { return d.owner }

func (d *Dir) Close() error { return d.root.Close() }

func (d *Dir) EnsureDir(name string) error {
	fi, err := d.root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		if err := d.root.Mkdir(name, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		if d.owner != nil {
			_ = chownPath(d.root, name, d.owner)
		}
		fi, err = d.root.Lstat(name)
	}
	if err != nil {
		return err
	}
	if fi.Mode().Type() != fs.ModeDir {
		return fmt.Errorf("%s in the log folder is not a plain folder", name)
	}
	return nil
}

func (d *Dir) OpenAppend(name string) (*os.File, error) {
	if fi, err := d.root.Lstat(name); err == nil && !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s in the log folder is not a regular file", name)
	}
	f, err := d.root.OpenFile(name, os.O_WRONLY|os.O_APPEND|os.O_CREATE|extraOpenFlags, 0o644)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		_ = f.Close()
		if err == nil {
			err = fmt.Errorf("%s in the log folder is not a regular file", name)
		}
		return nil, err
	}
	if d.owner != nil {
		_ = chownFile(f, d.owner)
	}
	return f, nil
}

func (d *Dir) Rename(oldname, newname string) error { return d.root.Rename(oldname, newname) }

func (d *Dir) Remove(name string) error { return d.root.Remove(name) }

func (d *Dir) Lstat(name string) (fs.FileInfo, error) { return d.root.Lstat(name) }

func (d *Dir) Truncate(name string) error {
	fi, err := d.root.Lstat(name)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s in the log folder is not a regular file", name)
	}
	f, err := d.root.OpenFile(name, os.O_WRONLY|extraOpenFlags, 0)
	if err != nil {
		return err
	}
	err = f.Truncate(0)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func (d *Dir) ReadDir(name string) ([]fs.DirEntry, error) {
	f, err := d.root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return f.ReadDir(-1)
}
