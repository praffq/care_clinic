package logdir

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"

	"golang.org/x/sys/windows/registry"
)

var sidRE = regexp.MustCompile(`^S-1-[0-9]+(-[0-9]+)+$`)

func ValidUser(sid string) bool { return sidRE.MatchString(sid) }

func ForUser(sid string) (string, *Owner, error) {
	if !ValidUser(sid) {
		return "", nil, fmt.Errorf("%q is not a Windows SID", sid)
	}
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion\ProfileList\`+sid, registry.QUERY_VALUE)
	if err != nil {
		return "", nil, fmt.Errorf("no Windows profile for %s: %w", sid, err)
	}
	defer func() { _ = k.Close() }()
	raw, _, err := k.GetStringValue("ProfileImagePath")
	if err != nil {
		return "", nil, fmt.Errorf("profile folder of %s: %w", sid, err)
	}
	home, err := registry.ExpandString(raw)
	if err != nil {
		return "", nil, err
	}
	home = filepath.Clean(home)
	dir := Default("windows", home, noEnv)
	if err := Prepare(home, dir, nil); err != nil {
		return "", nil, err
	}
	return dir, nil, nil
}

func chownDir(string, *Owner) error { return nil }

func ownedBy(fs.FileInfo, *Owner) bool { return true }
