package updates

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/release"
)

func TestJournalRequiresApprovalAndSuccessfulPlanMovesToHistory(t *testing.T) {
	c := coordinator(t)
	j, err := c.Create(3, Snapshot{DesktopVersion: "1.0.0"}, Snapshot{DesktopVersion: "1.0.1"}, []string{string(Desktop)})
	if err != nil {
		t.Fatal(err)
	}
	if j.State != PendingApproval {
		t.Fatalf("new plan state = %q", j.State)
	}
	hooks := Hooks{Preflight: func(context.Context, *Journal) error { return nil }}
	if err := c.Execute(context.Background(), j.PlanID, hooks); err == nil {
		t.Fatal("unapproved plan ran")
	}
	approved, err := c.Approve(j.PlanID, "staff")
	if err != nil {
		t.Fatal(err)
	}
	var applies int
	hooks.Apply = func(_ context.Context, name string, _ *Journal) (string, error) {
		applies++
		if name != string(Desktop) {
			t.Fatalf("step = %q", name)
		}
		return "restore previous app", nil
	}
	hooks.Rollback = func(context.Context, string, Journal) error { return nil }
	if err := c.Execute(context.Background(), approved.PlanID, hooks); err != nil {
		t.Fatal(err)
	}
	if applies != 1 {
		t.Fatalf("applied %d times", applies)
	}
	if journal, err := c.Journal(); err != nil || journal != nil {
		t.Fatalf("journal after completion = %#v, %v", journal, err)
	}
	history, err := c.History()
	if err != nil || len(history) != 1 || history[0].State != Done || history[0].ApprovedBy != "staff" {
		t.Fatalf("history = %#v, %v", history, err)
	}
}

