package clinic

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/agent"
	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
	"github.com/ohcnetwork/care_desktop/app/internal/release"
	"github.com/ohcnetwork/care_desktop/app/internal/stack"
	"github.com/ohcnetwork/care_desktop/app/internal/stack/stacktest"
)

type stackFixture struct {
	*stacktest.Service
	Runtime *stack.Runtime
}

// fakeStack answers like a running clinic. CARE_FAKE_STATE (stopped,
// unreachable) and CARE_MIGRATION_FAILURE/CARE_PLUGIN_FAILURE=migration
// select failures.
func fakeStack(t *testing.T) stackFixture {
	svc := stacktest.New(t)
	svc.Fake.Handle = func(_ context.Context, op protocol.Op, _ agent.Emitter) (any, error) {
		switch op.(type) {
		case *protocol.StackStatus:
			backend := "running"
			switch os.Getenv("CARE_FAKE_STATE") {
			case "unreachable":
				return nil, errors.New("engine unreachable")
			case "stopped":
				backend = "exited"
			}
			return protocol.StackStatusResult{Services: []protocol.ServiceState{
				{Name: protocol.ServiceDB, State: "running"}, {Name: protocol.ServiceBackend, State: backend},
			}}, nil
		case *protocol.RunMigrations:
			if os.Getenv("CARE_MIGRATION_FAILURE") == "migration" || os.Getenv("CARE_PLUGIN_FAILURE") == "migration" {
				return nil, errors.New("migration failed")
			}
		case *protocol.ManageCmd:
			if os.Getenv("CARE_MIGRATION_FAILURE") == "existing" {
				return nil, errors.New("Error: That username is already taken.")
			}
		}
		return nil, nil
	}
	return stackFixture{svc, &stack.Runtime{Service: svc, StateDir: t.TempDir()}}
}

func TestStartConfiguresMigratesThenStarts(t *testing.T) {
	for _, existing := range []bool{false, true} {
		dir := t.TempDir()
		if err := os.CopyFS(dir, os.DirFS("../../../deployments")); err != nil {
			t.Fatal(err)
		}
		if existing {
			t.Setenv("CARE_MIGRATION_FAILURE", "existing")
		}
		fs := fakeStack(t)
		var images []stack.Image
		for _, role := range stack.RequiredRoles {
			images = append(images, stack.Image{Role: role, Repo: "example/" + string(role), Digest: protocol.Digest("sha256:" + strings.Repeat("a", 64))})
		}
		e := &Clinic{InstallDir: dir, BackupDir: filepath.Join(dir, "backups"), MDNSName: "north", Pins: &release.Pins{},
			AdminPassword: "SyntheticAdminSecret123", Stack: fs.Runtime, StackImages: images, localSetupOffered: true}
		if err := e.Start(); err != nil {
			t.Fatal(err)
		}
		got := strings.Join(fs.Methods(), " ")
		if want := "RunMigrations StackUp Manage"; !strings.HasSuffix(got, want) {
			t.Fatalf("calls = %q, want suffix %q", got, want)
		}
		env, err := os.ReadFile(filepath.Join(fs.Files, "config", "clinic.env"))
		if err != nil || !strings.Contains(string(env), "north.local") {
			t.Fatalf("clinic.env not uploaded for north.local: %q, %v", env, err)
		}
		status, err := e.Status()
		if err != nil || status != "backend running\ndb running" {
			t.Fatalf("status = %q, %v", status, err)
		}
	}
}

func TestStartReportsMigrationFailureBeforeStarting(t *testing.T) {
	t.Setenv("CARE_MIGRATION_FAILURE", "migration")
	fs := fakeStack(t)
	e := &Clinic{InstallDir: t.TempDir(), Stack: fs.Runtime}
	if err := e.migrate(); err == nil {
		t.Fatal("migration failure was not reported")
	}
	if got := strings.Join(fs.Methods(), " "); got != "RunMigrations" {
		t.Fatalf("calls = %q", got)
	}
}
