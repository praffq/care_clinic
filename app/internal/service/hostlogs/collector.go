package hostlogs

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/client"
	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/logdir"
)

type Agent interface {
	Op(ctx context.Context, op protocol.Op, result any, onFrame client.FrameFunc) error
}

type Config struct {
	Dir       *logdir.Dir
	Agent     Agent
	Ready     func() bool
	StatePath string
	Notes     []string
	Log       *slog.Logger
	Now       func() time.Time

	BackoffMin  time.Duration
	BackoffMax  time.Duration
	ReadyPoll   time.Duration
	SweepEvery  time.Duration
	SaveEvery   time.Duration
	IdleClose   time.Duration
	StableAfter time.Duration

	MaxFileBytes int64
	Keep         int
	MaxAge       time.Duration
	Budget       int64
	ConsoleQueue int
}

func (c *Config) setDefaults() {
	def := func(p *time.Duration, v time.Duration) {
		if *p <= 0 {
			*p = v
		}
	}
	def(&c.BackoffMin, time.Second)
	def(&c.BackoffMax, time.Minute)
	def(&c.ReadyPoll, 2*time.Second)
	def(&c.SweepEvery, time.Minute)
	def(&c.SaveEvery, 5*time.Second)
	def(&c.IdleClose, 10*time.Minute)
	def(&c.StableAfter, time.Minute)
	def(&c.MaxAge, logdir.MaxAge)
	if c.MaxFileBytes <= 0 {
		c.MaxFileBytes = logdir.MaxFileBytes
	}
	if c.Keep <= 0 {
		c.Keep = logdir.KeepRotated
	}
	if c.Budget <= 0 {
		c.Budget = logdir.Budget
	}
	if c.ConsoleQueue <= 0 {
		c.ConsoleQueue = 4 << 20
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Ready == nil {
		c.Ready = func() bool { return true }
	}
	if c.Log == nil {
		c.Log = slog.New(slog.DiscardHandler)
	}
}

type Collector struct {
	cfg    Config
	notes  *logdir.Writer
	system *logdir.Writer
	state  *state

	mu         sync.Mutex
	containers map[string]*logdir.Writer
	once       map[string]bool
}

func New(cfg Config) *Collector {
	cfg.setDefaults()
	c := &Collector{
		cfg:        cfg,
		notes:      logdir.NewWriter(cfg.Dir, CollectorLog, cfg.MaxFileBytes, cfg.Keep),
		system:     logdir.NewWriter(cfg.Dir, SystemLog, cfg.MaxFileBytes, cfg.Keep),
		containers: map[string]*logdir.Writer{},
		once:       map[string]bool{},
	}
	st, err := loadState(cfg.StatePath)
	if err != nil {
		c.Note("saved log positions in %s were unreadable (%v); guest logs start again from their most recent lines", cfg.StatePath, err)
	}
	c.state = st
	return c
}

func (c *Collector) Dir() string { return c.cfg.Dir.Path() }

func (c *Collector) Note(format string, args ...any) {
	line := c.cfg.Now().Format(time.RFC3339) + " " + fmt.Sprintf(format, args...) + "\n"
	if _, err := c.notes.Write([]byte(line)); err != nil {
		c.cfg.Log.Warn("collector.log write failed", "err", err)
	}
}

func (c *Collector) ConsoleWriter() io.WriteCloser {
	w := logdir.NewWriter(c.cfg.Dir, ConsoleLog, c.cfg.MaxFileBytes, c.cfg.Keep)
	a := logdir.NewAsync(w, c.cfg.ConsoleQueue, func(n int64) {
		c.Note("engine console: dropped %d bytes of VM console output because the log disk could not keep up", n)
	})
	return closeBoth{a, w}
}

type closeBoth struct {
	*logdir.Async
	w *logdir.Writer
}

func (c closeBoth) Close() error {
	_ = c.Async.Close()
	return c.w.Close()
}

func (c *Collector) Run(ctx context.Context) {
	for _, n := range c.cfg.Notes {
		c.Note("%s", n)
	}
	c.Note("collecting CARE Clinic logs into %s", c.cfg.Dir.Path())
	var wg sync.WaitGroup
	wg.Go(func() { c.watchEngine(ctx) })
	wg.Go(func() { c.stream(ctx, "guest system logs", c.runSystem) })
	wg.Go(func() { c.stream(ctx, "container logs", c.runContainers) })
	wg.Go(func() { c.housekeeping(ctx) })
	wg.Wait()
	if err := c.state.save(false); err != nil {
		c.Note("couldn't save log positions to %s: %v", c.cfg.StatePath, err)
	}
	c.closeWriters()
}

func (c *Collector) closeWriters() {
	c.mu.Lock()
	for name, w := range c.containers {
		_ = w.Close()
		delete(c.containers, name)
	}
	c.mu.Unlock()
	_ = c.system.Close()
	_ = c.notes.Close()
}

func (c *Collector) sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func (c *Collector) watchEngine(ctx context.Context) {
	up := true
	var since time.Time
	for {
		ready := c.cfg.Ready()
		switch {
		case up && !ready:
			up, since = false, c.cfg.Now()
			c.Note("engine is not running or not answering; guest and container log collection is paused")
		case !up && ready:
			up = true
			c.Note("engine is answering again after %s; guest and container logs resume from their saved positions", c.cfg.Now().Sub(since).Round(time.Second))
		}
		if !c.sleep(ctx, c.cfg.ReadyPoll) {
			return
		}
	}
}

func (c *Collector) waitReady(ctx context.Context) bool {
	for !c.cfg.Ready() {
		if !c.sleep(ctx, c.cfg.ReadyPoll) {
			return false
		}
	}
	return true
}

func (c *Collector) stream(ctx context.Context, what string, run func(context.Context, func()) error) {
	backoff := c.cfg.BackoffMin
	var lastErr string
	var lostAt time.Time
	for ctx.Err() == nil {
		if !c.waitReady(ctx) {
			return
		}
		start := c.cfg.Now()
		connected := func() {
			if !lostAt.IsZero() {
				c.Note("%s: reconnected after %s; resuming from the saved position", what, c.cfg.Now().Sub(lostAt).Round(time.Second))
				lostAt, lastErr = time.Time{}, ""
			}
			backoff = c.cfg.BackoffMin
		}
		err := run(ctx, connected)
		if ctx.Err() != nil {
			return
		}
		if c.cfg.Now().Sub(start) >= c.cfg.StableAfter {
			backoff = c.cfg.BackoffMin
		}
		msg := "stream ended"
		if err != nil {
			msg = err.Error()
		}
		if lostAt.IsZero() {
			lostAt = c.cfg.Now()
		}
		if msg != lastErr && c.cfg.Ready() {
			c.Note("%s: interrupted (%s); reconnecting with backoff, lines written meanwhile are fetched when it reconnects", what, msg)
			lastErr = msg
		}
		if !c.sleep(ctx, backoff) {
			return
		}
		backoff = min(backoff*2, c.cfg.BackoffMax)
	}
}

func isFollowing(f protocol.Frame) bool {
	return f.Progress != nil && f.Progress.State == protocol.LogStateFollowing
}

func (c *Collector) runSystem(ctx context.Context, connected func()) error {
	op := protocol.LogsSystem{Cursors: c.state.systemCursors()}
	return c.cfg.Agent.Op(ctx, op, nil, func(f protocol.Frame) {
		if isFollowing(f) {
			connected()
			return
		}
		l := f.Log
		if l == nil {
			return
		}
		connected()
		if l.Stream == protocol.LogStreamGap {
			c.Note("guest %s log: %s", l.Source, l.Line)
			return
		}
		src := l.Source
		if src == "" {
			src = "guest"
		}
		if _, err := c.system.Write([]byte(src + ": " + l.Line + "\n")); err != nil {
			c.writeFailed(SystemLog, err)
			return
		}
		if cur, ok := protocol.ParseFileCursor(l.Cursor); ok && l.Source != "" {
			c.state.setSystem(l.Source, cur)
		}
	})
}

func (c *Collector) runContainers(ctx context.Context, connected func()) error {
	op := protocol.LogsContainers{Since: c.state.containerSince()}
	return c.cfg.Agent.Op(ctx, op, nil, func(f protocol.Frame) {
		if isFollowing(f) {
			connected()
			return
		}
		l := f.Log
		if l == nil {
			return
		}
		connected()
		if l.Stream == protocol.LogStreamGap {
			who := l.Container
			if who == "" {
				who = l.Source
			}
			c.Note("containers (%s): %s", who, l.Line)
			return
		}
		if !protocol.ValidContainerName(l.Container) {
			return
		}
		w, err := c.containerWriter(l.Container)
		if err != nil {
			c.writeFailed(path.Join(ContainersDir, l.Container+".log"), err)
			return
		}
		var b strings.Builder
		if !l.Time.IsZero() {
			b.WriteString(l.Time.UTC().Format(time.RFC3339Nano))
			b.WriteByte(' ')
		}
		if l.Stream == "stderr" {
			b.WriteString("stderr: ")
		}
		b.WriteString(l.Line)
		b.WriteByte('\n')
		if _, err := w.Write([]byte(b.String())); err != nil {
			c.writeFailed(w.Name(), err)
			return
		}
		c.state.setContainer(l.Container, l.Time, c.cfg.Now())
	})
}

func (c *Collector) writeFailed(name string, err error) {
	c.mu.Lock()
	key := "write|" + name + "|" + err.Error()
	seen := c.once[key]
	c.once[key] = true
	c.mu.Unlock()
	if !seen {
		c.Note("couldn't write %s: %v; lines are being dropped", name, err)
	}
}

func (c *Collector) containerWriter(name string) (*logdir.Writer, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if w, ok := c.containers[name]; ok {
		return w, nil
	}
	if err := c.cfg.Dir.EnsureDir(ContainersDir); err != nil {
		return nil, err
	}
	w := logdir.NewWriter(c.cfg.Dir, path.Join(ContainersDir, name+".log"), c.cfg.MaxFileBytes, c.cfg.Keep)
	c.containers[name] = w
	return w, nil
}

func (c *Collector) inUse(rel string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, w := range c.containers {
		if w.Name() == rel {
			return true
		}
	}
	return false
}

func (c *Collector) closeIdle() {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	for name, w := range c.containers {
		if now.Sub(w.LastWrite()) > c.cfg.IdleClose {
			_ = w.Close()
			delete(c.containers, name)
		}
	}
}

func (c *Collector) Sweep() {
	c.closeIdle()
	actions, err := c.cfg.Dir.Sweep(logdir.SweepOptions{MaxAge: c.cfg.MaxAge, Budget: c.cfg.Budget, Now: c.cfg.Now(), InUse: c.inUse})
	if err != nil {
		c.Note("retention: couldn't check the log folder: %v", err)
	}
	for _, a := range actions {
		if a.Err != nil {
			c.Note("retention: couldn't remove old logs from %s: %v", a.File, a.Err)
			continue
		}
		c.Note("retention: %s %s (%d bytes): %s", a.Action, a.File, a.Bytes, a.Reason)
	}
	c.state.prune(c.cfg.Now().Add(-c.cfg.MaxAge))
}

func (c *Collector) housekeeping(ctx context.Context) {
	c.Sweep()
	sweep := time.NewTicker(c.cfg.SweepEvery)
	defer sweep.Stop()
	save := time.NewTicker(c.cfg.SaveEvery)
	defer save.Stop()
	var lastSaveErr string
	for {
		select {
		case <-ctx.Done():
			return
		case <-sweep.C:
			c.Sweep()
		case <-save.C:
			if err := c.state.save(false); err != nil && err.Error() != lastSaveErr {
				lastSaveErr = err.Error()
				c.Note("couldn't save log positions to %s: %v", c.cfg.StatePath, err)
			} else if err == nil {
				lastSaveErr = ""
			}
		}
	}
}
