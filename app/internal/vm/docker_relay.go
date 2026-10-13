package vm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"syscall"
	"time"
)

type dockerRelay struct {
	gate chan struct{}
	dial func(context.Context) (net.Conn, error)
}

func newDockerRelay() *dockerRelay {
	return &dockerRelay{
		gate: make(chan struct{}, 1),
		dial: func(ctx context.Context) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", qemuTCPDocker))
		},
	}
}

func (r *dockerRelay) connect(parent context.Context) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	select {
	case r.gate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-r.gate }()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		conn, err := r.dial(ctx)
		if err != nil && !errors.Is(err, syscall.ECONNREFUSED) {
			return nil, err
		}
		// libslirp's listen backlog is one. Pace only connection establishment;
		// successful streams remain concurrent and carry the original TLS bytes.
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			if conn != nil {
				_ = conn.Close()
			}
			return nil, ctx.Err()
		case <-timer.C:
		}
		if err == nil {
			return conn, nil
		}
		// No bytes have been sent: retrying this refused dial cannot replay an
		// HTTP request or duplicate a Docker operation.
	}
}
