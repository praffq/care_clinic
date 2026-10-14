package clinic

import (
	"strings"
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/release"
)

func TestRunnerTargetsTheAgentsGuestFolders(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	e := &Clinic{InstallDir: t.TempDir(), Pins: &release.Pins{}}
	run := e.Runner()
	if run.AgentDir != AgentClinicDir || run.PrepareAgent == nil {
		t.Fatalf("runner does not prepare the guest clinic folder: dir=%q prepare=%v", run.AgentDir, run.PrepareAgent != nil)
	}
	env := map[string]string{}
	for _, kv := range run.Env {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	if env["COMPOSE_FILE"] != AgentClinicDir+"/docker-compose.yml" || env["BACKUP_DIR"] != AgentBackupDir {
		t.Fatalf("compose would read host paths: COMPOSE_FILE=%q BACKUP_DIR=%q", env["COMPOSE_FILE"], env["BACKUP_DIR"])
	}
}
