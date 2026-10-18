//go:build !unix

package agent

import "os"

func inodeOf(os.FileInfo) uint64 { return 0 }
