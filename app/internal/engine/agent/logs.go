package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
)

var DefaultLogFiles = map[string]string{
	protocol.LogSourceMessages: "/var/log/messages",
	protocol.LogSourceDocker:   "/var/log/docker.log",
	protocol.LogSourceAgent:    "/var/log/care-engine-agent.log",
}

const (
	defaultLogPoll        = time.Second
	defaultContainerPoll  = 3 * time.Second
	stoppedContainerCheck = 30 * time.Second
	defaultLogTailBytes   = 64 << 10
	maxLogReadPerPoll     = 1 << 20
)

func (d *DockerExecutor) logFiles() map[string]string {
	if d.LogFiles != nil {
		return d.LogFiles
	}
	return DefaultLogFiles
}

func (d *DockerExecutor) logPoll() time.Duration {
	if d.LogPoll > 0 {
		return d.LogPoll
	}
	return defaultLogPoll
}

func (d *DockerExecutor) containerPoll() time.Duration {
	if d.ContainerPoll > 0 {
		return d.ContainerPoll
	}
	return defaultContainerPoll
}

func (d *DockerExecutor) LogsSystem(ctx context.Context, op protocol.LogsSystem, em Emitter) error {
	tailBytes := op.TailBytes
	if tailBytes == 0 {
		tailBytes = defaultLogTailBytes
	}
	files := d.logFiles()
	var tails []*fileTail
	for _, src := range protocol.LogSources {
		path, ok := files[src]
		if !ok {
			continue
		}
		ft := &fileTail{source: src, path: path, em: em, tailBytes: tailBytes}
		if c, ok := op.Cursors[src]; ok {
			ft.resume = &c
		}
		tails = append(tails, ft)
	}
	em.Progress(protocol.Progress{State: protocol.LogStateFollowing})
	defer func() {
		for _, ft := range tails {
			ft.close()
		}
	}()
	t := time.NewTicker(d.logPoll())
	defer t.Stop()
	for {
		for _, ft := range tails {
			ft.poll()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

type fileTail struct {
	source    string
	path      string
	em        Emitter
	tailBytes int64
	resume    *protocol.FileCursor

	started bool
	skip    bool
	f       *os.File
	ino     uint64
	off     int64
	partial []byte
	buf     []byte
}

func (t *fileTail) gap(msg string) {
	t.em.Log(protocol.LogLine{Source: t.source, Stream: protocol.LogStreamGap, Line: msg})
}

func (t *fileTail) close() {
	if t.f != nil {
		_ = t.f.Close()
		t.f = nil
	}
}

func (t *fileTail) poll() {
	if t.f == nil && !t.open() {
		return
	}
	t.drain()
	fi, err := os.Stat(t.path)
	if err != nil {
		return
	}
	if ino := inodeOf(fi); ino != t.ino {
		t.drain()
		t.flushPartial()
		t.close()
		if t.openAt(0) {
			t.drain()
		}
		return
	}
	if fi.Size() < t.off {
		t.flushPartial()
		t.gap(t.path + " was emptied inside the engine (log rotation); lines written just before that may be missing")
		if _, err := t.f.Seek(0, io.SeekStart); err == nil {
			t.off = 0
			t.drain()
		}
	}
}

func (t *fileTail) open() bool {
	fi, err := os.Stat(t.path)
	if err != nil {
		return false
	}
	if t.started {
		return t.openAt(0)
	}
	t.started = true
	if c := t.resume; c != nil {
		ino := inodeOf(fi)
		switch {
		case c.Inode == ino && c.Offset <= fi.Size():
			return t.openAt(c.Offset)
		case c.Inode == ino:
			t.gap(t.path + " was emptied inside the engine since the last copy; lines written before that may be missing")
		case t.drainRotated(*c):
		default:
			t.gap(t.path + " was rotated inside the engine since the last copy; lines that were not copied before the rotation are missing")
		}
		return t.openAt(0)
	}
	start := max(0, fi.Size()-t.tailBytes)
	if !t.openAt(start) {
		return false
	}
	t.skip = start > 0
	return true
}

func (t *fileTail) drainRotated(c protocol.FileCursor) bool {
	for _, p := range []string{t.path + ".0", t.path + ".1"} {
		fi, err := os.Stat(p)
		if err != nil || inodeOf(fi) != c.Inode || fi.Size() < c.Offset {
			continue
		}
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		if _, err := f.Seek(c.Offset, io.SeekStart); err != nil {
			_ = f.Close()
			continue
		}
		t.f, t.ino, t.off = f, c.Inode, c.Offset
		for t.drain() {
		}
		t.flushPartial()
		t.close()
		return true
	}
	return false
}

func (t *fileTail) openAt(off int64) bool {
	f, err := os.Open(t.path)
	if err != nil {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return false
	}
	if off > fi.Size() {
		off = 0
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		_ = f.Close()
		return false
	}
	t.f, t.ino, t.off, t.partial = f, inodeOf(fi), off, t.partial[:0]
	return true
}

func (t *fileTail) drain() bool {
	if t.buf == nil {
		t.buf = make([]byte, 64<<10)
	}
	read := 0
	for read < maxLogReadPerPoll {
		n, err := t.f.Read(t.buf)
		if n > 0 {
			read += n
			t.consume(t.buf[:n])
		}
		if err != nil || n == 0 {
			return false
		}
	}
	return true
}

func (t *fileTail) consume(p []byte) {
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			t.off += int64(len(p))
			if !t.skip {
				room := maxLineBytes - len(t.partial)
				t.partial = append(t.partial, p[:min(room, len(p))]...)
			}
			return
		}
		t.off += int64(i + 1)
		if t.skip {
			t.skip = false
		} else {
			if room := maxLineBytes - len(t.partial); room > 0 {
				t.partial = append(t.partial, p[:min(room, i)]...)
			}
			t.emit()
		}
		p = p[i+1:]
	}
}

func (t *fileTail) flushPartial() {
	if len(t.partial) > 0 {
		t.emit()
	}
}

func (t *fileTail) emit() {
	line := strings.TrimRight(string(t.partial), "\r")
	t.partial = t.partial[:0]
	if strings.TrimSpace(line) == "" {
		return
	}
	t.em.Log(protocol.LogLine{Source: t.source, Line: line, Cursor: protocol.FileCursor{Inode: t.ino, Offset: t.off}.String()})
}

type dockerContainer struct {
	id, name, state string
}

func (d *DockerExecutor) listContainers(ctx context.Context) ([]dockerContainer, error) {
	out, err := d.capture(ctx, "docker ps", Command{Args: []string{dockerBin, "ps", "--all", "--no-trunc",
		"--format", "{{.ID}}\t{{.Names}}\t{{.State}}"}})
	if err != nil {
		return nil, err
	}
	var list []dockerContainer
	for l := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(l, "\t")
		if len(f) != 3 || f[0] == "" {
			continue
		}
		name, _, _ := strings.Cut(f[1], ",")
		list = append(list, dockerContainer{id: f[0], name: strings.TrimPrefix(name, "/"), state: f[2]})
	}
	return list, nil
}

