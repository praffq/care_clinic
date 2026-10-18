package vm

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/forward"
)

// The QEMU driver (Windows, WHPX) boots the same bundle as
// Virtualization.framework. QEMU has no vsock device there, so the agent is
// reached over mutual TLS: every boot gets a fresh CA, server and client
// certificate; the server bundle reaches the guest through fw_cfg
// (opt/care/tls, see engine/README.md) and QEMU's user-mode network forwards
// loopback-only host ports to the agent (47377) and its TLS relays
// (forward.Port.TCPPort). DialVsock maps the vsock ports the rest of the
// service uses onto those TLS ports.

// ErrHypervisorUnavailable means the hardware accelerator QEMU needs (WHPX
// on Windows) is not available. The driver never falls back to software
// emulation (TCG), which is far too slow to run the clinic.
var ErrHypervisorUnavailable = errors.New("hardware virtualization is not available")

// TLS bundle fw_cfg name; the agent reads /sys/firmware/qemu_fw_cfg/by_name/<name>/raw.
const qemuTLSName = "opt/care/tls"

// QEMUOptions configures the QEMU driver.
type QEMUOptions struct {
	// Binary is qemu-system-x86_64(.exe).
	Binary string
	// FirmwareDir is passed with -L (QEMU's share directory).
	FirmwareDir string
	// Machine, Accel and CPU select the machine type, accelerator and CPU
	// model, e.g. "pc", "whpx,kernel-irqchip=off", "host".
	Machine, Accel, CPU string
	// RunDir holds per-boot secrets (the fw_cfg TLS bundle); it must only be
	// readable by the service. It is created if missing.
	RunDir string
	// Probe, if set, checks the accelerator before booting; its error is
	// returned by Start.
	Probe func() error
	// Prepare, if set, adjusts the command before it starts (Windows: no
	// console window).
	Prepare func(*exec.Cmd)
	// Started, if set, is called with the running process, e.g. to put it
	// in a kill-on-close job object; release runs once QEMU has exited.
	Started func(*os.Process) (release func(), err error)
}

// NewQEMU returns a Machine that boots cfg with QEMU.
func NewQEMU(cfg Config, opt QEMUOptions) (Machine, error) {
	cfg = cfg.WithDefaults()
	errs := []error{cfg.Validate()}
	if opt.Binary == "" {
		errs = append(errs, errors.New("QEMU binary is required"))
	}
	if opt.Machine == "" || opt.Accel == "" || opt.CPU == "" {
		errs = append(errs, errors.New("QEMU machine, accelerator and CPU are required"))
	}
	if opt.RunDir == "" {
		errs = append(errs, errors.New("QEMU run directory is required"))
	}
	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("engine VM config: %w", err)
	}
	return &qemuMachine{cfg: cfg, opt: opt, done: make(chan struct{})}, nil
}

type qemuMachine struct {
	cfg Config
	opt QEMUOptions

	mu      sync.Mutex
	started bool
	state   State
	err     error
	proc    *os.Process
	forced  bool
	tls     *tls.Config
	// hostPorts maps the vsock ports DialVsock accepts to host loopback ports.
	hostPorts map[uint32]int

	finishOnce sync.Once
	done       chan struct{}
	cleanup    []func()
}

// qemuForward is one hostfwd: host loopback port -> guest TCP port, reached
// by DialVsock(vsock).
type qemuForward struct {
	vsock uint32
	guest int
	host  int
}

// qemuForwards returns the agent API and the clinic relays.
func qemuForwards() []qemuForward {
	fw := []qemuForward{{vsock: AgentVsockPort, guest: forward.TLSAgent}}
	for _, p := range forward.StandardPorts() {
		fw = append(fw, qemuForward{vsock: p.VsockPort, guest: int(p.TCPPort)})
	}
	return fw
}

