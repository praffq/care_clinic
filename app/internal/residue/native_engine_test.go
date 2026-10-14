package residue

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/engine"
	"github.com/ohcnetwork/care_desktop/app/internal/enginetest"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/proc"
)

func TestNativeAgentFixtureDoesNotInspectHostInstallation(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	agent := enginetest.New(t, nil)
	available, err := engineAvailableWith(proc.Runner{Agent: agent.Client}, false, func() (bool, error) {
		t.Fatal("reachable agent fixture consulted the real host installation")
		return false, os.ErrPermission
	})
	if !available || err != nil {
		t.Fatalf("isolated agent unavailable: %v, %v", available, err)
	}
}

func TestNativeMissingSocketRequiresProofOfEngineAbsence(t *testing.T) {
	for _, state := range []string{"absent", "retained data", "inspection failed"} {
		t.Run(state, func(t *testing.T) {
			t.Setenv("PATH", t.TempDir())
			client := engine.New(func(context.Context) (net.Conn, error) {
				return nil, fmt.Errorf("agent socket: %w", os.ErrNotExist)
			})
			available, err := engineAvailableWith(proc.Runner{Agent: client}, false, func() (bool, error) {
				switch state {
				case "absent":
					return true, nil
				case "retained data":
					return false, nil
				default:
					return false, os.ErrPermission
				}
			})
			if available {
				t.Fatal("missing socket reported an available engine")
			}
			switch state {
			case "absent":
				if err != nil {
					t.Fatalf("proven absent engine: %v", err)
				}
			case "retained data":
				if err == nil || !strings.Contains(err.Error(), "retained clinic volumes") {
					t.Fatalf("retained native data treated as absent: %v", err)
				}
			case "inspection failed":
				if !errors.Is(err, os.ErrPermission) {
					t.Fatalf("absence inspection error lost: %v", err)
				}
			}
		})
	}
}

func TestNativeAbsenceRequiresAllPathsMissing(t *testing.T) {
	root := t.TempDir()
	data, agent := filepath.Join(root, "data"), filepath.Join(root, "agent")
	paths := []string{data, agent}
	if absent, err := nativePathsAbsent(paths); !absent || err != nil {
		t.Fatalf("all missing: %v, %v", absent, err)
	}
	if err := os.Mkdir(data, 0o700); err != nil {
		t.Fatal(err)
	}
	if absent, err := nativePathsAbsent(paths); absent || err != nil {
		t.Fatalf("retained data: %v, %v", absent, err)
	}
	if err := os.Remove(data); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agent, nil, 0o700); err != nil {
		t.Fatal(err)
	}
	if absent, err := nativePathsAbsent(paths); absent || err != nil {
		t.Fatalf("retained agent: %v, %v", absent, err)
	}
	// An uninspectable path must not become evidence of absence.
	if absent, err := nativePathsAbsent([]string{filepath.Join(agent, "child")}); absent || err == nil {
		t.Fatalf("invalid ancestor: %v, %v", absent, err)
	}
}
