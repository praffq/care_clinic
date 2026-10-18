package hostfwd

import (
	"encoding/binary"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestFindListener(t *testing.T) {
	b := make([]byte, 4+2*24)
	binary.LittleEndian.PutUint32(b, 2)
	row := func(i int, port uint16, pid uint32) {
		r := b[4+i*24:]
		binary.BigEndian.PutUint16(r[8:10], port)
		binary.LittleEndian.PutUint32(r[20:24], pid)
	}
	row(0, 135, 900)
	row(1, 443, 4)
	if pid, ok := findListener(b, 443); !ok || pid != 4 {
		t.Fatalf("443 -> %d %v", pid, ok)
	}
	if _, ok := findListener(b, 80); ok {
		t.Fatal("found a listener on 80")
	}
	if _, ok := findListener(b[:10], 443); ok {
		t.Fatal("read past a short table")
	}
}

func TestPortOwnerAndInUse(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	owner := portOwner(port)
	if !strings.Contains(owner, "pid "+strconv.Itoa(os.Getpid())) {
		t.Fatalf("owner %q", owner)
	}
	_, err = net.Listen("tcp4", "127.0.0.1:"+port)
	if err == nil || !addrInUse(err) {
		t.Fatalf("second listen: %v", err)
	}
	if e := describeBindError("127.0.0.1:"+port, err); !strings.Contains(e.Error(), "already in use by") {
		t.Fatalf("%v", e)
	}
}
