package hostlogs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ohcnetwork/care_desktop/app/internal/sys/atomicfile"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/logdir"
)

const (
	UserFile      = "log-user"
	StateFile     = "logs-state.json"
	ServiceLog    = "service.log"
	ConsoleLog    = "engine-console.log"
	SystemLog     = "guest-system.log"
	CollectorLog  = "collector.log"
	ContainersDir = logdir.ContainersDir
)

var forUser = logdir.ForUser

func WriteUser(root, user string) error {
	if !logdir.ValidUser(user) {
		return fmt.Errorf("%q is not a valid user for the log folder", user)
	}
	return atomicfile.Write(filepath.Join(root, UserFile), []byte(user+"\n"), 0o644)
}

func ReadUser(root string) (string, error) {
	b, err := os.ReadFile(filepath.Join(root, UserFile))
	if err != nil {
		return "", err
	}
	user := strings.TrimSpace(string(b))
	if !logdir.ValidUser(user) {
		return "", fmt.Errorf("%s holds %q, which is not a valid user", filepath.Join(root, UserFile), user)
	}
	return user, nil
}

func Resolve(root, fallback string) (*logdir.Dir, []string, error) {
	var notes []string
	user, err := ReadUser(root)
	switch {
	case err != nil:
		notes = append(notes, fmt.Sprintf("%v; logs are written to %s instead of the user's log folder", err, fallback))
	default:
		dir, owner, err := forUser(user)
		if err == nil {
			var d *logdir.Dir
			if d, err = logdir.Open(dir, owner); err == nil {
				return d, nil, nil
			}
		}
		notes = append(notes, fmt.Sprintf("couldn't use the log folder of %s: %v; logs are written to %s instead", user, err, fallback))
	}
	d, err := logdir.OpenOrCreate(fallback, nil)
	return d, notes, err
}
