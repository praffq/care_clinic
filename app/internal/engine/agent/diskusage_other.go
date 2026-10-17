//go:build !unix

package agent

import "errors"

func diskUsage(string) (free, total uint64, err error) {
	return 0, 0, errors.New("disk usage not supported on this platform")
}
