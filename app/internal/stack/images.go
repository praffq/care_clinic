package stack

import (
	"archive/tar"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
)

// devTarballs maps roles to the tarball names scripts/dev-images.sh writes.
var devTarballs = map[protocol.ImageRole]string{
	protocol.ImageRoleBackend:  "care-backend-A",
	protocol.ImageRoleFrontend: "care-frontend-A",
	protocol.ImageRoleCaddy:    "care-caddy",
	protocol.ImageRolePostgres: "postgres",
	protocol.ImageRoleRedis:    "redis",
	protocol.ImageRoleSilo:     "silo",
	protocol.ImageRoleBackup:   "care-backup",
}

// DevImages returns the stack images in dir, as written by
// scripts/dev-images.sh: <name>.tar plus <name>.tar.digest. overrides maps a
// role to another tarball name, e.g. backend → care-backend-B.
func DevImages(dir string, overrides map[protocol.ImageRole]string) ([]Image, error) {
	var out []Image
	for _, role := range RequiredRoles {
		name := devTarballs[role]
		if o := overrides[role]; o != "" {
			name = o
		}
		tarball := filepath.Join(dir, name+".tar")
		d, err := os.ReadFile(tarball + ".digest")
		if err != nil {
			return nil, fmt.Errorf("%s image: %w (run scripts/dev-images.sh)", role, err)
		}
		dg := protocol.Digest(strings.TrimSpace(string(d)))
		if !dg.Valid() {
			return nil, fmt.Errorf("%s.digest: invalid digest %q", tarball, dg)
		}
		repo, err := tarballRepo(tarball)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", tarball, err)
		}
		out = append(out, Image{Role: role, Repo: repo, Digest: dg, Tarball: tarball})
	}
	return out, nil
}

// tarballRepo returns the repository (no tag) an OCI image archive was saved
// under, from its index.json name annotation.
func tarballRepo(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	tr := tar.NewReader(f)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return "", errors.New("no index.json in image archive")
		}
		if err != nil {
			return "", err
		}
		if h.Name != "index.json" {
			continue
		}
		var idx struct {
			Manifests []struct {
				Annotations map[string]string `json:"annotations"`
			} `json:"manifests"`
		}
		if err := json.NewDecoder(io.LimitReader(tr, 1<<20)).Decode(&idx); err != nil {
			return "", fmt.Errorf("index.json: %w", err)
		}
		for _, m := range idx.Manifests {
			if name := m.Annotations["io.containerd.image.name"]; name != "" {
				return stripTag(name), nil
			}
		}
		return "", errors.New("index.json names no image")
	}
}

// stripTag drops a ":tag" after the last path component.
func stripTag(ref string) string {
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		return ref[:i]
	}
	return ref
}
