package hostfwd

import (
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"unsafe"

	"golang.org/x/sys/windows"
)

// addrInUse also covers WSAEACCES, which Windows returns for ports another
// process holds exclusively or that are in an excluded port range.
func addrInUse(err error) bool {
	return errors.Is(err, windows.WSAEADDRINUSE) || errors.Is(err, windows.WSAEACCES)
}

var (
	iphlpapi                = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetExtendedTCPTable = iphlpapi.NewProc("GetExtendedTcpTable")
)

const (
	tcpTableOwnerPIDListener = 3 // TCP_TABLE_OWNER_PID_LISTENER
	afInet                   = 2
)

// portOwner returns "name (pid N)" for the process listening on TCP port
// (IPv4), or "" when it cannot tell. PID 4 is the kernel's HTTP server
// (http.sys), which IIS and other web services register ports with.
func portOwner(port string) string {
	p, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return ""
	}
	pid, ok := listenerPID(uint16(p))
	if !ok {
		return ""
	}
	if pid == 4 {
		return "System (http.sys, e.g. IIS) (pid 4)"
	}
	if name := processName(pid); name != "" {
		return fmt.Sprintf("%s (pid %d)", name, pid)
	}
	return fmt.Sprintf("pid %d", pid)
}

func listenerPID(port uint16) (uint32, bool) {
	var size uint32
	for range 3 {
		buf := make([]byte, max(size, 4))
		r, _, _ := procGetExtendedTCPTable.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0, afInet, tcpTableOwnerPIDListener, 0)
		switch windows.Errno(r) {
		case windows.ERROR_INSUFFICIENT_BUFFER:
			continue
		case 0:
			return findListener(buf[:size], port)
		default:
			return 0, false
		}
	}
	return 0, false
}

// findListener scans a MIB_TCPTABLE_OWNER_PID: a DWORD count, then rows of
// state, local addr, local port (network order in the low 16 bits),
// remote addr, remote port and owning pid, each a DWORD.
func findListener(b []byte, port uint16) (uint32, bool) {
	const row = 24
	if len(b) < 4 {
		return 0, false
	}
	n := int(binary.LittleEndian.Uint32(b))
	for i := range n {
		off := 4 + i*row
		if off+row > len(b) {
			break
		}
		r := b[off : off+row]
		local := binary.BigEndian.Uint16(r[8:10])
		if local == port {
			return binary.LittleEndian.Uint32(r[20:24]), true
		}
	}
	return 0, false
}

func processName(pid uint32) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer func() { _ = windows.CloseHandle(h) }()
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return ""
	}
	return filepath.Base(windows.UTF16ToString(buf[:n]))
}
