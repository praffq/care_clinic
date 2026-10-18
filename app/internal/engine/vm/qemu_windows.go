package vm

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/ohcnetwork/care_desktop/app/internal/sys/whpx"
)

// WHPX settings, from the containers_anywhere WHPX diagnosis (Windows 11
// 25H2): the default WHPX irqchip can stop the guest with "Unexpected VP
// exit code 4", so QEMU emulates only the interrupt controller
// (kernel-irqchip=off) while the CPU stays hardware-accelerated. Never TCG.
const (
	WHPXMachine = "pc"
	WHPXAccel   = "whpx,kernel-irqchip=off"
	WHPXCPU     = "host"
)

// QEMUBinary is the QEMU executable inside Config.QEMUDir.
const QEMUBinary = "qemu-system-x86_64.exe"

// New returns a QEMU/WHPX Machine for cfg.
func New(cfg Config) (Machine, error) {
	dir := cfg.QEMUDir
	if dir == "" {
		exe, err := os.Executable()
		if err != nil {
			return nil, err
		}
		dir = filepath.Join(filepath.Dir(exe), "qemu")
	}
	return NewQEMU(cfg, QEMUOptions{
		Binary:      filepath.Join(dir, QEMUBinary),
		FirmwareDir: filepath.Join(dir, "share"),
		Machine:     WHPXMachine,
		Accel:       WHPXAccel,
		CPU:         WHPXCPU,
		RunDir:      filepath.Join(filepath.Dir(cfg.DataDisk), "run"),
		Probe:       CheckWHPX,
		Prepare: func(cmd *exec.Cmd) {
			cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
		},
		Started: killOnClose,
	})
}

// CheckWHPX returns ErrHypervisorUnavailable, with what to do, unless the
// Windows Hypervisor Platform is usable.
func CheckWHPX() error {
	if err := whpx.Check(); err != nil {
		return fmt.Errorf("%w: %w", ErrHypervisorUnavailable, err)
	}
	return nil
}

// killOnClose puts QEMU in a job object that kills it when the job handle
// closes, so a crashed service never leaves a VM holding the data disk.
func killOnClose(p *os.Process) (func(), error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("job object: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return nil, fmt.Errorf("job object: %w", err)
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(p.Pid))
	if err != nil {
		_ = windows.CloseHandle(job)
		return nil, fmt.Errorf("open QEMU process: %w", err)
	}
	defer func() { _ = windows.CloseHandle(h) }()
	if err := windows.AssignProcessToJobObject(job, h); err != nil {
		_ = windows.CloseHandle(job)
		return nil, fmt.Errorf("job object: %w", err)
	}
	return func() { _ = windows.CloseHandle(job) }, nil
}
