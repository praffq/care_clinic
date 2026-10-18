package control

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// SystemSocket is the installed service's control pipe.
const SystemSocket = `\\.\pipe\care-clinic-control`

func dialSocket(ctx context.Context, path string) (net.Conn, error) {
	c, err := winio.DialPipeContext(ctx, path)
	if err != nil {
		return nil, err
	}
	if path == SystemSocket {
		// Any local user could create this pipe name while the service is
		// stopped; only talk to one created by SYSTEM or an administrator.
		if err := checkPipeOwner(c, trustedOwner); err != nil {
			_ = c.Close()
			return nil, err
		}
	}
	return c, nil
}

func trustedOwner(sid *windows.SID) bool {
	return sid.IsWellKnown(windows.WinLocalSystemSid) || sid.IsWellKnown(windows.WinBuiltinAdministratorsSid)
}

// checkPipeOwner verifies the owner of the pipe c is connected to.
func checkPipeOwner(c net.Conn, ok func(*windows.SID) bool) error {
	f, isFile := c.(interface{ Fd() uintptr })
	if !isFile {
		return errors.New("control pipe: no handle to check its owner")
	}
	sd, err := windows.GetSecurityInfo(windows.Handle(f.Fd()), windows.SE_KERNEL_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("control pipe owner: %w", err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return fmt.Errorf("control pipe owner: %w", err)
	}
	if owner == nil || !ok(owner) {
		return fmt.Errorf("control pipe is owned by %v, not the CARE Clinic service; is another program using its name?", owner)
	}
	return nil
}