func TestFailedStepStopsAfterThreeAttemptsAndRollsBack(t *testing.T) {
	c := coordinator(t)
	j, err := c.Create(1, Snapshot{}, Snapshot{}, []string{string(CAREImages)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Approve(j.PlanID, "operator"); err != nil {
		t.Fatal(err)
	}
	attempts, undos := 0, 0
	err = c.Execute(context.Background(), j.PlanID, Hooks{
		Preflight: func(context.Context, *Journal) error { return nil },
		Apply: func(context.Context, string, *Journal) (string, error) {
			attempts++
			return "restore previous images", errors.New("image health failed")
		},
		Rollback: func(context.Context, string, Journal) error { undos++; return nil },
	})
	if err == nil || attempts != 3 || undos != 1 {
		t.Fatalf("execute error=%v attempts=%d undos=%d", err, attempts, undos)
	}
	history, err := c.History()
	if err != nil || len(history) != 1 || history[0].State != RolledBack ||
		history[0].Steps[0].Attempts != 3 || history[0].Steps[0].Status != StepUndone {
		t.Fatalf("history = %#v, %v", history, err)
	}
}

func TestPreflightFailureDoesNotApply(t *testing.T) {
	c := coordinator(t)
	j, err := c.Create(1, Snapshot{}, Snapshot{}, []string{string(Engine)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Approve(j.PlanID, "staff"); err != nil {
		t.Fatal(err)
	}
	applied := false
	err = c.Execute(context.Background(), j.PlanID, Hooks{
		Preflight: func(context.Context, *Journal) error { return errors.New("insufficient disk") },
		Apply: func(context.Context, string, *Journal) (string, error) {
			applied = true
			return "", nil
		},
		Rollback: func(context.Context, string, Journal) error { return nil },
	})
	if err == nil || applied {
		t.Fatalf("preflight error=%v applied=%v", err, applied)
	}
	history, err := c.History()
	if err != nil || len(history) != 1 || history[0].State != Failed {
		t.Fatalf("history = %#v, %v", history, err)
	}
}

func TestStartupFinalizesUnapprovedPlanAndRollsBackInterruptedPlan(t *testing.T) {
	c := coordinator(t)
	pending, err := c.Create(1, Snapshot{}, Snapshot{}, []string{string(Engine)})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Recover(context.Background(), Hooks{}); err != nil {
		t.Fatal(err)
	}
	if j, err := c.Journal(); err != nil || j != nil {
		t.Fatalf("unapproved plan was not finalized: %#v, %v", j, err)
	}
	history, err := c.History()
	if err != nil || len(history) != 1 || history[0].PlanID != pending.PlanID || history[0].State != Failed {
		t.Fatalf("unapproved plan history = %#v, %v", history, err)
	}
	interrupted, err := c.Create(1, Snapshot{}, Snapshot{}, []string{string(Engine)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Approve(interrupted.PlanID, "staff"); err != nil {
		t.Fatal(err)
	}
	if err := c.Recover(context.Background(), Hooks{
		Rollback: func(context.Context, string, Journal) error { return nil },
	}); err != nil {
		t.Fatal(err)
	}
	history, err = c.History()
	if err != nil || len(history) != 2 || history[1].State != RolledBack {
		t.Fatalf("history = %#v, %v", history, err)
	}
}

func TestDesktopHandoffRemainsJournaledUntilNewAppResumesPlan(t *testing.T) {
	c := coordinator(t)
	j, err := c.Create(1, Snapshot{DesktopVersion: "1.0.0"}, Snapshot{DesktopVersion: "1.0.1"}, []string{string(Desktop)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Approve(j.PlanID, "staff"); err != nil {
		t.Fatal(err)
	}
	err = c.Execute(context.Background(), j.PlanID, Hooks{
		Preflight: func(context.Context, *Journal) error { return nil },
		Apply: func(context.Context, string, *Journal) (string, error) {
			return "previous app retained", ErrHandoffPending
		},
		Rollback: func(context.Context, string, Journal) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	journal, err := c.Journal()
	if err != nil || journal == nil || journal.State != Running || journal.Steps[0].Status != StepRunning {
		t.Fatalf("handoff journal = %#v, %v", journal, err)
	}
	if err := c.CompleteHandoff(j.PlanID, "1.0.1"); err != nil {
		t.Fatal(err)
	}
	if err := c.Execute(context.Background(), j.PlanID, Hooks{
		Preflight: func(context.Context, *Journal) error { return nil },
		Apply: func(context.Context, string, *Journal) (string, error) {
			t.Fatal("completed desktop step was applied again")
			return "", nil
		},
		Rollback: func(context.Context, string, Journal) error { return nil },
	}); err != nil {
		t.Fatal(err)
	}
	if journal, err := c.Journal(); err != nil || journal != nil {
		t.Fatalf("completed handoff left journal = %#v, %v", journal, err)
	}
}

func TestCorruptJournalIsNotTreatedAsNoPendingPlan(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "journal.json")
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(dir); err == nil || !strings.Contains(err.Error(), "read update journal") {
		t.Fatalf("corrupt journal error = %v", err)
	}
}

func TestJournalDoesNotPersistSettingsValues(t *testing.T) {
	c := coordinator(t)
	j, err := c.Create(1, Snapshot{SettingsHash: strings.Repeat("a", 64)}, Snapshot{}, []string{string(Desktop)})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(c.JournalPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "settings_value") || strings.Contains(string(data), "password") {
		t.Fatalf("journal contains a settings value: %s", data)
	}
	var stored Journal
	if err := json.Unmarshal(data, &stored); err != nil || stored.PlanID != j.PlanID {
		t.Fatalf("journal decode = %#v, %v", stored, err)
	}
}

func TestDependencyOrderAndBridgeDetection(t *testing.T) {
	m := dependencyManifest()
	order, err := DependencyOrder("0.1.19", "0.1.1", m)
	if err != nil || len(order) != 2 || order[0] != Desktop || order[1] != Engine {
		t.Fatalf("desktop-first dependency order = %v, %v", order, err)
	}
	order, err = DependencyOrder("0.1.20", "0.1.1", m)
	if err != nil || len(order) != 1 || order[0] != Engine {
		t.Fatalf("engine-only order = %v, %v", order, err)
	}
	m.Engine.RequiresDesktop = ">=0.1.21"
	if _, err := DependencyOrder("0.1.18", "0.1.1", m); err == nil || !strings.Contains(err.Error(), "bridge release") {
		t.Fatalf("missing bridge error = %v", err)
	}
}

func coordinator(t *testing.T) *Coordinator {
	t.Helper()
	c, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func dependencyManifest() release.Manifest {
	hash := strings.Repeat("a", 64)
	image := "ghcr.io/example/care@sha256:" + hash
	asset := release.Asset{URL: "https://example.invalid/release", SHA256: hash, Size: 1}
	build := release.ImageBuild{Commit: strings.Repeat("b", 40), Image: image, Builder: image, SourceHash: hash}
	frontend := build
	frontend.BuildEnv = map[string]string{}
	return release.Manifest{
		Schema: 1, Revision: 1, PublishedAt: "2026-10-14T14:00:00Z",
		Desktop: release.DesktopUpdate{Version: "0.1.20", RequiresEngine: ">=0.1.1", Assets: map[string]release.Asset{
			"darwin-universal": asset, "windows-amd64": asset,
		}},
		Engine: release.EngineUpdate{Version: "0.1.2", RequiresDesktop: ">=0.1.19", Assets: map[string]release.Asset{
			"linux-arm64": asset, "linux-amd64": asset,
		}},
		Care:          release.CareUpdate{Version: "3.1.0", RequiresDesktop: ">=0.1.19", RequiresEngine: ">=0.1.2", Backend: build, Frontend: frontend},
		SupportImages: map[string]string{"postgres": image, "redis": image, "caddy": image, "silo": image, "backup": image},
		Blocked:       []string{}, SourceHashes: map[string]string{"desktop": hash, "engine": hash, "support": hash},
	}
}
