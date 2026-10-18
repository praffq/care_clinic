package bundle

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func appLayout(t *testing.T) (Layout, string) {
	root := filepath.Join(t.TempDir(), "CARE Clinic.app", "Contents")
	macos := filepath.Join(root, "MacOS")
	if err := os.MkdirAll(macos, 0o755); err != nil {
		t.Fatal(err)
	}
	return Layout{MacOSDir: macos}, root
}

func TestServicePath(t *testing.T) {
	l, _ := appLayout(t)
	if _, err := l.ServicePath(); err == nil {
		t.Fatal("found a missing binary")
	}
	bin := filepath.Join(l.MacOSDir, ServiceBinaryName)
	if err := os.WriteFile(bin, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := l.ServicePath(); err == nil {
		t.Fatal("accepted a non-executable binary")
	}
	_ = os.Chmod(bin, 0o755)
	if got, err := l.ServicePath(); err != nil || got != bin {
		t.Fatalf("got %q %v", got, err)
	}
}

func TestEngine(t *testing.T) {
	l, root := appLayout(t)
	if _, err := l.Engine(); !errors.Is(err, ErrNoEngine) {
		t.Fatalf("err = %v", err)
	}
	dir := filepath.Join(root, "Resources", "engine")
	_ = os.MkdirAll(dir, 0o755)
	data := []byte("engine")
	sum := sha256.Sum256(data)
	want := hex.EncodeToString(sum[:])
	tarball := filepath.Join(dir, "care-engine-0.2.0-arm64.tar.gz")
	if err := os.WriteFile(tarball, data, 0o644); err != nil {
		t.Fatal(err)
	}
	e, err := l.Engine()
	if err != nil || e.Version != "0.2.0" || e.SHA256 != want || e.Size != int64(len(data)) {
		t.Fatalf("hashed: %+v %v", e, err)
	}
	listed := "ab" + want[2:]
	if err := os.WriteFile(filepath.Join(dir, sumsName), []byte(listed+"  care-engine-0.2.0-arm64.tar.gz\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if e, err = l.Engine(); err != nil || e.SHA256 != listed {
		t.Fatalf("listed: %+v %v", e, err)
	}
	l.EngineBundle = filepath.Join(t.TempDir(), "engine.tgz")
	if _, err := l.Engine(); err == nil {
		t.Fatal("accepted a badly named bundle")
	}
}
