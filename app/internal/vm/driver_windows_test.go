//go:build windows

package vm

import (
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestQEMUArgsUseConservativeWHPXCPU(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	m := Manifest{
		dir:     filepath.Join(t.TempDir(), "engine"),
		Kernel:  "kernel",
		Initrd:  "initrd",
		System:  "/nix/store/care-system",
		Cmdline: "console=hvc0 panic=1",
	}
	c := Config{CPUs: 4, MemoryMB: 4096}
	creds := filepath.Join(certDir(), "guest.pem")
	args := qemuArgs(m, c, creds)
	for flag, want := range map[string]string{
		"-machine": "q35",
		"-accel":   "whpx,kernel-irqchip=off",
		"-cpu":     "Nehalem",
		"-smp":     strconv.Itoa(c.CPUs),
		"-m":       strconv.Itoa(c.MemoryMB),
		"-kernel":  m.path(m.Kernel),
		"-initrd":  m.path(m.Initrd),
		"-append":  m.commandLine("ttyS0"),
		"-fw_cfg":  "name=opt/care/tls,file=" + qemuEscape(creds),
	} {
		i := slices.Index(args, flag)
		if i < 0 || i+1 >= len(args) || args[i+1] != want {
			t.Errorf("%s: want %q in %q", flag, want, args)
		}
	}
	for _, want := range []string{
		"virtio-blk-pci,drive=care-system,serial=care-system",
		"virtio-blk-pci,drive=care-data,serial=care-data",
		"virtio-net-pci,netdev=net0",
		"hostfwd=tcp:127.0.0.1:" + strconv.Itoa(qemuTCPDocker) + "-:" + strconv.Itoa(guestTCPDocker),
		"hostfwd=tcp:127.0.0.1:" + strconv.Itoa(hostTCPControl) + "-:" + strconv.Itoa(guestTCPControl),
	} {
		if !strings.Contains(strings.Join(args, "\n"), want) {
			t.Errorf("missing %q in %q", want, args)
		}
		if strings.Contains(strings.Join(args, "\n"), "hostfwd=tcp:127.0.0.1:"+strconv.Itoa(hostTCPDocker)+"-") {
			t.Fatal("QEMU must not own the public Docker relay port")
		}
	}
}
