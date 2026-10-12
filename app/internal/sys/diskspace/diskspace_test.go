package diskspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOfWalksUpToAnExistingFolder(t *testing.T) {
	dir := t.TempDir()
	u, err := Of(filepath.Join(dir, "not", "made", "yet"))
	if err != nil {
		t.Fatal(err)
	}
	if u.Path != dir {
		t.Fatalf("path = %q, want %q", u.Path, dir)
	}
	if u.Total == 0 || u.Free > u.Total {
		t.Fatalf("implausible usage: %+v", u)
	}
	if u.Volume == "" {
		t.Fatalf("no volume id: %+v", u)
	}
	same, err := Of(dir)
	if err != nil {
		t.Fatal(err)
	}
	if same.Volume != u.Volume {
		t.Fatalf("volume ids differ for the same folder: %q vs %q", same.Volume, u.Volume)
	}
}

func TestOfUsesTheSameFilesystemIDForSeparateDirectories(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	if err := os.Mkdir(left, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(right, 0o700); err != nil {
		t.Fatal(err)
	}
	leftUsage, err := Of(left)
	if err != nil {
		t.Fatal(err)
	}
	rightUsage, err := Of(right)
	if err != nil {
		t.Fatal(err)
	}
	if leftUsage.Volume != rightUsage.Volume {
		t.Fatalf("same-filesystem directories have different IDs: %q vs %q", leftUsage.Volume, rightUsage.Volume)
	}
}

func TestOfRejectsEmptyPath(t *testing.T) {
	if _, err := Of(""); err == nil {
		t.Fatal("expected an error for an empty path")
	}
}
