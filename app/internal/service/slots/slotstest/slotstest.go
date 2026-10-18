// Package slotstest builds engine bundle tarballs for tests.
package slotstest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/service/slots"
)

// Entry is one tarball entry for WriteTestBundle.
type Entry struct {
	Name     string
	Body     []byte
	Type     byte // tar.TypeReg when zero
	Linkname string
}

// BundleEntries returns the entries of a valid bundle for version and arch
// with small fake boot files.
func BundleEntries(version, arch string) []Entry {
	top := "care-engine-" + version + "-" + arch
	files := map[string][]byte{
		"vmlinuz":    []byte("kernel " + version),
		"initramfs":  []byte("initramfs " + version),
		"system.img": []byte("system " + version),
	}
	m := slots.Manifest{Version: version, Arch: arch, DataSchema: 1, Files: map[string]string{}}
	for name, b := range files {
		sum := sha256.Sum256(b)
		m.Files[name] = hex.EncodeToString(sum[:])
	}
	mb, _ := json.Marshal(m)
	return []Entry{
		{Name: top + "/", Type: tar.TypeDir},
		{Name: top + "/manifest.json", Body: mb},
		{Name: top + "/vmlinuz", Body: files["vmlinuz"]},
		{Name: top + "/initramfs", Body: files["initramfs"]},
		{Name: top + "/system.img", Body: files["system.img"]},
	}
}

// WriteTestBundle writes entries as a .tar.gz under t.TempDir and returns
// its path and SHA-256.
func WriteTestBundle(t testing.TB, entries []Entry) (string, string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		typ := e.Type
		if typ == 0 {
			typ = tar.TypeReg
		}
		hdr := &tar.Header{Name: e.Name, Typeflag: typ, Mode: 0o644, Linkname: e.Linkname}
		if typ == tar.TypeReg {
			hdr.Size = int64(len(e.Body))
		}
		if typ == tar.TypeDir {
			hdr.Mode = 0o755
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(e.Body); err != nil && typ == tar.TypeReg {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "bundle.tar.gz")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(buf.Bytes())
	return path, hex.EncodeToString(sum[:])
}
