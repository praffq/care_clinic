package backup

import (
	"github.com/ohcnetwork/care_desktop/app/internal/sys/proc"
)

type Store struct {
	Dir          string
	BackupDir    string
	Image        string
	BackendImage string
	Host         string
	Project      string
	Log          func(string)

	EnsureImage         func() error
	EnsureRestoreImages func() error
	Migrate             func(database, restoreID string) error

	// AgentBackupDir and AgentDir are the guest backup and clinic folders;
	// BackupDir and Dir are their host counterparts.
	AgentBackupDir string
	AgentDir       string
	// Refresh brings the host's verified export of the guest backups and
	// backup state up to date before restore decisions read it.
	Refresh func() error

	run proc.Runner
}

func New(run proc.Runner, s *Store) *Store {
	s.run = run
	return s
}

func (s *Store) logln(line string) {
	if s.Log != nil {
		s.Log(line)
	}
}

func (s *Store) dc(args ...string) error {
	return s.run.AgentRun("compose", args...)
}
