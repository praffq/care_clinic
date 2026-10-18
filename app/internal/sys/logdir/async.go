package logdir

import (
	"fmt"
	"io"
	"sync"
)

type Async struct {
	w      io.Writer
	limit  int
	onDrop func(int64)

	mu      sync.Mutex
	cond    *sync.Cond
	queue   [][]byte
	queued  int
	dropped int64
	closed  bool
	done    chan struct{}
}

func NewAsync(w io.Writer, limit int, onDrop func(dropped int64)) *Async {
	a := &Async{w: w, limit: limit, onDrop: onDrop, done: make(chan struct{})}
	a.cond = sync.NewCond(&a.mu)
	go a.run()
	return a
}

func (a *Async) Write(p []byte) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return 0, io.ErrClosedPipe
	}
	if a.queued+len(p) > a.limit {
		a.dropped += int64(len(p))
		return len(p), nil
	}
	a.queue = append(a.queue, append([]byte(nil), p...))
	a.queued += len(p)
	a.cond.Signal()
	return len(p), nil
}

func (a *Async) run() {
	defer close(a.done)
	for {
		a.mu.Lock()
		for len(a.queue) == 0 && a.dropped == 0 && !a.closed {
			a.cond.Wait()
		}
		if len(a.queue) == 0 && a.dropped == 0 && a.closed {
			a.mu.Unlock()
			return
		}
		batch := a.queue
		a.queue = nil
		a.queued = 0
		dropped := a.dropped
		a.dropped = 0
		a.mu.Unlock()
		for _, b := range batch {
			_, _ = a.w.Write(b)
		}
		if dropped > 0 {
			_, _ = fmt.Fprintf(a.w, "\n[care-clinic: %d bytes of output were dropped here because the log disk could not keep up]\n", dropped)
			if a.onDrop != nil {
				a.onDrop(dropped)
			}
		}
	}
}

func (a *Async) Close() error {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		<-a.done
		return nil
	}
	a.closed = true
	a.cond.Signal()
	a.mu.Unlock()
	<-a.done
	return nil
}
