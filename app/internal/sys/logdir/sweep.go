package logdir

import (
	"errors"
	"io/fs"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
)

type SweepOptions struct {
	MaxAge time.Duration
	Budget int64
	Now    time.Time
	InUse  func(rel string) bool
}

type SweepAction struct {
	File   string
	Action string
	Reason string
	Bytes  int64
	Err    error
}

type logFile struct {
	rel       string
	size      int64
	mod       time.Time
	rotated   bool
	container bool
}

func (f logFile) removable(inUse func(string) bool) bool {
	if f.rotated {
		return true
	}
	return f.container && (inUse == nil || !inUse(f.rel))
}

func (d *Dir) logFiles() ([]logFile, error) {
	var out []logFile
	add := func(dir string, container bool) error {
		entries, err := d.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".log") {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			rel := e.Name()
			if dir != "." {
				rel = path.Join(dir, e.Name())
			}
			out = append(out, logFile{rel: rel, size: info.Size(), mod: info.ModTime(), rotated: rotationIndex(e.Name()) > 0, container: container})
		}
		return nil
	}
	if err := add(".", false); err != nil {
		return nil, err
	}
	if fi, err := d.Lstat(ContainersDir); err == nil && fi.Mode().Type() == fs.ModeDir {
		if err := add(ContainersDir, true); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (d *Dir) Sweep(o SweepOptions) ([]SweepAction, error) {
	if o.MaxAge <= 0 {
		o.MaxAge = MaxAge
	}
	if o.Budget <= 0 {
		o.Budget = Budget
	}
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	files, err := d.logFiles()
	if err != nil {
		return nil, err
	}
	var actions []SweepAction
	apply := func(f *logFile, reason string) {
		a := SweepAction{File: f.rel, Reason: reason, Bytes: f.size}
		if f.removable(o.InUse) {
			a.Action = "deleted"
			a.Err = d.Remove(f.rel)
			if errors.Is(a.Err, fs.ErrNotExist) {
				a.Err = nil
			}
		} else {
			a.Action = "emptied"
			a.Err = d.Truncate(f.rel)
		}
		if a.Err == nil {
			f.size = 0
		}
		actions = append(actions, a)
	}
	var total int64
	for i := range files {
		f := &files[i]
		if o.Now.Sub(f.mod) > o.MaxAge && (f.size > 0 || f.removable(o.InUse)) {
			apply(f, "unchanged for more than "+days(o.MaxAge))
		}
		total += f.size
	}
	if total <= o.Budget {
		return actions, nil
	}
	order := make([]*logFile, 0, len(files))
	for i := range files {
		if files[i].size > 0 {
			order = append(order, &files[i])
		}
	}
	slices.SortStableFunc(order, func(a, b *logFile) int {
		if a.removable(o.InUse) != b.removable(o.InUse) {
			if a.removable(o.InUse) {
				return -1
			}
			return 1
		}
		return a.mod.Compare(b.mod)
	})
	for _, f := range order {
		if total <= o.Budget {
			break
		}
		size := f.size
		apply(f, "the log folder is over its "+mib(o.Budget)+" limit")
		if f.size == 0 {
			total -= size
		}
	}
	return actions, nil
}

func days(d time.Duration) string {
	n := int(d / (24 * time.Hour))
	if n == 1 {
		return "1 day"
	}
	return strconv.Itoa(n) + " days"
}

func mib(n int64) string { return strconv.FormatInt(n>>20, 10) + " MiB" }
