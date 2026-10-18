package vm

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/forward"
)

// The test binary doubles as a fake QEMU (CARE_FAKE_QEMU=serve|exit3): it
// records its arguments, reads the fw_cfg TLS bundle and serves mutual TLS
// on every hostfwd port like the guest agent and its relays would.
func TestMain(m *testing.M) {
	if mode := os.Getenv("CARE_FAKE_QEMU"); mode != "" {
		os.Exit(fakeQEMU(mode, os.Args[1:]))
	}
	os.Exit(m.Run())
}

func fakeQEMU(mode string, args []string) int {
	if out := os.Getenv("CARE_FAKE_QEMU_ARGS"); out != "" {
		b, _ := json.Marshal(args)
		_ = os.WriteFile(out, b, 0o600)
	}
	fmt.Println("fake console")
	fmt.Fprintln(os.Stderr, "fake stderr")
	if mode == "exit3" {
		return 3
	}
	var bundle string
	var fwds [][2]int
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "-fw_cfg":
			_, file, _ := strings.Cut(args[i+1], ",file=")
			bundle = strings.ReplaceAll(file, ",,", ",")
		case "-netdev":
			for _, opt := range strings.Split(args[i+1], ",") {
				spec, ok := strings.CutPrefix(opt, "hostfwd=tcp:127.0.0.1:")
				if !ok {
					continue
				}
				h, g, _ := strings.Cut(spec, "-:")
				hp, _ := strconv.Atoi(h)
				gp, _ := strconv.Atoi(g)
				fwds = append(fwds, [2]int{hp, gp})
			}
		}
	}
	data, err := os.ReadFile(bundle)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fw_cfg:", err)
		return 1
	}
	var blocks []*pem.Block
	for b, rest := pem.Decode(data); b != nil; b, rest = pem.Decode(rest) {
		blocks = append(blocks, b)
	}
	if len(blocks) != 3 {
		fmt.Fprintln(os.Stderr, "bad bundle")
		return 1
	}
	ca, err := x509.ParseCertificate(blocks[0].Bytes)
	if err != nil {
		return 1
	}
	pair, err := tls.X509KeyPair(pem.EncodeToMemory(blocks[1]), pem.EncodeToMemory(blocks[2]))
	if err != nil {
		return 1
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	cfg := &tls.Config{Certificates: []tls.Certificate{pair}, ClientCAs: pool, ClientAuth: tls.RequireAndVerifyClientCert, MinVersion: tls.VersionTLS13}
	for _, f := range fwds {
		l, err := tls.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(f[0])), cfg)
		if err != nil {
			fmt.Fprintln(os.Stderr, "listen:", err)
			return 1
		}
		if f[1] == forward.TLSAgent {
			mux := http.NewServeMux()
			mux.HandleFunc("/v1/status", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })
			mux.HandleFunc("/v1/shutdown", func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusAccepted)
				go func() { time.Sleep(100 * time.Millisecond); os.Exit(0) }()
			})
			go func() { _ = (&http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}).Serve(l) }()
			continue
		}
		guest := f[1]
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				go func() {
					defer func() { _ = c.Close() }()
					_, _ = fmt.Fprintf(c, "%d\n", guest)
					_, _ = io.Copy(c, c)
				}()
			}
		}()
	}
	select {}
}

type fakeQEMUEnv struct {
	cfg  Config
	opt  QEMUOptions
	args string
}

func newFakeQEMU(t *testing.T, mode string) fakeQEMUEnv {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	args := filepath.Join(dir, "args.json")
	t.Setenv("CARE_FAKE_QEMU", mode)
	t.Setenv("CARE_FAKE_QEMU_ARGS", args)
	return fakeQEMUEnv{
		cfg: Config{
			BundleDir:    writeBundle(t),
			DataDisk:     filepath.Join(dir, "data, disk.img"),
			DataDiskSize: 1 << 20,
			ConsoleLog:   filepath.Join(dir, "console.log"),
		},
		opt: QEMUOptions{
			Binary: exe, Machine: "pc", Accel: "whpx,kernel-irqchip=off", CPU: "host",
			RunDir: filepath.Join(dir, "run"),
		},
		args: args,
	}
}

func startFake(t *testing.T, e fakeQEMUEnv) Machine {
	t.Helper()
	m, err := NewQEMU(e.cfg, e.opt)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = m.ForceStop(ctx)
	})
	return m
}

func waitDone(t *testing.T, m Machine) {
	t.Helper()
	select {
	case <-m.Done():
	case <-time.After(20 * time.Second):
		t.Fatal("VM did not stop")
	}
}

func agentClient(m Machine) *http.Client {
	return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return m.DialVsock(ctx, AgentVsockPort) },
	}}
}

