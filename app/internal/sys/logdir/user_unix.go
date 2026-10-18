//go:build unix

package logdir

import (
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"regexp"
	"runtime"
	"strconv"
	"syscall"
)

var userNameRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*$`)

func ValidUser(name string) bool { return userNameRE.MatchString(name) && name != "root" }

func ForUser(name string) (string, *Owner, error) {
	if !ValidUser(name) {
		return "", nil, fmt.Errorf("%q is not a valid user name", name)
	}
	u, err := user.Lookup(name)
	if err != nil {
		return "", nil, err
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return "", nil, fmt.Errorf("user %s: uid %q: %w", name, u.Uid, err)
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return "", nil, fmt.Errorf("user %s: gid %q: %w", name, u.Gid, err)
	}
	if u.HomeDir == "" {
		return "", nil, fmt.Errorf("user %s has no home folder", name)
	}
	owner := &Owner{UID: uid, GID: gid}
	dir := Default(runtime.GOOS, u.HomeDir, noEnv)
	if os.Geteuid() != 0 && os.Geteuid() != uid {
		owner = nil
	}
	if err := Prepare(u.HomeDir, dir, owner); err != nil {
		return "", nil, err
	}
	return dir, owner, nil
}

func chownDir(path string, o *Owner) error { return os.Lchown(path, o.UID, o.GID) }

func ownedBy(fi fs.FileInfo, o *Owner) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || o == nil {
		return true
	}
	return int(st.Uid) == o.UID || st.Uid == 0
}
