//go:build !windows

package hostfwd

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
	"time"
)

func addrInUse(err error) bool { return errors.Is(err, syscall.EADDRINUSE) }

// portOwner returns "command (pid N)" for the process listening on TCP
// port, or "" when lsof is missing or finds nothing.
func portOwner(port string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, "lsof", "-nP", "-iTCP:"+port, "-sTCP:LISTEN", "-Fpc").Output()
	return parseLsof(out)
}
