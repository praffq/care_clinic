package updates

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/client"
	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
)

type busyAgent struct {
	Agent
	busy  int
	calls int
	err   error
}

func (a *busyAgent) Op(_ context.Context, op protocol.Op, _ any) error {
	if _, ok := op.(protocol.StackUp); !ok {
		return fmt.Errorf("unexpected op %s", op.OpName())
	}
	a.calls++
	if a.calls <= a.busy {
		return fmt.Errorf("agent op stack.up: %w", client.ErrBusy)
	}
	return a.err
}

func TestStackUpWaitsWhileTheAgentIsBusy(t *testing.T) {
	defer func(d time.Duration) { busyRetry = d }(busyRetry)
	busyRetry = time.Millisecond

	a := &busyAgent{busy: 4}
	c := &Coordinator{o: Options{Agent: a}}
	if err := c.stackUp(context.Background()); err != nil || a.calls != 5 {
		t.Fatalf("stackUp = %v after %d calls, want success after 5", err, a.calls)
	}

	failed := errors.New("unhealthy")
	a = &busyAgent{busy: 1, err: failed}
	c = &Coordinator{o: Options{Agent: a}}
	if err := c.stackUp(context.Background()); !errors.Is(err, failed) || a.calls != 2 {
		t.Fatalf("stackUp = %v after %d calls, want the agent's error after 2", err, a.calls)
	}

	a = &busyAgent{busy: 1 << 30}
	c = &Coordinator{o: Options{Agent: a}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := c.stackUp(ctx); !errors.Is(err, client.ErrBusy) {
		t.Fatalf("stackUp = %v, want busy once the context ends", err)
	}
}
