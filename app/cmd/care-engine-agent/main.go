// Command care-engine-agent runs inside the engine VM and serves the engine
// protocol (internal/engine/agent) to the host service.
//
// Listeners:
//   - AF_VSOCK port 1024 (Virtualization.framework on macOS), always;
//   - TCP :47377 with mutual TLS, only when QEMU passes a TLS bundle through
//     fw_cfg (see DefaultTLSBundle). There is never a plain TCP listener.
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
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/agent"
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

// tlsListener returns the mutual-TLS TCP listener, or nil when bundlePath
// does not exist.
func tlsListener(bundlePath, addr string) (net.Listener, error) {
	data, err := os.ReadFile(bundlePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read tls bundle: %w", err)
	}
	cfg, err := parseTLSBundle(data)
	if err != nil {
		return nil, err
	}
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	return tls.NewListener(l, cfg), nil
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
	tl, err := tlsListener(*tlsBundle, *tlsAddr)
	if err != nil {
		log.Fatalf("mutual-TLS listener: %v", err)
	}
	if tl != nil {
		listeners = append(listeners, tl)
	} else {
		log.Printf("no TLS bundle at %s; TCP listener disabled", *tlsBundle)
	}
	if len(listeners) == 0 {
		log.Fatal("no listeners configured")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
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
		os.Exit(1)
	case <-ctx.Done():
		log.Print("stopping")
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
		wg.Wait()
	}
}
