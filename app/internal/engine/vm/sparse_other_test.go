//go:build !unix

package vm

import (
	"os"
	"testing"
)

func assertSparse(*testing.T, string, os.FileInfo) {}
