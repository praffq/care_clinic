//go:build !unix

package logdir

import "os"

const extraOpenFlags = 0

func chownFile(*os.File, *Owner) error { return nil }

func chownPath(*os.Root, string, *Owner) error { return nil }
