package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"
	"unsafe"

	"github.com/ohcnetwork/care_desktop/app/internal/service/hostlogs"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const serviceUsage = `usage: care-clinic-service service <command>

commands (from an elevated prompt):
  install [--root dir] [--group name] [--log-user SID] [--no-qemu]
                 copy this binary and qemu\ into <root>\bin and register the
                 CareClinic service (LocalSystem, delayed automatic start,
                 restarted on failure) and open TCP 80/443 and UDP 5353 in
                 Windows Firewall; re-running it updates the service.
                 --log-user records whose log folder
                 (%LOCALAPPDATA%\care-clinic\logs) the service writes to
  uninstall      stop and remove the service, its firewall rules and
                 %ProgramData%\CARE Clinic\bin (the clinic's data is kept)
  start | stop   start or stop the service
  status         print the service state
`

// rootSDDL gives SYSTEM and Administrators full control of the state root
// and everything under it, and nobody else any access.
const rootSDDL = "O:BAD:PAI(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"

const (
	serviceExe = "care-clinic-service.exe"
	// preshutdownTimeout lets the engine stop cleanly when Windows shuts down.
	preshutdownTimeout = 120 * time.Second
	stopTimeout        = 150 * time.Second
)

func serviceCmd(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, serviceUsage)
		return 2
	}
	var err error
	switch args[0] {
	case "install":
		err = serviceInstall(args[1:])
	case "uninstall":
		err = serviceUninstall()
	case "start":
		err = withService(func(s *mgr.Service) error { return startService(s) })
	case "stop":
		err = withService(stopService)
	case "status":
		err = withService(func(s *mgr.Service) error {
			st, err := s.Query()
			if err == nil {
				fmt.Println(stateName(st.State))
			}
			return err
		})
	default:
		fmt.Fprint(os.Stderr, serviceUsage)
		return 2
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "care-clinic-service service:", err)
		return 1
	}
	return 0
}

func serviceInstall(args []string) error {
	fs := flag.NewFlagSet("service install", flag.ContinueOnError)
	root := fs.String("root", systemRoot, "state directory")
	group := fs.String("group", "care-clinic", "local group whose members may use the control pipe")
	logUser := fs.String("log-user", "", "SID of the user whose log folder receives the service, engine and container logs")
	noQEMU := fs.Bool("no-qemu", false, "do not copy qemu\\ (for testing the service without an engine)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := requireAdmin(); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err := secureDir(*root); err != nil {
		return err
	}
	if *logUser != "" {
		if err := hostlogs.WriteUser(*root, *logUser); err != nil {
			return err
		}
	}
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer func() { _ = m.Disconnect() }()
	s, err := m.OpenService(serviceName)
	exists := err == nil
	if exists {
		defer func() { _ = s.Close() }()
		// The running service holds its binaries open.
		if err := stopService(s); err != nil {
			return err
		}
	}
	bin := filepath.Join(*root, "bin")
	if err := installBinaries(exe, bin, serviceExe, !*noQEMU); err != nil {
		return err
	}
	cfg := mgr.Config{
		DisplayName:      "CARE Clinic",
		Description:      "Runs the CARE engine VM and serves CARE on this computer's network.",
		StartType:        mgr.StartAutomatic,
		DelayedAutoStart: true,
		ErrorControl:     mgr.ErrorNormal,
		ServiceType:      windows.SERVICE_WIN32_OWN_PROCESS,
	}
	svcArgs := []string{"--root", *root, "--group", *group, "--log-dir", filepath.Join(*root, "logs")}
	if exists {
		cfg.BinaryPathName = quoteCommandLine(append([]string{filepath.Join(bin, serviceExe)}, svcArgs...))
		cfg.ServiceStartName = "LocalSystem"
		if err := s.UpdateConfig(cfg); err != nil {
			return err
		}
	} else {
		s, err = m.CreateService(serviceName, filepath.Join(bin, serviceExe), cfg, svcArgs...)
		if err != nil {
			return err
		}
		defer func() { _ = s.Close() }()
	}
	if err := configureRecovery(s); err != nil {
		return err
	}
	return openFirewall(filepath.Join(bin, serviceExe))
}

