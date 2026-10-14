package clinic

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/compose"
	"github.com/ohcnetwork/care_desktop/app/internal/updates"
)

func TestCAREUpdateCheckpointsSurviveInterruptedApply(t *testing.T) {
	for _, phase := range []string{"backup", "images", "migration", "rollback"} {
		t.Run(phase, func(t *testing.T) {
			e, trace, _ := migrationFixture(t)
			if err := e.Backups().EnsureKeysDir(); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(e.InstallDir, "keys", "backup-cert.pem"), []byte("fixture"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := queueBackendUpdate(e.InstallDir, "b"); err != nil {
				t.Fatal(err)
			}
			t.Setenv("CARE_NEXT_FINGERPRINT", compose.ReadLock(e.InstallDir).Backend.Next.Image)
			script := `#!/bin/sh
printf '%s\n' "$*" >> "$CARE_MIGRATION_TRACE"
case "$1 $2" in
  'compose run')
    for last do :; done
    mkdir -p "$BACKUP_DIR"
    printf 'fixture backup' > "$BACKUP_DIR/care-$last.dump.enc"
    exit 0 ;;
  'compose stop'|'compose ps'|'compose up') exit 0 ;;
  'image ls') printf 'fixture-image\n'; exit 0 ;;
  'image inspect') printf '%s\n' "$CARE_NEXT_FINGERPRINT"; exit 0 ;;
  'image tag') exit 0 ;;
esac
exit 99
`
			if err := os.WriteFile(filepath.Join(e.InstallDir, "docker"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(e.InstallDir, "updates")
			c, err := updates.New(dir)
			if err != nil {
				t.Fatal(err)
			}
			j, err := c.Create(1, updates.Snapshot{}, updates.Snapshot{}, []string{string(updates.CAREImages)})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.Approve(j.PlanID, "staff"); err != nil {
				t.Fatal(err)
			}
			crash := new(int)
			func() {
				defer func() {
					if got := recover(); got != crash {
						t.Fatalf("expected simulated crash at %s, got %v", phase, got)
					}
				}()
				err := c.Execute(context.Background(), j.PlanID, updates.Hooks{
					Preflight: func(context.Context, *updates.Journal) error { return nil },
					Apply: func(_ context.Context, _ string, plan *updates.Journal, checkpoint updates.Checkpoint) (string, error) {
						err := e.ApplyUpdateWithJournal(plan, func() error {
							if err := checkpoint(); err != nil {
								return err
							}
							reached := plan.Rollback.BackupID != ""
							switch phase {
							case "images":
								reached = len(plan.Rollback.ImagesKept) > 0
							case "migration":
								reached = plan.Rollback.DBMigrated
							case "rollback":
								reached = plan.State == updates.RollingBack
							}
							if reached {
								panic(crash)
							}
							return nil
						})
						t.Fatalf("apply returned before simulated crash: %v", err)
						return "", err
					},
					Rollback: func(context.Context, string, updates.Journal) error {
						t.Fatal("rollback should happen only after reloading")
						return nil
					},
				})
				t.Fatalf("execute returned before simulated crash: %v", err)
			}()
			reloaded, err := updates.New(dir)
			if err != nil {
				t.Fatal(err)
			}
			stored, err := reloaded.Journal()
			if err != nil || stored == nil || stored.Rollback.BackupID == "" {
				t.Fatalf("backup ID not durable: %+v, %v", stored, err)
			}
			wantMigrated := phase == "migration" || phase == "rollback"
			if stored.Rollback.DBMigrated != wantMigrated {
				t.Fatalf("migration flag = %v, want %v", stored.Rollback.DBMigrated, wantMigrated)
			}
			if phase != "backup" && !slices.Contains(stored.Rollback.ImagesKept, e.Pins.BackendImage+"-previous") {
				t.Fatal("previous image not journaled before activation")
			}
			calls := trace()
			if (phase == "backup" || phase == "images") && strings.Contains(calls, "image tag") {
				t.Fatal("image activation preceded its durable checkpoint")
			}
			if phase == "migration" && strings.Contains(calls, "manage.py migrate") {
				t.Fatal("migration preceded its durable checkpoint")
			}
			if phase == "rollback" {
				if stored.State != updates.RollingBack || !strings.Contains(calls, "manage.py migrate") {
					t.Fatalf("rollback transition not durable after failed migration: %+v\n%s", stored, calls)
				}
				lock := compose.ReadLock(e.InstallDir)
				if lock.Backend.Previous == nil {
					t.Fatal("previous image metadata lost during activation")
				}
			}
			undone := false
			if err := reloaded.Recover(context.Background(), updates.Hooks{
				Rollback: func(_ context.Context, _ string, recovered updates.Journal) error {
					undone = true
					if recovered.State != updates.RollingBack || recovered.Rollback.BackupID != stored.Rollback.BackupID ||
						recovered.Rollback.DBMigrated != wantMigrated {
						t.Fatalf("recovery lost persisted rollback metadata: %+v", recovered)
					}
					return nil
				},
			}); err != nil || !undone {
				t.Fatalf("startup rollback = %v, invoked = %v", err, undone)
			}
		})
	}
}