type containerLogs struct {
	d    *DockerExecutor
	em   Emitter
	tail int

	mu      sync.Mutex
	since   map[string]time.Time
	active  map[string]bool
	checked map[string]time.Time
}

func (d *DockerExecutor) LogsContainers(ctx context.Context, op protocol.LogsContainers, em Emitter) error {
	s := &containerLogs{d: d, em: em, tail: op.TailLines, since: maps.Clone(op.Since), active: map[string]bool{}, checked: map[string]time.Time{}}
	if s.tail == 0 {
		s.tail = protocol.DefaultLogTailLine
	}
	if s.since == nil {
		s.since = map[string]time.Time{}
	}
	em.Progress(protocol.Progress{State: protocol.LogStateFollowing})
	var wg sync.WaitGroup
	defer wg.Wait()
	t := time.NewTicker(d.containerPoll())
	defer t.Stop()
	dockerDown := false
	for {
		list, err := d.listContainers(ctx)
		switch {
		case err != nil && ctx.Err() == nil && !dockerDown:
			dockerDown = true
			em.Log(protocol.LogLine{Source: protocol.LogSourceDocker, Stream: protocol.LogStreamGap, Line: "docker is not answering inside the engine, container logs are paused: " + err.Error()})
		case err == nil && dockerDown:
			dockerDown = false
			em.Log(protocol.LogLine{Source: protocol.LogSourceDocker, Stream: protocol.LogStreamGap, Line: "docker is answering again, container logs resumed"})
		}
		now := d.now()
		for _, c := range list {
			if !protocol.ValidContainerName(c.name) {
				continue
			}
			follow := c.state == "running"
			s.mu.Lock()
			if s.active[c.id] || (!follow && now.Sub(s.checked[c.id]) < stoppedContainerCheck) {
				s.mu.Unlock()
				continue
			}
			s.active[c.id], s.checked[c.id] = true, now
			since := s.since[c.name]
			s.mu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				s.copy(ctx, c, since, follow)
				s.mu.Lock()
				delete(s.active, c.id)
				s.mu.Unlock()
			}()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

func (s *containerLogs) copy(ctx context.Context, c dockerContainer, since time.Time, follow bool) {
	args := []string{dockerBin, "logs", "--timestamps"}
	if follow {
		args = append(args, "--follow")
	}
	if since.IsZero() {
		args = append(args, "--tail", strconv.Itoa(s.tail))
	} else {
		args = append(args, "--since", since.UTC().Format(time.RFC3339Nano))
	}
	args = append(args, c.id)
	out := &splitter{fn: func(l string) { s.emit(c.name, "stdout", since, l) }}
	errw := &splitter{fn: func(l string) { s.emit(c.name, "stderr", since, l) }}
	err := s.d.run(ctx, Command{Args: args, Stdout: out, Stderr: errw})
	out.flush()
	errw.flush()
	if err != nil && ctx.Err() == nil && !errors.Is(err, context.Canceled) && follow {
		s.em.Log(protocol.LogLine{Container: c.name, Stream: protocol.LogStreamGap, Line: fmt.Sprintf("following the logs of %s stopped: %v", c.name, err)})
	}
}

func (s *containerLogs) emit(name, stream string, after time.Time, raw string) {
	ts, msg := splitTimestamp(raw)
	if !after.IsZero() && !ts.IsZero() && !ts.After(after) {
		return
	}
	s.mu.Lock()
	if ts.After(s.since[name]) {
		s.since[name] = ts
	}
	s.mu.Unlock()
	s.em.Log(protocol.LogLine{Container: name, Stream: stream, Time: ts, Line: msg})
}

func splitTimestamp(raw string) (time.Time, string) {
	head, rest, ok := strings.Cut(raw, " ")
	if !ok {
		head, rest = raw, ""
	}
	ts, err := time.Parse(time.RFC3339Nano, head)
	if err != nil {
		return time.Time{}, raw
	}
	return ts, rest
}

type splitter struct {
	fn  func(string)
	buf []byte
}

func (s *splitter) Write(p []byte) (int, error) {
	for _, b := range p {
		if b == '\n' {
			s.line()
			continue
		}
		if len(s.buf) < maxLineBytes {
			s.buf = append(s.buf, b)
		}
	}
	return len(p), nil
}

func (s *splitter) line() {
	l := strings.TrimRight(string(s.buf), "\r")
	s.buf = s.buf[:0]
	if l != "" {
		s.fn(l)
	}
}

func (s *splitter) flush() {
	if len(s.buf) > 0 {
		s.line()
	}
}
