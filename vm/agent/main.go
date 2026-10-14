// Command care-engine-agent runs inside the CARE engine VM. It gives the host
// an operation API and file transfers, and forwards the clinic's web ports.
//
// On macOS the host reaches it over vsock. On Windows, QEMU forwards loopback
// TCP ports to it and both ends prove themselves with a certificate the host
// generates per install and hands over through QEMU's fw_cfg.
package main

import (
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"time"
)

// Ports are shared with app/internal/vm/protocol.go; keep them in step.
const (
	VsockControl = 2377
	VsockHTTP    = 80
	VsockHTTPS   = 443

	TCPControl = 2377

	dockerSocket = "/run/docker.sock"
	credsPath    = "/sys/firmware/qemu_fw_cfg/by_name/opt/care/tls/raw"
)

func main() {
	creds := flag.String("tls", credsPath, "PEM certificate and key for TCP mode")
	socket := flag.String("socket", "", "protected Unix socket for native Linux mode")
	flag.Parse()
	log.SetFlags(0)
	var err error
	if *socket != "" {
		err = runNative(*socket)
	} else {
		err = run(*creds)
	}
	if err != nil {
		log.Fatal(err)
	}
}

func run(credsFile string) error {
	api := newAPI(dockerSocket)
	defer api.closeIdleConnections()
	srv := &http.Server{Handler: api.routes(), ReadHeaderTimeout: 10 * time.Second}
	errs := make(chan error, 4)

	pem, err := os.ReadFile(credsFile)
	switch {
	case err == nil:
		cfg, err := serverTLS(pem)
		if err != nil {
			return err
		}
		control, err := tls.Listen("tcp", fmt.Sprintf(":%d", TCPControl), cfg)
		if err != nil {
			return err
		}
		log.Printf("agent: TLS control :%d", TCPControl)
		go func() { errs <- srv.Serve(control) }()
	case errors.Is(err, os.ErrNotExist):
		listen := func(port uint32) (net.Listener, error) { return listenVsock(port) }
		control, err := listen(VsockControl)
		if err != nil {
			return err
		}
		web, err := listen(VsockHTTP)
		if err != nil {
			return err
		}
		secure, err := listen(VsockHTTPS)
		if err != nil {
			return err
		}
		log.Printf("agent: vsock mode")
		go func() { errs <- srv.Serve(control) }()
		go func() { errs <- serveProxy(web, tcpTarget("127.0.0.1:80")) }()
		go func() { errs <- serveProxy(secure, tcpTarget("127.0.0.1:443")) }()
	default:
		return err
	}
	return <-errs
}
