package updates

import (
	"context"
	"errors"
	"fmt"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
)

// CARE update steps (F6), in order.
const (
	StepPreflight = "preflight"
	StepBackup    = "backup"
	StepImages    = "images"
	StepStopApp   = "stop-app"
	StepWriteEnv  = "write-env"
	StepMigrate   = "migrate"
	StepStartApp  = "start-stack"
	StepHealth    = "health"
)

// CARE rollback steps.
const (
	StepRBStopApp   = "rollback-stop-app"
	StepRBRestoreDB = "rollback-restore-db"
	StepRBEnv       = "rollback-env"
	StepRBStart     = "rollback-start-stack"
	StepRBHealth    = "rollback-health"
)

var careSteps = []string{StepPreflight, StepBackup, StepImages, StepBuild, StepBuildFrontend, StepStopApp, StepWriteEnv, StepMigrate, StepStartApp, StepHealth}

// appServices are stopped for a CARE update; db (and the rest) stay up.
var appServices = []protocol.Service{protocol.ServiceBackend, protocol.ServiceCeleryWorker, protocol.ServiceCeleryBeat}

// stackUpTimeout is passed to stack.up; the agent waits until healthy.
const stackUpTimeout = 600

func (c *Coordinator) careForward() map[string]stepFunc {
	return map[string]stepFunc{
		StepPreflight: c.carePreflight,
		StepBackup: func(ctx context.Context, j *Journal) error {
			if j.Rollback.PreviousImages == nil || j.Rollback.BackupID != "" {
				return nil // fresh install: nothing to back up
			}
			id, err := c.o.Agent.Backup(ctx)
			if err != nil {
				return fmt.Errorf("database backup: %w", err)
			}
			if id == "" {
				return errors.New("database backup: no backup id returned")
			}
			j.Rollback.BackupID = id
			c.logf("Backed up the database as %s", id)
			return c.saveJournal(j)
		},
		StepImages: func(ctx context.Context, j *Journal) error {
			if err := c.downloadImages(ctx, j.Care.Images); err != nil {
				return err
			}
			if err := c.saveJournal(j); err != nil {
				return err
			}
			return c.o.Stack.EnsureImages(ctx, StackImages(j.Care.Images))
		},
		StepBuild:         c.build,
		StepBuildFrontend: c.buildFrontend,
		StepStopApp: func(ctx context.Context, j *Journal) error {
			if j.Rollback.PreviousImages == nil {
				return nil
			}
			if err := c.flag(j, &j.Rollback.StackStopped); err != nil {
				return err
			}
			return c.o.Agent.Op(ctx, protocol.StackStop{Services: appServices}, nil)
		},
		StepWriteEnv: func(ctx context.Context, j *Journal) error {
			if err := c.flag(j, &j.Rollback.EnvWritten); err != nil {
				return err
			}
			plugs, err := targetPlugs(j)
			if err != nil {
				return err
			}
			return c.o.Stack.WriteEnv(ctx, StackImages(targetImages(j)), plugs)
		},
		// Migrations always run: they are idempotent, and the feed's
		// migrations flag is informational. db_migrated is set first so a
		// crash mid-migration still restores the backup.
		StepMigrate: func(ctx context.Context, j *Journal) error {
			if err := c.flag(j, &j.Rollback.DBMigrated); err != nil {
				return err
			}
			return c.o.Agent.Op(ctx, protocol.RunMigrations{}, nil)
		},
		StepStartApp: func(ctx context.Context, j *Journal) error {
			return c.o.Agent.Op(ctx, protocol.StackUp{Wait: true, TimeoutSec: stackUpTimeout}, nil)
		},
		StepHealth: func(ctx context.Context, j *Journal) error { return c.o.Stack.Health(ctx) },
	}
}

// flag sets *f and saves the journal before the action it guards.
func (c *Coordinator) flag(j *Journal, f *bool) error {
	if *f {
		return nil
	}
	*f = true
	return c.saveJournal(j)
}

