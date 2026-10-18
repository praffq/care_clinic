package protocol

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	OpLogsSystem     = "logs.system"
	OpLogsContainers = "logs.containers"

	MaxLogTailBytes    = 1 << 20
	MaxLogContainers   = 256
	DefaultLogTailLine = 200

	LogSourceMessages = "messages"
	LogSourceDocker   = "docker"
	LogSourceAgent    = "agent"

	LogStreamGap = "gap"

	LogStateFollowing = "following"
)

var LogSources = []string{LogSourceMessages, LogSourceDocker, LogSourceAgent}

var containerNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

func ValidContainerName(name string) bool { return containerNameRE.MatchString(name) }

type FileCursor struct {
	Inode  uint64 `json:"inode"`
	Offset int64  `json:"offset"`
}

func (c FileCursor) String() string {
	return strconv.FormatUint(c.Inode, 10) + ":" + strconv.FormatInt(c.Offset, 10)
}

func ParseFileCursor(s string) (FileCursor, bool) {
	ino, off, ok := strings.Cut(s, ":")
	if !ok {
		return FileCursor{}, false
	}
	i, err1 := strconv.ParseUint(ino, 10, 64)
	o, err2 := strconv.ParseInt(off, 10, 64)
	if err1 != nil || err2 != nil || o < 0 {
		return FileCursor{}, false
	}
	return FileCursor{Inode: i, Offset: o}, true
}

type LogsSystem struct {
	Cursors   map[string]FileCursor `json:"cursors,omitempty"`
	TailBytes int64                 `json:"tail_bytes,omitempty"`
}

func (LogsSystem) OpName() string { return OpLogsSystem }

func (o LogsSystem) Validate() error {
	var v validator
	for _, k := range slices.Sorted(maps.Keys(o.Cursors)) {
		v.check(slices.Contains(LogSources, k), "cursors."+k, "unknown log source; must be one of %s", strings.Join(LogSources, ", "))
		v.check(o.Cursors[k].Offset >= 0, "cursors."+k+".offset", "must not be negative")
	}
	v.check(o.TailBytes >= 0 && o.TailBytes <= MaxLogTailBytes, "tail_bytes", "must be between 0 and %d", MaxLogTailBytes)
	return v.err()
}

type LogsContainers struct {
	Since     map[string]time.Time `json:"since,omitempty"`
	TailLines int                  `json:"tail_lines,omitempty"`
}

func (LogsContainers) OpName() string { return OpLogsContainers }

func (o LogsContainers) Validate() error {
	var v validator
	v.check(len(o.Since) <= MaxLogContainers, "since", "at most %d containers", MaxLogContainers)
	for _, k := range slices.Sorted(maps.Keys(o.Since)) {
		v.check(ValidContainerName(k), fmt.Sprintf("since[%q]", k), "not a container name")
	}
	v.check(o.TailLines >= 0 && o.TailLines <= MaxTailLines, "tail_lines", "must be between 0 and %d", MaxTailLines)
	return v.err()
}
