package vm

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestDockerRelayRetriesOnlyRefusedDials(t *testing.T) {
	r := newDockerRelay()
	var calls int
	client, server := net.Pipe()
	defer func() { _ = server.Close() }()
	r.dial = func(context.Context) (net.Conn, error) {
		calls++
		if calls < 3 {
			return nil, &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}
		}
		return client, nil
	}
	conn, err := r.connect(context.Background())
	if err != nil || calls != 3 || conn != client {
		t.Fatalf("connect: %v, calls: %d", err, calls)
	}
	_ = conn.Close()

	calls = 0
	r.dial = func(context.Context) (net.Conn, error) {
		calls++
		return nil, syscall.EACCES
	}
	if _, err := r.connect(context.Background()); !errors.Is(err, syscall.EACCES) || calls != 1 {
		t.Fatalf("unexpected retry of non-refused error: %v, calls: %d", err, calls)
	}
}

func TestDockerRelaySerializesDialsNotStreams(t *testing.T) {
	r := newDockerRelay()
	var active, peak atomic.Int32
	r.dial = func(context.Context) (net.Conn, error) {
		n := active.Add(1)
		for old := peak.Load(); n > old; old = peak.Load() {
			if peak.CompareAndSwap(old, n) {
				break
			}
		}
		time.Sleep(time.Millisecond)
		client, server := net.Pipe()
		go func() {
			defer func() { _ = server.Close() }()
			_, _ = server.Write([]byte("ok"))
		}()
		active.Add(-1)
		return client, nil
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	connected := make(chan struct{}, 8)
	read := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for range 8 {
		wg.Go(func() {
			<-start
			conn, err := r.connect(ctx)
			connected <- struct{}{}
			if err != nil {
				t.Error(err)
				return
			}
			defer func() { _ = conn.Close() }()
			<-read
			_ = conn.SetReadDeadline(time.Now().Add(time.Second))
			buf := make([]byte, 2)
			if n, err := conn.Read(buf); err != nil || n != 2 || string(buf) != "ok" {
				t.Errorf("stream failed: %d %v %q", n, err, buf)
			}
		})
	}
	close(start)
	for range 8 {
		select {
		case <-connected:
		case <-ctx.Done():
			t.Error("connection establishment blocked on an existing stream")
		}
	}
	close(read)
	wg.Wait()
	if peak.Load() != 1 {
		t.Fatalf("concurrent upstream dials: %d", peak.Load())
	}
}

func TestDockerRelayCancellation(t *testing.T) {
	for _, queued := range []bool{false, true} {
		r := newDockerRelay()
		if queued {
			r.gate <- struct{}{}
		}
		r.dial = func(context.Context) (net.Conn, error) {
			return nil, syscall.ECONNREFUSED
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
		_, err := r.connect(ctx)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("queued=%v: %v", queued, err)
		}
		if !queued && len(r.gate) != 0 {
			t.Fatal("cancelled dial retained the gate")
		}
	}
}

func TestForwardClosesActiveStreamsOnStop(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	up, server := net.Pipe()
	defer func() { _ = server.Close() }()
	attached := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go forward(ctx, ln, func(context.Context) (net.Conn, error) {
		close(attached)
		return up, nil
	}, t.Logf)
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	<-attached
	cancel()
	_ = server.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := server.Read(make([]byte, 1)); err == nil {
		t.Fatal("upstream was not closed")
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("upstream remained open after stop")
	}
}
