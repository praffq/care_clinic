//go:build linux && (386 || amd64 || arm || arm64 || loong64 || riscv64 || s390x)

package diskspace

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// btrfsIocFsInfo is BTRFS_IOC_FS_INFO, _IOR(0x94, 31, 1024-byte args). It is
// only defined for architectures using the generic ioctl encoding.
const btrfsIocFsInfo = 0x8000_0000 | 1024<<16 | 0x94<<8 | 31

// btrfsFsInfoArgs mirrors struct btrfs_ioctl_fs_info_args (1024 bytes).
type btrfsFsInfoArgs struct {
	MaxID        uint64
	NumDevices   uint64
	FSID         [16]byte
	Nodesize     uint32
	Sectorsize   uint32
	CloneAlign   uint32
	CsumType     uint16
	CsumSize     uint16
	Flags        uint64
	Generation   uint64
	MetadataUUID [16]byte
	Reserved     [944]byte
}

// btrfsFsInfo is replaceable in tests.
var btrfsFsInfo = func(fd int, args *btrfsFsInfoArgs) error {
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), btrfsIocFsInfo, uintptr(unsafe.Pointer(args)))
	if errno != 0 {
		return errno
	}
	return nil
}

func btrfsIdentity(args *btrfsFsInfoArgs) string {
	return fmt.Sprintf("btrfs:%x", args.FSID)
}

func btrfsFilesystemID(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("identify btrfs filesystem of %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	var args btrfsFsInfoArgs
	if err := btrfsFsInfo(int(f.Fd()), &args); err != nil {
		return "", fmt.Errorf("identify btrfs filesystem of %s: %w", path, err)
	}
	if args.FSID == ([16]byte{}) {
		return "", fmt.Errorf("identify btrfs filesystem of %s: empty filesystem id", path)
	}
	return btrfsIdentity(&args), nil
}
