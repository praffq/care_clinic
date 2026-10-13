package vm

import "context"

// startBlocking runs a start call that cannot itself be interrupted (the
// macOS Virtualization.framework Start blocks until its completion handler
// fires). On cancellation the caller is released immediately and the machine
// is stopped, then cleanup runs, as soon as start returns; if the helper exits
// first the in-process VM ends with it. A start that succeeds after
// cancellation is also stopped, so no machine reaches readiness checks or
// advertisement once a stop was requested.
func startBlocking(ctx context.Context, start func() error, stop, cleanup func()) error {
	result := make(chan error, 1)
	go func() { result <- start() }()
	select {
	case err := <-result:
		if err != nil {
			cleanup()
			return err
		}
		if ctx.Err() != nil {
			stop()
			cleanup()
			return ctx.Err()
		}
		return nil
	case <-ctx.Done():
		go func() {
			if err := <-result; err == nil {
				stop()
			}
			cleanup()
		}()
		return ctx.Err()
	}
}
