//go:build unix

package logdir

import (
	"os"
	"syscall"
)

const extraOpenFlags = syscall.O_NONBLOCK

func chownFile(f *os.File, o *Owner) error { return f.Chown(o.UID, o.GID) }

func chownPath(root *os.Root, name string, o *Owner) error { return root.Lchown(name, o.UID, o.GID) }
