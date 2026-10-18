package updates

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
	"github.com/ohcnetwork/care_desktop/app/internal/service/slots"
)

// Engine update steps (F7), in order.
const (
	StepStage       = "stage-slot"
	StepStopStack   = "stop-stack"
	StepActivate    = "activate"
	StepBoot        = "boot"
	StepEngineStart = "start-stack"
	StepEngineCheck = "health"
)

// Engine rollback steps. data.img is never rolled back: only the slot is.
const (
	StepRBReactivate = "rollback-reactivate"
	StepRBReboot     = "rollback-boot"
)

var engineSteps = []string{StepStage, StepStopStack, StepActivate, StepBoot, StepEngineStart, StepEngineCheck}

func (c *Coordinator) engineForward() map[string]stepFunc {
	return map[string]stepFunc{
		StepStage: func(ctx context.Context, j *Journal) error { return c.stageEngine(ctx, j.Engine) },
		StepStopStack: func(ctx context.Context, j *Journal) error {
			if !j.Engine.WithCare || j.Rollback.PreviousEngine == "" {
				return nil
			}
			if err := c.flag(j, &j.Rollback.StackStopped); err != nil {
				return err
			}
			return c.o.Agent.Op(ctx, protocol.StackStop{}, nil)
		},
		StepActivate: func(ctx context.Context, j *Journal) error {
			if err := c.flag(j, &j.Rollback.EngineActivated); err != nil {
				return err
			}
			c.logf("Activating engine %s", j.Engine.Version)
			_, err := c.o.Service.Activate(ctx, j.Engine.Version)
			return err
		},
		StepBoot: func(ctx context.Context, j *Journal) error {
			st, err := c.waitEngine(ctx, j.Engine.Version)
			if err != nil {
				return err
			}
			if st.DataSchema != j.Engine.DataSchema {
				return fmt.Errorf("engine %s reports data_schema %d, the feed says %d", st.EngineVersion, st.DataSchema, j.Engine.DataSchema)
			}
			if c.o.MaxDataSchema > 0 && st.DataSchema > c.o.MaxDataSchema {
				return fmt.Errorf("engine data_schema %d is newer than this app supports (%d)", st.DataSchema, c.o.MaxDataSchema)
			}
			return nil
		},
		StepEngineStart: func(ctx context.Context, j *Journal) error {
			if !j.Engine.WithCare {
				return nil
			}
			return c.stackUp(ctx)
		},
		StepEngineCheck: func(ctx context.Context, j *Journal) error {
			if !j.Engine.WithCare {
				return nil
			}
			return c.o.Stack.Health(ctx)
		},
	}
}

// stageEngine makes sure the target slot is installed (inactive): it is
// normally staged by Check already.
func (c *Coordinator) stageEngine(ctx context.Context, t *EngineTarget) error {
	l, err := c.o.Service.Slots(ctx)
	if err != nil {
		return fmt.Errorf("list engine slots: %w", err)
	}
	if slices.ContainsFunc(l.Slots, func(s slots.Slot) bool { return s.Version == t.Version }) {
		return nil
	}
	path, err := c.download(ctx, t.URL, t.SHA256, t.Size)
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	c.logf("Installing engine %s into a new slot", t.Version)
	sl, err := c.o.Service.InstallEngine(ctx, f, t.Size, t.SHA256)
	if err != nil {
		return fmt.Errorf("install engine %s: %w", t.Version, err)
	}
	if sl.Version != t.Version {
		return fmt.Errorf("engine bundle is version %s, the feed says %s", sl.Version, t.Version)
	}
	return nil
}

// waitEngine polls the agent until it reports version with docker ready.
func (c *Coordinator) waitEngine(ctx context.Context, version string) (protocol.Status, error) {
	ctx, cancel := context.WithTimeout(ctx, c.o.BootTimeout)
	defer cancel()
	var last error
	for {
		st, err := c.o.Agent.Status(ctx)
		switch {
		case err != nil:
			last = err
		case st.EngineVersion != version:
			last = fmt.Errorf("engine reports version %s, want %s", st.EngineVersion, version)
		case !st.DockerReady:
			last = errors.New("docker is not ready")
		default:
			return st, nil
		}
		if err := sleep(ctx, c.o.PollInterval); err != nil {
			return st, fmt.Errorf("engine %s did not come up within %s: %w", version, c.o.BootTimeout, last)
		}
	}
}

// engineRollbackPlan re-activates the previous slot when the new one was
// activated, and restarts the stack when it was stopped.
func engineRollbackPlan(j *Journal) []string {
	r := j.Rollback
	var steps []string
	if r.EngineActivated && r.PreviousEngine != "" {
		steps = append(steps, StepRBReactivate, StepRBReboot)
	}
	if j.Engine.WithCare && r.StackStopped {
		steps = append(steps, StepRBStart, StepRBHealth)
	}
	return steps
}

func (c *Coordinator) engineRollback() map[string]stepFunc {
	care := c.careRollback()
	return map[string]stepFunc{
		StepRBReactivate: func(ctx context.Context, j *Journal) error {
			c.logf("Re-activating engine %s", j.Rollback.PreviousEngine)
			_, err := c.o.Service.Activate(ctx, j.Rollback.PreviousEngine)
			return err
		},
		StepRBReboot: func(ctx context.Context, j *Journal) error {
			_, err := c.waitEngine(ctx, j.Rollback.PreviousEngine)
			return err
		},
		StepRBStart:  care[StepRBStart],
		StepRBHealth: care[StepRBHealth],
	}
}
