package main

import (
	"context"
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

// startRelays listens on each port's vsock port (through listen) and splices
// every connection with its fixed guest TCP address; the client never picks
// the destination. A port that cannot be bound is logged and skipped so the
// agent API stays up. The returned function waits, once ctx is done, for the
// relays to close their listeners and connections.
func startRelays(ctx context.Context, listen func(port uint32) (net.Listener, error), ports []forward.Port) (wait func()) {
	var wg sync.WaitGroup
	for _, p := range ports {
		l, err := listen(p.VsockPort)
		if err != nil {
			log.Printf("relay %s disabled: %v", p.Name, err)
			continue
		}
		log.Printf("relaying vsock port %d to %s", p.VsockPort, p.GuestAddr)
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
