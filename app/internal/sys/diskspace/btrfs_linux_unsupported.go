//go:build linux && !(386 || amd64 || arm || arm64 || loong64 || riscv64 || s390x)

package diskspace

import "fmt"

func btrfsFilesystemID(path string) (string, error) {
	return "", fmt.Errorf("identify btrfs filesystem of %s: not supported on this architecture", path)
}
