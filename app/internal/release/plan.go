package release

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SourceHash hashes tracked build inputs by relative name and content, not mtimes.
func SourceHash(root string, paths ...string) (string, error) {
	var files []string
	for _, path := range paths {
		err := filepath.WalkDir(filepath.Join(root, path), func(path string, d fs.DirEntry, err error) error {
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				return err
			}
			if d.IsDir() {
				switch d.Name() {
				case ".git", "node_modules", "dist", "install", "bin":
					return filepath.SkipDir
				}
				return nil
			}
			if d.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("build input symlink is not supported: %s", path)
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			if rel == "app/wails.json" {
				// Installer metadata is generated from versions.toml.
				return nil
			}
			files = append(files, rel)
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	sort.Strings(files)
	h := sha256.New()
	for _, path := range files {
		b, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", filepath.ToSlash(path), len(b))
		_, _ = h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func AssetForFile(path, releaseURL string) (Asset, error) {
	f, err := os.Open(path)
	if err != nil {
		return Asset{}, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return Asset{}, err
	}
	if n == 0 {
		return Asset{}, fmt.Errorf("asset %s is empty", path)
	}
	return Asset{
		URL:    strings.TrimRight(releaseURL, "/") + "/" + filepath.Base(path),
		SHA256: hex.EncodeToString(h.Sum(nil)), Size: n,
	}, nil
}
