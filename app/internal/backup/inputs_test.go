package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/enginetest"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/proc"
)

func TestRestoreInputKeyIsPrivateAndRemovedAfterCancellation(t *testing.T) {
	agent := enginetest.New(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := New(proc.Runner{Agent: agent.Client, Ctx: ctx}, &Store{})
	inputs, err := s.newRestoreInputs()
	if err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(t.TempDir(), "recovery.pem")
	if err := os.WriteFile(local, []byte("synthetic private recovery material"), 0o644); err != nil {
		t.Fatal(err)
	}
	remote, err := inputs.addKey(local)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(agent.Path(remote))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("uploaded key is not private: %v, %v", info, err)
	}
	cancel()
	inputs.remove()
	if agent.Exists(remote) || agent.Exists(inputs.dir) {
		t.Fatal("cancelled restore retained uploaded recovery material")
	}
}
