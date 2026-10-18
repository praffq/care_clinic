// Command care-engine-agent runs inside the engine VM and serves the engine
// protocol (internal/engine/agent) to the host service.
//
// Listeners:
//   - AF_VSOCK port 1024 (Virtualization.framework on macOS), always;
//   - TCP :47377 with mutual TLS, only when QEMU passes a TLS bundle through
//     fw_cfg (see DefaultTLSBundle). There is never a plain TCP listener.
//
// The agent also relays the fixed ports in forward.StandardPorts to the
// clinic's Caddy on guest loopback (80, 443, 8480), so the host can expose
// them without a host-visible VM IP: on vsock ports with vsock, and on
// mutual-TLS TCP ports (TCPPort) with the TLS bundle. QEMU's hostfwd carries
// the TLS ports to host loopback; TLS keeps other local users of the host
// from reaching the guest (and spoofing PROXY headers) through them.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/agent"
	"github.com/ohcnetwork/care_desktop/app/internal/engine/forward"
)

var version = "dev"

const (
	DefaultVsockPort = 1024
	DefaultTLSListen = ":47377"
)

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.code = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// logRequests writes one line per request to the service log.
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Printf("%s %s %s %d %s", r.RemoteAddr, r.Method, r.URL.Path, rec.code, time.Since(start).Round(time.Millisecond))
	})
}

// loadTLS returns the mutual-TLS server config, or nil when bundlePath does
// not exist.
func loadTLS(bundlePath string) (*tls.Config, error) {
	data, err := os.ReadFile(bundlePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read tls bundle: %w", err)
	}
	return parseTLSBundle(data)
}

// tlsListener returns the mutual-TLS TCP listener, or nil when bundlePath
// does not exist.
func tlsListener(bundlePath, addr string) (net.Listener, error) {
	cfg, err := loadTLS(bundlePath)
	if err != nil || cfg == nil {
		return nil, err
	}
	return listenTLS(cfg, addr)
}

func listenTLS(cfg *tls.Config, addr string) (net.Listener, error) {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	return tls.NewListener(l, cfg), nil
}

// containerdStoreType is DriverStatus "driver-type" when dockerd uses the
// containerd image store, which image digests rely on.
const containerdStoreType = "io.containerd.snapshotter.v1"

// logImageStore waits for dockerd and logs which image store it uses.
func logImageStore(ctx context.Context) {
	for {
		out, err := exec.CommandContext(ctx, "docker", "info", "--format", "{{.Driver}} {{json .DriverStatus}}").Output()
		if err == nil {
			info := strings.TrimSpace(string(out))
			if strings.Contains(info, containerdStoreType) {
				log.Printf("docker image store: containerd (%s)", info)
			} else {
				log.Printf("WARNING: docker is not using the containerd image store; image digests will not match (%s)", info)
			}
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func main() {
	vsockPort := flag.Uint("vsock-port", DefaultVsockPort, "AF_VSOCK listen port (0 disables)")
	tlsBundle := flag.String("tls-bundle", DefaultTLSBundle, "PEM bundle (CA cert, server cert, server key) enabling the mutual-TLS TCP listener")
	tlsAddr := flag.String("tls-listen", DefaultTLSListen, "TCP listen address for mutual TLS")
	flag.Parse()
	log.SetFlags(log.LstdFlags | log.LUTC)

	srv := &http.Server{
		Handler: logRequests(agent.NewServer(agent.Config{
			EngineVersion: version,
			ShutdownDelay: 500 * time.Millisecond,
		}, &agent.DockerExecutor{})),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	var listeners []net.Listener
	if *vsockPort != 0 {
		l, err := listenVsock(uint32(*vsockPort))
		if err != nil {
			log.Fatal(err)
		}
		listeners = append(listeners, l)
	}
	tlsCfg, err := loadTLS(*tlsBundle)
	if err != nil {
		log.Fatalf("mutual-TLS listener: %v", err)
	}
	if tlsCfg != nil {
		tl, err := listenTLS(tlsCfg, *tlsAddr)
		if err != nil {
			log.Fatalf("mutual-TLS listener: %v", err)
		}
		listeners = append(listeners, tl)
	} else {
		log.Printf("no TLS bundle at %s; TCP listener disabled", *tlsBundle)
	}
	if len(listeners) == 0 {
		log.Fatal("no listeners configured")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go logImageStore(ctx)
	var relays []func()
	if *vsockPort != 0 {
		relays = append(relays, startRelays(ctx, func(p forward.Port) (net.Listener, error) {
			return listenVsock(p.VsockPort)
		}, forward.StandardPorts()))
	}
	if tlsCfg != nil {
		relays = append(relays, startRelays(ctx, tlsRelayListener(tlsCfg), forward.StandardPorts()))
	}
	waitRelays := func() {
		for _, w := range relays {
			w()
		}
	}
	errc := make(chan error, len(listeners))
	var wg sync.WaitGroup
	for _, l := range listeners {
		log.Printf("care-engine-agent %s listening on %s %s", version, l.Addr().Network(), l.Addr())
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := srv.Serve(l); !errors.Is(err, http.ErrServerClosed) {
				errc <- fmt.Errorf("serve %s: %w", l.Addr(), err)
			}
		}()
	}

	select {
	case err := <-errc:
		log.Print(err)
		_ = srv.Close()
		wg.Wait()
		stop()
		waitRelays()
		os.Exit(1)
	case <-ctx.Done():
		log.Print("stopping")
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
		wg.Wait()
		waitRelays()
	}
}
