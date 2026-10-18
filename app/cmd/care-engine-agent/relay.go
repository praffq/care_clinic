package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/forward"
)

const (
	relayMaxConns    = 512
	relayDialTimeout = 5 * time.Second
)

// startRelays listens for each port (through listen) and splices
// every connection with its fixed guest TCP address; the client never picks
// the destination. A port that cannot be bound is logged and skipped so the
// agent API stays up. The returned function waits, once ctx is done, for the
// relays to close their listeners and connections.
func startRelays(ctx context.Context, listen func(p forward.Port) (net.Listener, error), ports []forward.Port) (wait func()) {
	var wg sync.WaitGroup
	for _, p := range ports {
		l, err := listen(p)
		if err != nil {
			log.Printf("relay %s disabled: %v", p.Name, err)
			continue
		}
		log.Printf("relaying %s %s to %s", l.Addr().Network(), l.Addr(), p.GuestAddr)
		target := p.GuestAddr
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := forward.Serve(ctx, l, func(ctx context.Context) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "tcp", target)
			}, forward.Options{
				Name:        "relay " + p.Name,
				MaxConns:    relayMaxConns,
				DialTimeout: relayDialTimeout,
				Logf:        log.Printf,
			})
			if err != nil {
				log.Printf("relay %s stopped: %v", p.Name, err)
			}
		}()
	}
	return wg.Wait
}

// tlsRelayListener listens for each port on its guest TCP port with mutual
// TLS (QEMU, where hostfwd reaches it).
func tlsRelayListener(cfg *tls.Config) func(forward.Port) (net.Listener, error) {
	return func(p forward.Port) (net.Listener, error) {
		if p.TCPPort == 0 {
			return nil, fmt.Errorf("port %s has no TCP relay port", p.Name)
		}
		return listenTLS(cfg, fmt.Sprintf(":%d", p.TCPPort))
	}
}