func (m *qemuMachine) Start(ctx context.Context) (err error) {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return ErrAlreadyStarted
	}
	m.started, m.state = true, StateStarting
	m.mu.Unlock()
	defer func() {
		if err != nil {
			m.finish(StateError, err)
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.opt.Probe != nil {
		if err := m.opt.Probe(); err != nil {
			return err
		}
	}
	if err := ValidateBundle(m.cfg.BundleDir); err != nil {
		return err
	}
	if st, err := os.Stat(m.opt.Binary); err != nil || !st.Mode().IsRegular() {
		return fmt.Errorf("QEMU binary %s: not found (reinstall CARE Clinic)", m.opt.Binary)
	}
	if _, err := EnsureDataDisk(m.cfg.DataDisk, m.cfg.DataDiskSize); err != nil {
		return err
	}
	if err := checkDataDiskFree(m.cfg.DataDisk); err != nil {
		return err
	}

	creds, err := newQEMUCredentials()
	if err != nil {
		return err
	}
	bundle, err := m.writeBundle(creds.bundle)
	if err != nil {
		return err
	}
	fws := qemuForwards()
	ports, err := freeLoopbackPorts(len(fws))
	if err != nil {
		return err
	}
	hostPorts := map[uint32]int{}
	for i := range fws {
		fws[i].host = ports[i]
		hostPorts[fws[i].vsock] = ports[i]
	}

	console, err := m.startConsole()
	if err != nil {
		return err
	}
	cmd := exec.Command(m.opt.Binary, qemuArgs(m.cfg, m.opt, bundle, fws)...)
	cmd.Stdout, cmd.Stderr = console, console
	if m.opt.Prepare != nil {
		m.opt.Prepare(cmd)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start QEMU: %w", err)
	}
	release := func() {}
	if m.opt.Started != nil {
		if release, err = m.opt.Started(cmd.Process); err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return fmt.Errorf("start QEMU: %w", err)
		}
	}
	m.mu.Lock()
	m.proc, m.tls, m.hostPorts = cmd.Process, creds.client, hostPorts
	m.mu.Unlock()
	m.setState(StateRunning)
	go func() {
		werr := cmd.Wait()
		release()
		m.mu.Lock()
		forced := m.forced
		m.mu.Unlock()
		switch {
		case werr == nil || forced:
			m.finish(StateStopped, nil)
		default:
			m.finish(StateError, fmt.Errorf("%w: QEMU %v (see %s)", ErrVMError, werr, m.cfg.ConsoleLog))
		}
	}()
	return nil
}

// writeBundle writes the fw_cfg bundle to a private file, removed when the
// VM stops. QEMU reads it once at startup.
func (m *qemuMachine) writeBundle(data []byte) (string, error) {
	if err := os.MkdirAll(m.opt.RunDir, 0o700); err != nil {
		return "", fmt.Errorf("QEMU run directory: %w", err)
	}
	f, err := os.CreateTemp(m.opt.RunDir, "tls-*.pem")
	if err != nil {
		return "", fmt.Errorf("QEMU TLS bundle: %w", err)
	}
	path := f.Name()
	m.onFinish(func() { _ = os.Remove(path) })
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return "", fmt.Errorf("QEMU TLS bundle: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("QEMU TLS bundle: %w", err)
	}
	return path, nil
}

// startConsole opens the console log and returns a writer for QEMU's
// stdout/stderr (the serial console and QEMU's own errors).
func (m *qemuMachine) startConsole() (io.Writer, error) {
	log, err := m.cfg.openConsole()
	if err != nil {
		return nil, err
	}
	_, _ = fmt.Fprintf(log, "\n=== engine VM boot %s (%s, QEMU %s) ===\n", time.Now().Format(time.RFC3339), m.cfg.BundleDir, m.opt.Accel)
	m.onFinish(func() { _ = log.Close() })
	if m.cfg.ConsoleMirror == nil {
		return log, nil
	}
	return &mirrorWriter{log: log, mirror: m.cfg.ConsoleMirror}, nil
}

// mirrorWriter writes to log and, ignoring its errors, to mirror.
type mirrorWriter struct{ log, mirror io.Writer }

func (w *mirrorWriter) Write(p []byte) (int, error) {
	n, err := w.log.Write(p)
	_, _ = w.mirror.Write(p)
	return n, err
}

// qemuArgs builds QEMU's command line. Option values that hold paths are
// escaped (QEMU doubles commas).
func qemuArgs(cfg Config, opt QEMUOptions, bundle string, fws []qemuForward) []string {
	netdev := "user,id=net0"
	for _, f := range fws {
		netdev += fmt.Sprintf(",hostfwd=tcp:127.0.0.1:%d-:%d", f.host, f.guest)
	}
	args := []string{
		"-machine", opt.Machine,
		"-accel", opt.Accel,
		"-cpu", opt.CPU,
		"-m", strconv.FormatUint(cfg.MemoryMiB, 10),
		"-smp", strconv.FormatUint(uint64(cfg.CPUs), 10),
		"-display", "none",
		"-monitor", "none",
		"-serial", "stdio",
		"-no-reboot",
	}
	if opt.FirmwareDir != "" {
		args = append(args, "-L", opt.FirmwareDir)
	}
	return append(args,
		"-kernel", filepath.Join(cfg.BundleDir, KernelFile),
		"-initrd", filepath.Join(cfg.BundleDir, InitrdFile),
		"-append", cfg.Cmdline("ttyS0"),
		// Order matters: system.img is /dev/vda, the data disk /dev/vdb.
		"-drive", "file="+qemuEscape(filepath.Join(cfg.BundleDir, SystemImgFile))+",if=virtio,format=raw,readonly=on",
		"-drive", "file="+qemuEscape(cfg.DataDisk)+",if=virtio,format=raw,discard=unmap",
		"-fw_cfg", "name="+qemuTLSName+",file="+qemuEscape(bundle),
		"-netdev", netdev,
		"-device", "virtio-net-pci,netdev=net0",
		"-device", "virtio-rng-pci",
	)
}

func qemuEscape(s string) string { return strings.ReplaceAll(s, ",", ",,") }