// waitAgent retries until the fake has bound its listeners.
func waitAgent(t *testing.T, m Machine) {
	t.Helper()
	c := agentClient(m)
	deadline := time.Now().Add(20 * time.Second)
	for {
		resp, err := c.Get("http://agent/v1/status")
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if string(b) != "ok" {
				t.Fatalf("status = %q", b)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("agent never answered: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestQEMUArgs(t *testing.T) {
	e := newFakeQEMU(t, "serve")
	cfg := e.cfg.WithDefaults()
	e.opt.FirmwareDir = `C:\qemu\share`
	args := qemuArgs(cfg, e.opt, `C:\run,x\tls.pem`, []qemuForward{{vsock: 1024, guest: 47377, host: 5001}, {vsock: 8080, guest: 47378, host: 5002}})
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-machine pc", "-accel whpx,kernel-irqchip=off", "-cpu host", "-m 4096", "-smp 2",
		"-serial stdio", "-no-reboot", `-L C:\qemu\share`,
		"-append console=ttyS0 care.system=/dev/vda care.data=/dev/vdb",
		`-fw_cfg name=opt/care/tls,file=C:\run,,x\tls.pem`,
		"-netdev user,id=net0,hostfwd=tcp:127.0.0.1:5001-:47377,hostfwd=tcp:127.0.0.1:5002-:47378",
		"data,, disk.img,if=virtio,format=raw,discard=unmap",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "tcg") {
		t.Errorf("args fall back to TCG: %s", joined)
	}
	sys := slices.Index(args, "file="+filepath.Join(cfg.BundleDir, SystemImgFile)+",if=virtio,format=raw,readonly=on")
	data := slices.IndexFunc(args, func(a string) bool { return strings.Contains(a, "discard=unmap") })
	if sys < 0 || data < sys {
		t.Errorf("system.img must be the first drive (vda): %v", args)
	}
}

func TestQEMUForwardsEveryPort(t *testing.T) {
	fws := qemuForwards()
	if len(fws) != 1+len(forward.StandardPorts()) || fws[0].vsock != AgentVsockPort || fws[0].guest != forward.TLSAgent {
		t.Fatalf("forwards = %+v", fws)
	}
	for _, f := range fws[1:] {
		if f.guest == 0 {
			t.Errorf("forward %+v has no guest TCP port", f)
		}
	}
}

func TestQEMUBootDialShutdown(t *testing.T) {
	e := newFakeQEMU(t, "serve")
	m := startFake(t, e)
	if m.State() != StateRunning {
		t.Fatalf("state = %v", m.State())
	}
	waitAgent(t, m)

	for _, p := range forward.StandardPorts() {
		c, err := m.DialVsock(context.Background(), p.VsockPort)
		if err != nil {
			t.Fatalf("dial %s: %v", p.Name, err)
		}
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		line := make([]byte, 6)
		if _, err := io.ReadFull(c, line); err != nil || string(line) != strconv.Itoa(int(p.TCPPort))+"\n" {
			t.Fatalf("%s reached %q, %v; want guest port %d", p.Name, line, err, p.TCPPort)
		}
		_ = c.Close()
	}
	if _, err := m.DialVsock(context.Background(), 9999); err == nil {
		t.Fatal("dialed an unforwarded port")
	}
	entries, _ := os.ReadDir(e.opt.RunDir)
	if len(entries) != 1 {
		t.Fatalf("run dir = %v, want the TLS bundle", entries)
	}

	resp, err := agentClient(m).Post("http://agent/v1/shutdown", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	waitDone(t, m)
	if m.Err() != nil || m.State() != StateStopped {
		t.Fatalf("after power-off: state %v err %v", m.State(), m.Err())
	}
	if entries, _ := os.ReadDir(e.opt.RunDir); len(entries) != 0 {
		t.Errorf("TLS bundle left behind: %v", entries)
	}
	if _, err := m.DialVsock(context.Background(), AgentVsockPort); !errors.Is(err, ErrNotRunning) {
		t.Errorf("dial after stop: %v", err)
	}
	log, _ := os.ReadFile(e.cfg.ConsoleLog)
	if !strings.Contains(string(log), "fake console") || !strings.Contains(string(log), "fake stderr") {
		t.Errorf("console log = %q", log)
	}
	var args []string
	b, _ := os.ReadFile(e.args)
	if err := json.Unmarshal(b, &args); err != nil || !slices.Contains(args, "whpx,kernel-irqchip=off") {
		t.Errorf("args = %v (%v)", args, err)
	}
}

func TestQEMUForceStop(t *testing.T) {
	m := startFake(t, newFakeQEMU(t, "serve"))
	waitAgent(t, m)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := m.Stop(ctx, 10*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	waitDone(t, m)
	if m.Err() != nil || m.State() != StateStopped {
		t.Fatalf("after force stop: state %v err %v", m.State(), m.Err())
	}
}

func TestQEMUCrash(t *testing.T) {
	m := startFake(t, newFakeQEMU(t, "exit3"))
	waitDone(t, m)
	if !errors.Is(m.Err(), ErrVMError) || m.State() != StateError {
		t.Fatalf("after exit 3: state %v err %v", m.State(), m.Err())
	}
}

func TestQEMUProbeFailsBeforeBoot(t *testing.T) {
	e := newFakeQEMU(t, "serve")
	e.opt.Probe = func() error { return fmt.Errorf("%w: enable WHPX", ErrHypervisorUnavailable) }
	m, err := NewQEMU(e.cfg, e.opt)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(context.Background()); !errors.Is(err, ErrHypervisorUnavailable) {
		t.Fatalf("Start = %v", err)
	}
	if _, err := os.Stat(e.args); !os.IsNotExist(err) {
		t.Error("QEMU ran although the accelerator is missing")
	}
	if _, err := os.Stat(e.cfg.DataDisk); !os.IsNotExist(err) {
		t.Error("data disk created although the accelerator is missing")
	}
	select {
	case <-m.Done():
	default:
		t.Error("Done not closed after a failed Start")
	}
	if err := m.Start(context.Background()); !errors.Is(err, ErrAlreadyStarted) {
		t.Errorf("second Start = %v", err)
	}
}

func TestQEMUMissingBinary(t *testing.T) {
	e := newFakeQEMU(t, "serve")
	e.opt.Binary = filepath.Join(t.TempDir(), "qemu-system-x86_64.exe")
	m, err := NewQEMU(e.cfg, e.opt)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "QEMU binary") {
		t.Fatalf("Start = %v", err)
	}
}

func TestNewQEMURejectsIncompleteOptions(t *testing.T) {
	if _, err := NewQEMU(validConfig(), QEMUOptions{}); err == nil {
		t.Fatal("empty options accepted")
	}
}
