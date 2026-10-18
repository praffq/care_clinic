package main

import (
	"context"
	"time"

	"golang.org/x/sys/windows/svc"
)

const serviceName = "CareClinic"

// stopCheckpoint is how often a stopping service tells the SCM it is still
// making progress; stopping the stack and the VM can take about 90 s.
const stopCheckpoint = 10 * time.Second

// handler runs serve under the service control manager.
type handler struct {
	serve func(ctx context.Context) error
	err   error // what serve returned
}

// Execute implements svc.Handler. Stop, shutdown and preshutdown cancel
// serve, which stops the engine gracefully; the SCM is kept informed with
// checkpoints until it returns. A serve error is reported as a
// service-specific exit code so the failure actions restart the service.
func (h *handler) Execute(_ []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.serve(ctx) }()
	running := svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown | svc.AcceptPreShutdown}
	status <- running
	exit := func(err error) (bool, uint32) {
		h.err = err
		if err != nil {
			return true, 1
		}
		return false, 0
	}
	for {
		select {
		case err := <-done:
			return exit(err)
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.Stop, svc.Shutdown, svc.PreShutdown:
				cancel()
				return exit(h.waitStopped(done, req, status))
			}
		}
	}
}

func (h *handler) waitStopped(done <-chan error, req <-chan svc.ChangeRequest, status chan<- svc.Status) error {
	st := svc.Status{State: svc.StopPending, WaitHint: uint32(3 * stopCheckpoint / time.Millisecond)}
	status <- st
	tick := time.NewTicker(stopCheckpoint)
	defer tick.Stop()
	for {
		select {
		case err := <-done:
			return err
		case <-tick.C:
			st.CheckPoint++
			status <- st
		case c := <-req:
			if c.Cmd == svc.Interrogate {
				status <- st
			}
		}
	}
}
