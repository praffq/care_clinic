//go:build linux && (386 || amd64 || arm || arm64 || loong64 || riscv64 || s390x)

package diskspace

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

func TestBtrfsFsInfoLayoutAndIdentity(t *testing.T) {
	var a btrfsFsInfoArgs
	if unsafe.Sizeof(a) != 1024 || unsafe.Offsetof(a.FSID) != 16 || unsafe.Offsetof(a.Flags) != 40 || unsafe.Offsetof(a.MetadataUUID) != 56 {
		t.Fatalf("struct layout does not match btrfs_ioctl_fs_info_args")
	}
	if btrfsIocFsInfo != 0x8400941f {
		t.Fatal("unexpected ioctl number")
	}
}

func TestBtrfsFilesystemIDUsesFilesystemUUIDNotSubvolumeFsid(t *testing.T) {
	old := btrfsFsInfo
	defer func() { btrfsFsInfo = old }()
	btrfsFsInfo = func(_ int, a *btrfsFsInfoArgs) error {
		a.FSID = [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
		return nil
	}
	dir := t.TempDir()
	a, err := btrfsFilesystemID(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := btrfsFilesystemID(filepath.Join(dir, "."))
	if a != b || a != "btrfs:0102030405060708090a0b0c0d0e0f10" {
		t.Fatalf("ids = %q %q", a, b)
	}
}

func TestBtrfsFilesystemIDReportsFailures(t *testing.T) {
	old := btrfsFsInfo
	defer func() { btrfsFsInfo = old }()
	boom := errors.New("ioctl failed")
	btrfsFsInfo = func(int, *btrfsFsInfoArgs) error { return boom }
	if _, err := btrfsFilesystemID(t.TempDir()); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	btrfsFsInfo = func(int, *btrfsFsInfoArgs) error { return nil }
	if _, err := btrfsFilesystemID(t.TempDir()); err == nil {
		t.Fatal("empty filesystem id accepted")
	}
	if _, err := btrfsFilesystemID(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing path accepted")
	}
}

// Supply two existing subvolumes of the same Btrfs filesystem. This check is
// read-only: it does not create subvolumes or mount filesystems.
func TestRealBtrfsSubvolumesShareIdentity(t *testing.T) {
	left, right := os.Getenv("CARE_TEST_BTRFS_SUBVOLUME_A"), os.Getenv("CARE_TEST_BTRFS_SUBVOLUME_B")
	if left == "" || right == "" {
		t.Skip("set CARE_TEST_BTRFS_SUBVOLUME_A and CARE_TEST_BTRFS_SUBVOLUME_B to distinct existing subvolumes of one filesystem")
	}
	var a, b unix.Statfs_t
	if err := unix.Statfs(left, &a); err != nil {
		t.Fatal(err)
	}
	if err := unix.Statfs(right, &b); err != nil {
		t.Fatal(err)
	}
	if int64(a.Type) != unix.BTRFS_SUPER_MAGIC || int64(b.Type) != unix.BTRFS_SUPER_MAGIC || a.Fsid == b.Fsid {
		t.Fatal("fixture requires Btrfs subvolumes with different raw statfs identities, not ordinary folders")
	}
	l, err := Of(left)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Of(right)
	if err != nil {
		t.Fatal(err)
	}
	if l.Volume != r.Volume || l.Volume == "" {
		t.Fatalf("volumes %q vs %q", l.Volume, r.Volume)
	}
}