// freeLoopbackPorts returns n distinct free TCP ports on 127.0.0.1. QEMU
// binds them afterwards; a port taken in between makes the boot's TLS dials
// fail closed (the certificates are pinned to this boot).
func freeLoopbackPorts(n int) ([]int, error) {
	var ls []net.Listener
	defer func() {
		for _, l := range ls {
			_ = l.Close()
		}
	}()
	ports := make([]int, 0, n)
	for range n {
		l, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			return nil, fmt.Errorf("pick a loopback port: %w", err)
		}
		ls = append(ls, l)
		ports = append(ports, l.Addr().(*net.TCPAddr).Port)
	}
	return ports, nil
}

type qemuCredentials struct {
	bundle []byte // CA cert, server cert, server key (PEM)
	client *tls.Config
}

// newQEMUCredentials creates a throwaway CA, a server certificate for
// 127.0.0.1 (the guest agent) and a client certificate (this service).
func newQEMUCredentials() (*qemuCredentials, error) {
	now := time.Now()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "care-clinic engine boot CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(10, 0, 0),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, err
	}
	issue := func(cn string, usage x509.ExtKeyUsage) ([]byte, []byte, error) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, nil, err
		}
		der, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
			SerialNumber: serial(),
			Subject:      pkix.Name{CommonName: cn},
			NotBefore:    now.Add(-time.Hour),
			NotAfter:     now.AddDate(10, 0, 0),
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{usage},
			IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		}, ca, &key.PublicKey, caKey)
		if err != nil {
			return nil, nil, err
		}
		kder, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return nil, nil, err
		}
		return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
			pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kder}), nil
	}
	srvCert, srvKey, err := issue("care-engine-agent", x509.ExtKeyUsageServerAuth)
	if err != nil {
		return nil, err
	}
	cliCert, cliKey, err := issue("care-clinic-service", x509.ExtKeyUsageClientAuth)
	if err != nil {
		return nil, err
	}
	pair, err := tls.X509KeyPair(cliCert, cliKey)
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	return &qemuCredentials{
		bundle: append(append(caPEM, srvCert...), srvKey...),
		client: &tls.Config{
			RootCAs:      roots,
			Certificates: []tls.Certificate{pair},
			MinVersion:   tls.VersionTLS13,
			ServerName:   "127.0.0.1",
		},
	}, nil
}

func serial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return big.NewInt(time.Now().UnixNano())
	}
	return n
}

func (m *qemuMachine) setState(s State) {
	m.mu.Lock()
	defer m.mu.Unlock()
	select {
	case <-m.done:
	default:
		m.state = s
	}
}

func (m *qemuMachine) onFinish(f func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanup = append(m.cleanup, f)
}

func (m *qemuMachine) finish(s State, err error) {
	m.finishOnce.Do(func() {
		m.mu.Lock()
		m.state, m.err = s, err
		cleanup := m.cleanup
		m.cleanup = nil
		m.mu.Unlock()
		for i := len(cleanup) - 1; i >= 0; i-- {
			cleanup[i]()
		}
		close(m.done)
	})
}

func (m *qemuMachine) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

func (m *qemuMachine) Done() <-chan struct{} { return m.done }

func (m *qemuMachine) Err() error {
	select {
	case <-m.done:
	default:
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.err
}

// DialVsock dials the mutual-TLS host port QEMU forwards for a vsock port:
// the agent API (AgentVsockPort) or a clinic relay (forward.StandardPorts).
func (m *qemuMachine) DialVsock(ctx context.Context, port uint32) (net.Conn, error) {
	m.mu.Lock()
	state, cfg, host, ok := m.state, m.tls, 0, false
	if m.hostPorts != nil {
		host, ok = m.hostPorts[port]
	}
	m.mu.Unlock()
	if state != StateRunning || cfg == nil {
		return nil, ErrNotRunning
	}
	if !ok {
		return nil, fmt.Errorf("vsock port %d is not forwarded by the QEMU driver", port)
	}
	d := &tls.Dialer{NetDialer: &net.Dialer{}, Config: cfg}
	c, err := d.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(host)))
	if err != nil {
		select {
		case <-m.done:
			return nil, ErrNotRunning
		default:
		}
		return nil, fmt.Errorf("vsock port %d: %w", port, err)
	}
	return c, nil
}

func (m *qemuMachine) Stop(ctx context.Context, timeout time.Duration) error {
	if m.State() == StateNew {
		return nil
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-m.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
	}
	return m.ForceStop(ctx)
}

func (m *qemuMachine) ForceStop(ctx context.Context) error {
	select {
	case <-m.done:
		return nil
	default:
	}
	m.mu.Lock()
	proc := m.proc
	if proc != nil {
		m.forced = true
	}
	m.mu.Unlock()
	if proc == nil {
		return ErrNotRunning
	}
	if err := proc.Kill(); err != nil {
		select {
		case <-m.done:
			return nil
		default:
		}
		if !errors.Is(err, os.ErrProcessDone) {
			return fmt.Errorf("force-stop VM: %w", err)
		}
	}
	select {
	case <-m.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