// quoteCommandLine builds a command line the way mgr.CreateService does.
func quoteCommandLine(args []string) string {
	out := ""
	for i, a := range args {
		if i > 0 {
			out += " "
		}
		out += windows.EscapeArg(a)
	}
	return out
}

// configureRecovery restarts the service after crashes and error exits
// (5 s, 10 s, then every 30 s; the count resets after a day) and gives it
// time to stop the engine at system shutdown.
func configureRecovery(s *mgr.Service) error {
	actions := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
	}
	if err := s.SetRecoveryActions(actions, uint32((24 * time.Hour).Seconds())); err != nil {
		return err
	}
	if err := s.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		return err
	}
	info := struct{ Timeout uint32 }{uint32(preshutdownTimeout.Milliseconds())}
	return windows.ChangeServiceConfig2(s.Handle, windows.SERVICE_CONFIG_PRESHUTDOWN_INFO, (*byte)(unsafe.Pointer(&info)))
}

func serviceUninstall() error {
	closeFirewall()
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer func() { _ = m.Disconnect() }()
	s, err := m.OpenService(serviceName)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()
	if err := stopService(s); err != nil {
		return err
	}
	if err := s.Delete(); err != nil {
		return err
	}
	// The binaries install copied; the clinic's data stays.
	return os.RemoveAll(filepath.Join(systemRoot, "bin"))
}

func withService(f func(*mgr.Service) error) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer func() { _ = m.Disconnect() }()
	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("open %s service: %w", serviceName, err)
	}
	defer func() { _ = s.Close() }()
	return f(s)
}

func startService(s *mgr.Service) error {
	err := s.Start()
	if errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
		return nil
	}
	return err
}

// stopService stops s and waits for it to exit (the engine needs up to
// about 90 s to shut down).
func stopService(s *mgr.Service) error {
	st, err := s.Query()
	if err != nil {
		return err
	}
	if st.State == svc.Stopped {
		return nil
	}
	if st.State != svc.StopPending {
		if _, err := s.Control(svc.Stop); err != nil && !errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
			return err
		}
	}
	deadline := time.Now().Add(stopTimeout)
	for {
		st, err := s.Query()
		if err != nil {
			return err
		}
		if st.State == svc.Stopped {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s did not stop within %s", serviceName, stopTimeout)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func stateName(s svc.State) string {
	switch s {
	case svc.Stopped:
		return "stopped"
	case svc.StartPending:
		return "starting"
	case svc.StopPending:
		return "stopping"
	case svc.Running:
		return "running"
	case svc.ContinuePending, svc.PausePending, svc.Paused:
		return "paused"
	}
	return fmt.Sprintf("state %d", s)
}

// secureDir creates dir owned by Administrators with access for SYSTEM and
// Administrators only, or resets an existing dir to that. A dir some other
// user owns is refused: ProgramData lets any user create folders, and one
// planted there before the first install must not become the service's.
func secureDir(dir string) error {
	sd, err := windows.SecurityDescriptorFromString(rootSDDL)
	if err != nil {
		return err
	}
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return err
	}
	err = windows.CreateDirectory(p, &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd})
	if err == nil {
		return nil
	}
	if !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	cur, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := cur.Owner()
	if err != nil {
		return err
	}
	if !trustedOwner(owner) {
		return fmt.Errorf("%s is owned by %s, not an administrator; remove it and install again", dir, owner)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	ba, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		ba, nil, dacl, nil)
}

func trustedOwner(sid *windows.SID) bool {
	return sid.IsWellKnown(windows.WinLocalSystemSid) || sid.IsWellKnown(windows.WinBuiltinAdministratorsSid)
}
