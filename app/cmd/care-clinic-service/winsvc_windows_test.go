package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"golang.org/x/sys/windows/svc"
)

func runHandler(t *testing.T, serve func(context.Context) error) (chan svc.ChangeRequest, chan svc.Status, chan [2]any, *handler) {
	t.Helper()
	req := make(chan svc.ChangeRequest)
	status := make(chan svc.Status, 16)
	out := make(chan [2]any, 1)
	h := &handler{serve: serve}
	go func() {
		special, code := h.Execute(nil, req, status)
		out <- [2]any{special, code}
	}()
	for _, want := range []svc.State{svc.StartPending, svc.Running} {
		if st := <-status; st.State != want {
			t.Fatalf("state %v, want %v", st.State, want)
		}
	}
	return req, status, out, h
}

func TestHandlerStop(t *testing.T) {
	for _, cmd := range []svc.Cmd{svc.Stop, svc.Shutdown, svc.PreShutdown} {
		stopped := make(chan struct{})
		req, status, out, h := runHandler(t, func(ctx context.Context) error {
			<-ctx.Done()
			<-stopped
			return nil
		})
		req <- svc.ChangeRequest{Cmd: svc.Interrogate, CurrentStatus: svc.Status{State: svc.Running}}
		if st := <-status; st.State != svc.Running {
			t.Fatalf("interrogate: %v", st.State)
		}
		req <- svc.ChangeRequest{Cmd: cmd}
		if st := <-status; st.State != svc.StopPending || st.WaitHint == 0 {
			t.Fatalf("%v: status %+v", cmd, st)
		}
		select {
		case <-out:
			t.Fatal("returned before serve stopped")
		case <-time.After(50 * time.Millisecond):
		}
		close(stopped)
		if r := <-out; r[0] != false || r[1] != uint32(0) || h.err != nil {
			t.Fatalf("%v: exit %v, err %v", cmd, r, h.err)
		}
	}
}

func TestHandlerServeFails(t *testing.T) {
	boom := errors.New("boom")
	_, _, out, h := runHandler(t, func(context.Context) error { return boom })
	if r := <-out; r[0] != true || r[1] != uint32(1) || !errors.Is(h.err, boom) {
		t.Fatalf("exit %v, err %v", r, h.err)
	}
}
