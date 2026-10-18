package control

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"unsafe"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// Listen creates the control pipe. Its DACL is the access check (see
// ListenConfig.Group); a pipe that already exists (another service
// instance, or a program squatting the name) is an error.
func Listen(cfg ListenConfig) (net.Listener, error) {
	log := cfg.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	var owners, clients []string
	if cfg.Group == "" {
		u, err := windows.GetCurrentProcessToken().GetTokenUser()
		if err != nil {
			return nil, fmt.Errorf("control pipe: current user: %w", err)
		}
		owners = append(owners, u.User.Sid.String())
	} else {
		group, members, err := groupSIDs(cfg.Group)
		if err != nil {
			return nil, fmt.Errorf("control pipe group: %w", err)
		}
		clients = append([]string{group}, members...)
		log.Info("control pipe access", "group", cfg.Group, "members", len(members))
	}
	l, err := winio.ListenPipe(cfg.Path, &winio.PipeConfig{
		SecurityDescriptor: pipeSDDL(owners, clients),
		InputBufferSize:    64 << 10,
		OutputBufferSize:   64 << 10,
	})
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_PIPE_BUSY) {
		return nil, fmt.Errorf("%s is in use: is another care-clinic-service running? (%w)", cfg.Path, err)
	}
	return l, err
}

// groupSIDs returns the SID of the local group name and of its members.
func groupSIDs(name string) (group string, members []string, err error) {
	sid, _, typ, err := windows.LookupSID("", name)
	if err != nil {
		return "", nil, fmt.Errorf("look up %q: %w", name, err)
	}
	if typ != windows.SidTypeAlias && typ != windows.SidTypeGroup && typ != windows.SidTypeWellKnownGroup {
		return "", nil, fmt.Errorf("%q is not a group", name)
	}
	members, err = localGroupMembers(name)
	if err != nil {
		return "", nil, err
	}
	return sid.String(), members, nil
}

var procNetLocalGroupGetMembers = windows.NewLazySystemDLL("netapi32.dll").NewProc("NetLocalGroupGetMembers")

// localGroupMembers lists a local group's direct members (LOCALGROUP_MEMBERS_INFO_0).
func localGroupMembers(name string) ([]string, error) {
	group, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	var out []string
	var resume uintptr
	for {
		var buf *byte
		var read, total uint32
		r, _, _ := procNetLocalGroupGetMembers.Call(0, uintptr(unsafe.Pointer(group)), 0,
			uintptr(unsafe.Pointer(&buf)), ^uintptr(0) /* MAX_PREFERRED_LENGTH */, uintptr(unsafe.Pointer(&read)),
			uintptr(unsafe.Pointer(&total)), uintptr(unsafe.Pointer(&resume)))
		if r != 0 && windows.Errno(r) != windows.ERROR_MORE_DATA {
			return nil, fmt.Errorf("members of %q: %w", name, windows.Errno(r))
		}
		if buf != nil {
			for _, sid := range unsafe.Slice((**windows.SID)(unsafe.Pointer(buf)), read) {
				if sid != nil && sid.IsValid() {
					out = append(out, sid.String())
				}
			}
			_ = windows.NetApiBufferFree(buf)
		}
		if windows.Errno(r) != windows.ERROR_MORE_DATA {
			return out, nil
		}
	}
}