// carePreflight checks the clock (against the feed: not for plugin or
// frontend settings changes), free disk and (for an update) that the running stack is healthy.
func (c *Coordinator) carePreflight(ctx context.Context, j *Journal) error {
	if j.Component != ComponentPlugins && j.Component != ComponentFrontend {
		if err := c.clockSane(); err != nil {
			return err
		}
	}
	st, err := c.o.Agent.Status(ctx)
	if err != nil {
		return fmt.Errorf("engine status: %w", err)
	}
	db, err := c.o.Stack.DBSize(ctx)
	if err != nil {
		return fmt.Errorf("database size: %w", err)
	}
	need := 2*uint64(max(db, 0)) + c.o.ImageReserveBytes
	if st.DiskFreeBytes < need {
		return fmt.Errorf("not enough free space in the engine: %s free, %s needed (twice the database plus images)", bytesStr(st.DiskFreeBytes), bytesStr(need))
	}
	if j.Rollback.PreviousImages != nil {
		if err := c.o.Stack.Health(ctx); err != nil {
			return fmt.Errorf("CARE is not healthy before the update: %w", err)
		}
	}
	return nil
}

// clockSane rejects a clock behind the verified feed's publication (by
// more than ClockSkew) or past its expiry.
func (c *Coordinator) clockSane() error {
	m, _, err := c.manifest()
	if err != nil {
		return err
	}
	if now := c.now(); now.Add(c.o.ClockSkew).Before(m.PublishedAt) {
		return fmt.Errorf("the system clock (%s) is behind the update feed (published %s)", now.Format("2006-01-02 15:04 MST"), m.PublishedAt.Format("2006-01-02 15:04 MST"))
	}
	return nil
}

func bytesStr(n uint64) string {
	const gib = 1 << 30
	return fmt.Sprintf("%.1f GiB", float64(n)/gib)
}

// careRollbackPlan undoes exactly what the journal says was changed: a
// failure before the stack was stopped needs nothing; after migrations
// started the previous env is written back and the database restored (in
// that order: the restore migrates and starts with the current env). A fresh
// install that fails is stopped.
func careRollbackPlan(j *Journal) []string {
	r := j.Rollback
	if !r.StackStopped && !r.EnvWritten && !r.DBMigrated {
		return nil
	}
	if r.PreviousImages == nil {
		return []string{StepRBStopApp}
	}
	var steps []string
	if r.DBMigrated {
		steps = append(steps, StepRBStopApp)
	}
	if r.EnvWritten {
		steps = append(steps, StepRBEnv)
	}
	if r.DBMigrated {
		steps = append(steps, StepRBRestoreDB)
	}
	return append(steps, StepRBStart, StepRBHealth)
}

func (c *Coordinator) careRollback() map[string]stepFunc {
	return map[string]stepFunc{
		StepRBStopApp: func(ctx context.Context, j *Journal) error {
			return c.o.Agent.Op(ctx, protocol.StackStop{Services: appServices}, nil)
		},
		StepRBRestoreDB: func(ctx context.Context, j *Journal) error {
			if j.Rollback.BackupID == "" {
				return errors.New("no pre-update backup to restore")
			}
			c.logf("Restoring the database from %s", j.Rollback.BackupID)
			return c.o.Agent.Restore(ctx, j.Rollback.BackupID)
		},
		StepRBEnv: func(ctx context.Context, j *Journal) error {
			return c.o.Stack.WriteEnv(ctx, StackImages(j.Rollback.PreviousImages), j.Rollback.PreviousPlugs)
		},
		StepRBStart: func(ctx context.Context, j *Journal) error {
			return c.o.Agent.Op(ctx, protocol.StackUp{Wait: true, TimeoutSec: stackUpTimeout}, nil)
		},
		StepRBHealth: func(ctx context.Context, j *Journal) error { return c.o.Stack.Health(ctx) },
	}
}
