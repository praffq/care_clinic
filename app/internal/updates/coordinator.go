// Package updates is the update coordinator: the only code that changes the
// installed desktop, engine and CARE versions. It checks the signed feed,
// plans and pre-fetches an update, and applies it one component at a time
// through a crash-safe journal, rolling back on failure. Nothing is applied
// automatically; callers decide when to Check, Apply, Resume or Rollback.
//
// The coordinator talks to the outside world only through the Service, Agent
// and Stack interfaces (see adapters.go for the real ones over the control
// socket), so every step and failure point is testable with fakes
// (updatestest). See docs/updates.md.
package updates

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
	"github.com/ohcnetwork/care_desktop/app/internal/feed"
	"github.com/ohcnetwork/care_desktop/app/internal/service/api"
	"github.com/ohcnetwork/care_desktop/app/internal/service/slots"
	"github.com/ohcnetwork/care_desktop/app/internal/stack"
	"github.com/ohcnetwork/care_desktop/app/internal/versions"
)

// MaxAttempts is how many times a step runs before the operation is rolled
// back (or, for a rollback step, before the rollback is declared failed).
const MaxAttempts = 3

// Service is the part of the host service's control client the coordinator
// uses; *control.Client implements it.
type Service interface {
	Slots(ctx context.Context) (api.SlotList, error)
	InstallEngine(ctx context.Context, tarball io.Reader, size int64, sha256 string) (slots.Slot, error)
	// Activate switches to an installed slot and boots it.
	Activate(ctx context.Context, version string) (api.EngineStatus, error)
}

// Agent runs typed ops on the engine agent (through the service relay).
type Agent interface {
	Status(ctx context.Context) (protocol.Status, error)
	Op(ctx context.Context, op protocol.Op, result any) error
	// Backup takes a backup set of the database and uploaded files that
	// Restore can bring back, and returns its id.
	Backup(ctx context.Context) (string, error)
	// Restore replaces the database and uploaded files with set id. Like
	// db.restore it may migrate and start the stack with the env currently
	// written, so the previous env is written first.
	Restore(ctx context.Context, id string) error
}

// Stack is the clinic stack's host-side runtime.
type Stack interface {
	// EnsureImages pulls or loads every image; the agent verifies digests.
	// It is cheap when they are already present.
	EnsureImages(ctx context.Context, images []stack.Image) error
	// WriteEnv renders the compose kit pinned to images and uploads it.
	WriteEnv(ctx context.Context, images []stack.Image) error
	// Health returns nil when the running stack is healthy.
	Health(ctx context.Context) error
	// DBSize estimates the database size in bytes (0 when unknown).
	DBSize(ctx context.Context) (int64, error)
}

// Fetcher fetches updates.json and updates.json.sig.
type Fetcher interface {
	Fetch(ctx context.Context) (data, sig []byte, err error)
}

// Options configures a Coordinator. Dir, Feed, Keys, Service, Agent, Stack
// and DesktopVersion are required.
type Options struct {
	// Dir holds the state files (verified.json, installed.json,
	// journal.json, history.json) and the download cache.
	Dir  string
	Feed Fetcher
	Keys feed.TrustedKeys
	// AllowInsecureURLs accepts http URLs (feed.DevBuild).
	AllowInsecureURLs bool
	// HTTP downloads assets; nil means http.DefaultClient.
	HTTP *http.Client

	Service Service
	Agent   Agent
	Stack   Stack

	// DesktopVersion is the running desktop app's version.
	DesktopVersion string
	// DesktopPlatform (feed.Platform*) and EngineArch (feed.Arch*) default
	// to this machine's.
	DesktopPlatform string
	EngineArch      string
	// MaxDataSchema is the highest engine data_schema this build supports;
	// 0 accepts any.
	MaxDataSchema int

	// ImageReserveBytes is the free space preflight requires on top of
	// twice the database size; 0 means 2 GiB.
	ImageReserveBytes uint64
	// ClockSkew is how far the clock may be behind the feed's
	// published_at; 0 means 1 hour.
	ClockSkew time.Duration
	// RetryDelay separates step attempts (0 means 5s; negative none).
	RetryDelay time.Duration
	// BootTimeout bounds waiting for an activated engine (0 means 5m);
	// PollInterval is the status poll period (0 means 2s).
	BootTimeout  time.Duration
	PollInterval time.Duration

	Now func() time.Time
	Log func(string)

	// TestHook, when set, is called at "before:<step>" and "after:<step>"
	// of every step (rollback steps included). A non-nil error aborts the
	// operation right there and is returned as is, leaving the journal as
	// a crashed process would. Tests only.
	TestHook func(point string) error
}

// Errors.
var (
	ErrBusy        = errors.New("another update operation is running")
	ErrInFlight    = errors.New("an update operation is unfinished; resume or roll it back")
	ErrNoJournal   = errors.New("no update operation to resume or roll back")
	ErrNotVerified = errors.New("no verified update feed; check for updates first")
	ErrUpToDate    = errors.New("everything is up to date")
	// ErrNotAvailable wraps the reason no update can be installed.
	ErrNotAvailable = errors.New("update not available")
	// ErrDesktopUpdate means the next step updates the desktop app: its
	// installer is downloaded and verified (ApplyResult.Desktop) but
	// installing it is up to the caller.
	ErrDesktopUpdate = errors.New("the desktop app must be updated first")
	// ErrRollbackFailed means the rollback did not finish; the journal is
	// kept in StateFailed until Rollback succeeds.
	ErrRollbackFailed = errors.New("rollback failed")
)

// OpError reports a failed operation and how it ended.
type OpError struct {
	Component string
	To        string
	Step      string
	Result    string // ResultRolledBack or ResultRollbackFailed
	Err       error
	// RollbackErr is set when Result is ResultRollbackFailed.
	RollbackErr error
}

func (e *OpError) Error() string {
	s := fmt.Sprintf("%s update to %s failed", e.Component, e.To)
	if e.Step != "" {
		s += " at " + e.Step
	}
	s += ": " + e.Err.Error()
	if e.Result == ResultRollbackFailed {
		return s + "; rollback failed: " + e.RollbackErr.Error()
	}
	return s + " (rolled back)"
}

func (e *OpError) Unwrap() []error {
	if e.Result == ResultRollbackFailed {
		return []error{e.Err, ErrRollbackFailed}
	}
	return []error{e.Err}
}

// Coordinator applies updates. One operation runs at a time, across
// processes sharing Dir.
type Coordinator struct {
	o  Options
	st store
	mu sync.Mutex
}

// Open creates a Coordinator. A journal left running by a process that is
// gone becomes StateUncertain (or StateRollbackUncertain).
func Open(o Options) (*Coordinator, error) {
	switch {
	case o.Dir == "":
		return nil, errors.New("updates: Dir required")
	case o.Feed == nil || o.Service == nil || o.Agent == nil || o.Stack == nil:
		return nil, errors.New("updates: Feed, Service, Agent and Stack required")
	case len(o.Keys) == 0:
		return nil, errors.New("updates: no trusted feed keys")
	}
	if _, err := versions.ParseSemver(o.DesktopVersion); err != nil {
		return nil, fmt.Errorf("updates: desktop version: %w", err)
	}
	if o.DesktopPlatform == "" {
		o.DesktopPlatform = runtime.GOOS + "-" + runtime.GOARCH
	}
	if o.EngineArch == "" {
		o.EngineArch = runtime.GOARCH
	}
	if o.HTTP == nil {
		o.HTTP = http.DefaultClient
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.ImageReserveBytes == 0 {
		o.ImageReserveBytes = 2 << 30
	}
	if o.ClockSkew == 0 {
		o.ClockSkew = time.Hour
	}
	if o.RetryDelay == 0 {
		o.RetryDelay = 5 * time.Second
	}
	if o.BootTimeout == 0 {
		o.BootTimeout = 5 * time.Minute
	}
	if o.PollInterval == 0 {
		o.PollInterval = 2 * time.Second
	}
	if err := os.MkdirAll(o.Dir, 0o700); err != nil {
		return nil, err
	}
	c := &Coordinator{o: o, st: store{dir: o.Dir}}
	unlock, err := c.acquire()
	if errors.Is(err, ErrBusy) {
		return c, nil // the other process owns the journal
	}
	if err != nil {
		return nil, err
	}
	unlock()
	return c, nil
}

func (c *Coordinator) logf(format string, a ...any) {
	if c.o.Log != nil {
		c.o.Log(fmt.Sprintf(format, a...))
	}
}

func (c *Coordinator) now() time.Time { return c.o.Now().UTC() }

// acquire takes the in-process and cross-process locks and marks a journal
// whose owner is gone as uncertain.
func (c *Coordinator) acquire() (func(), error) {
	if !c.mu.TryLock() {
		return nil, ErrBusy
	}
	fl, err := lockDir(c.st.path(lockFile))
	if err != nil {
		c.mu.Unlock()
		return nil, err
	}
	unlock := func() {
		fl.unlock()
		c.mu.Unlock()
	}
	j, err := c.st.journal()
	if err == nil && j != nil {
		next := map[JournalState]JournalState{StateRunning: StateUncertain, StateRollingBack: StateRollbackUncertain}[j.State]
		if next != "" {
			c.logf("Found an interrupted %s update to %s (%s); it is now %s", j.Component, j.To, j.State, next)
			j.State = next
			err = c.saveJournal(j)
		}
	}
	if err != nil {
		unlock()
		return nil, err
	}
	return unlock, nil
}

// Status is a snapshot of the coordinator's state files.
type Status struct {
	Installed Installed      `json:"installed"`
	Revision  uint64         `json:"highest_revision"`
	CheckedAt time.Time      `json:"last_verified_at,omitzero"`
	Journal   *Journal       `json:"journal,omitempty"`
	Last      *HistoryEntry  `json:"last,omitempty"`
	History   []HistoryEntry `json:"-"`
}

// Status reads the state files without taking the lock.
func (c *Coordinator) Status() (Status, error) {
	var s Status
	var err error
	if s.Installed, err = c.installed(); err != nil {
		return s, err
	}
	v, err := c.st.verified()
	if err != nil {
		return s, err
	}
	s.Revision, s.CheckedAt = v.HighestRevision, v.LastVerifiedAt
	if s.Journal, err = c.st.journal(); err != nil {
		return s, err
	}
	if s.History, err = c.st.history(); err != nil {
		return s, err
	}
	if n := len(s.History); n > 0 {
		s.Last = &s.History[n-1]
	}
	return s, nil
}

// History returns history.json, oldest first.
func (c *Coordinator) History() ([]HistoryEntry, error) { return c.st.history() }

func (c *Coordinator) installed() (Installed, error) {
	in, err := c.st.installed()
	in.Desktop = c.o.DesktopVersion
	return in, err
}

// PlannedStep is one step of a plan.
type PlannedStep struct {
	Component string `json:"component"`
	From      string `json:"from,omitempty"`
	To        string `json:"to"`
}

// CheckResult is the outcome of Check.
type CheckResult struct {
	Revision  uint64        `json:"revision"`
	Available bool          `json:"available"`
	Reason    string        `json:"reason,omitempty"` // why nothing is available
	Urgent    bool          `json:"urgent,omitempty"`
	Steps     []PlannedStep `json:"steps,omitempty"`
	Held      []string      `json:"held,omitempty"`
	// Prefetched is true once every artifact of the plan is downloaded and
	// staged; PrefetchError says why not.
	Prefetched    bool   `json:"prefetched"`
	PrefetchError string `json:"prefetch_error,omitempty"`
}

// Check fetches and verifies the feed, records it in verified.json, plans
// an update and pre-fetches its artifacts (downloads, inactive engine slot,
// image pulls). It never changes the installed versions. Feed errors wrap
// feed.ErrBadSignature, feed.ErrUnknownKey, feed.ErrRollback,
// feed.ErrExpired or feed.ErrInvalid; verified.json is then left unchanged.
func (c *Coordinator) Check(ctx context.Context) (*CheckResult, error) {
	unlock, err := c.acquire()
	if err != nil {
		return nil, err
	}
	defer unlock()

	data, sig, err := c.o.Feed.Fetch(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetch update feed: %w", err)
	}
	ver, err := c.st.verified()
	if err != nil {
		return nil, err
	}
	now := c.now()
	m, err := feed.Verify(data, sig, feed.VerifyOptions{Keys: c.o.Keys, Now: now, MinRevision: ver.HighestRevision, AllowInsecureURLs: c.o.AllowInsecureURLs})
	if err != nil {
		c.logf("Rejected the update feed: %v", err)
		return nil, fmt.Errorf("update feed rejected: %w", err)
	}
	fs := feed.State{HighestRevision: ver.HighestRevision}
	fs.Record(data, m, now)
	ver = Verified{HighestRevision: fs.HighestRevision, LastVerifiedAt: fs.LastVerifiedAt, ManifestSHA256: fs.ManifestSHA256, Manifest: data, Signature: sig}
	if err := c.st.save(VerifiedFile, ver); err != nil {
		return nil, err
	}
	c.logf("Verified update feed revision %d", m.Revision)

	in, err := c.installed()
	if err != nil {
		return nil, err
	}
	res := &CheckResult{Revision: m.Revision}
	if in, err = c.adopt(ctx, m, in); errors.Is(err, ErrUnknownCare) {
		res.Reason = err.Error()
		return res, nil
	} else if err != nil {
		return nil, err
	}
	p, err := c.resolve(m, in)
	var np *feed.NoPathError
	switch {
	case errors.As(err, &np):
		res.Reason = np.Error()
		return res, nil
	case err != nil:
		return nil, err
	case len(p.Steps) == 0:
		res.Reason = ErrUpToDate.Error()
		return res, nil
	}
	res.Available, res.Urgent, res.Held = true, p.Urgent, p.Held
	for _, s := range p.Steps {
		res.Steps = append(res.Steps, PlannedStep{Component: s.Component, From: s.From, To: s.To})
	}
	if err := c.prefetch(ctx, m, in, p); err != nil {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		res.PrefetchError = err.Error()
		c.logf("Pre-fetch incomplete: %v", err)
	} else {
		res.Prefetched = true
	}
	return res, nil
}

// manifest re-verifies verified.json's feed (signature, revision, expiry).
func (c *Coordinator) manifest() (*feed.Manifest, Verified, error) {
	ver, err := c.st.verified()
	if err != nil {
		return nil, ver, err
	}
	if len(ver.Manifest) == 0 {
		return nil, ver, ErrNotVerified
	}
	m, err := feed.Verify(ver.Manifest, ver.Signature, feed.VerifyOptions{Keys: c.o.Keys, Now: c.now(), MinRevision: ver.HighestRevision, AllowInsecureURLs: c.o.AllowInsecureURLs})
	if err != nil {
		return nil, ver, fmt.Errorf("verified feed no longer valid (check for updates again): %w", err)
	}
	return m, ver, nil
}

func (c *Coordinator) resolve(m *feed.Manifest, in Installed) (*feed.Plan, error) {
	fi := feed.Installed{DesktopAsset: c.o.DesktopPlatform, EngineArch: c.o.EngineArch}
	var err error
	if fi.Desktop, err = versions.ParseSemver(in.Desktop); err != nil {
		return nil, fmt.Errorf("desktop version: %w", err)
	}
	if in.Engine != nil {
		if fi.Engine, err = versions.ParseSemver(in.Engine.Version); err != nil {
			return nil, fmt.Errorf("installed engine: %w", err)
		}
	}
	if in.Care != nil {
		v, err := versions.ParseSemver(in.Care.Version)
		if err != nil {
			return nil, fmt.Errorf("installed CARE: %w", err)
		}
		fi.Care = &feed.CareVersion{Version: v, ReleasedAt: in.Care.ReleasedAt}
	}
	return feed.ResolvePlan(fi, m)
}

// DesktopDownload is a verified desktop installer ready to be installed.
type DesktopDownload struct {
	Version string `json:"version"`
	Path    string `json:"path"`
}

// ApplyResult lists what Apply did.
type ApplyResult struct {
	Applied []HistoryEntry   `json:"applied,omitempty"`
	Desktop *DesktopDownload `json:"desktop,omitempty"`
}

// Apply installs the plan for the verified feed, one component at a time;
// each step commits before the next starts. It stops at the first failure
// (an *OpError, rolled back or not), at a desktop step (ErrDesktopUpdate),
// or returns ErrUpToDate / ErrNotAvailable when there is nothing to do.
func (c *Coordinator) Apply(ctx context.Context) (*ApplyResult, error) {
	unlock, err := c.acquire()
	if err != nil {
		return nil, err
	}
	defer unlock()
	if j, err := c.st.journal(); err != nil {
		return nil, err
	} else if j != nil {
		return nil, fmt.Errorf("%w (%s update to %s is %s)", ErrInFlight, j.Component, j.To, j.State)
	}
	m, ver, err := c.manifest()
	if err != nil {
		return nil, err
	}
	in, err := c.installed()
	if err != nil {
		return nil, err
	}
	if in, err = c.adopt(ctx, m, in); errors.Is(err, ErrUnknownCare) {
		return nil, fmt.Errorf("%w: %w", ErrNotAvailable, err)
	} else if err != nil {
		return nil, err
	}
	p, err := c.resolve(m, in)
	var np *feed.NoPathError
	switch {
	case errors.As(err, &np):
		return nil, fmt.Errorf("%w: %s", ErrNotAvailable, np.Error())
	case err != nil:
		return nil, err
	case len(p.Steps) == 0:
		return nil, ErrUpToDate
	}
	res := &ApplyResult{}
	for _, s := range p.Steps {
		if s.Component == feed.ComponentDesktop {
			path, err := c.downloadDesktop(ctx, s.Desktop)
			if err != nil {
				return res, err
			}
			res.Desktop = &DesktopDownload{Version: s.To, Path: path}
			return res, ErrDesktopUpdate
		}
		if in, err = c.installed(); err != nil {
			return res, err
		}
		j, err := c.newJournal(m, ver.HighestRevision, in, s)
		if err != nil {
			return res, err
		}
		if err := c.run(ctx, j); err != nil {
			return res, err
		}
		h, err := c.st.history()
		if err != nil {
			return res, err
		}
		res.Applied = append(res.Applied, h[len(h)-1])
	}
	return res, nil
}

// Resume continues an interrupted operation: forward when it was applying,
// or its rollback when it was rolling back.
func (c *Coordinator) Resume(ctx context.Context) error {
	unlock, err := c.acquire()
	if err != nil {
		return err
	}
	defer unlock()
	j, err := c.st.journal()
	if err != nil {
		return err
	}
	if j == nil {
		return ErrNoJournal
	}
	switch j.State {
	case StateUncertain:
		c.logf("Resuming the %s update to %s", j.Component, j.To)
		return c.run(ctx, j)
	case StateRollbackUncertain:
		c.logf("Resuming the rollback of the %s update to %s", j.Component, j.To)
		return c.rollback(ctx, j, errors.New(j.Error))
	default:
		return fmt.Errorf("%w: the rollback failed; use Rollback to retry it", ErrRollbackFailed)
	}
}

// Rollback undoes an unfinished operation, or retries a failed rollback.
func (c *Coordinator) Rollback(ctx context.Context) error {
	unlock, err := c.acquire()
	if err != nil {
		return err
	}
	defer unlock()
	j, err := c.st.journal()
	if err != nil {
		return err
	}
	if j == nil {
		return ErrNoJournal
	}
	c.logf("Rolling back the %s update to %s", j.Component, j.To)
	if j.State == StateUncertain {
		err = c.rollback(ctx, j, errors.New("rolled back on request"))
	} else {
		for i := range j.RollbackSteps {
			if s := &j.RollbackSteps[i]; s.Status != StepDone {
				s.Status, s.Attempts = StepPending, 0
			}
		}
		err = c.rollback(ctx, j, errors.New(j.Error))
	}
	// The rollback was asked for, so finishing it is success.
	if oe, ok := errors.AsType[*OpError](err); ok && oe.Result == ResultRolledBack {
		return nil
	}
	return err
}

func (c *Coordinator) saveJournal(j *Journal) error {
	j.UpdatedAt = c.now()
	return c.st.save(JournalFile, j)
}

func (c *Coordinator) hook(point string) error {
	if c.o.TestHook == nil {
		return nil
	}
	return c.o.TestHook(point)
}

// stepError is a step that failed MaxAttempts times.
type stepError struct {
	step string
	err  error
}

func (e *stepError) Error() string { return e.step + ": " + e.err.Error() }

type stepFunc func(ctx context.Context, j *Journal) error

// runSteps runs the steps not yet done, each at most MaxAttempts times
// counting attempts made before a crash. It returns a *stepError when a step
// gives up; any other error (cancellation, test crash) aborts as is.
func (c *Coordinator) runSteps(ctx context.Context, j *Journal, steps []StepRecord, fns map[string]stepFunc) error {
	for i := range steps {
		s := &steps[i]
		if s.Status == StepDone {
			continue
		}
		fn := fns[s.Name]
		if fn == nil {
			return fmt.Errorf("journal: unknown step %q", s.Name)
		}
		for {
			if s.Attempts >= MaxAttempts {
				s.Status = StepFailed
				if err := c.saveJournal(j); err != nil {
					return err
				}
				return &stepError{step: s.Name, err: errors.New(s.Error)}
			}
			s.Attempts++
			s.Status, s.At = StepRunning, c.now()
			if err := c.saveJournal(j); err != nil {
				return err
			}
			if err := c.hook("before:" + s.Name); err != nil {
				return err
			}
			err := fn(ctx, j)
			if herr := c.hook("after:" + s.Name); herr != nil {
				return herr
			}
			if err == nil {
				s.Status, s.Error = StepDone, ""
				if err := c.saveJournal(j); err != nil {
					return err
				}
				break
			}
			s.Error = err.Error()
			c.logf("%s: %s (attempt %d of %d)", s.Name, err, s.Attempts, MaxAttempts)
			if ctx.Err() != nil {
				_ = c.saveJournal(j)
				return ctx.Err()
			}
			if s.Attempts < MaxAttempts {
				s.Status = StepPending
			}
			if err := c.saveJournal(j); err != nil {
				return err
			}
			if s.Attempts < MaxAttempts {
				if err := sleep(ctx, c.o.RetryDelay); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// run executes j's forward steps, then commits or rolls back.
func (c *Coordinator) run(ctx context.Context, j *Journal) error {
	j.State = StateRunning
	if err := c.saveJournal(j); err != nil {
		return err
	}
	err := c.runSteps(ctx, j, j.Steps, c.forwardSteps(j))
	var se *stepError
	switch {
	case errors.As(err, &se):
		j.FailedStep = se.step
		return c.rollback(ctx, j, se.err)
	case err != nil:
		if ctx.Err() != nil {
			j.State = StateUncertain
			_ = c.saveJournal(j)
		}
		return err
	}
	return c.commit(j)
}

// rollback undoes j after cause; it continues an earlier rollback when the
// journal already has rollback steps.
func (c *Coordinator) rollback(ctx context.Context, j *Journal, cause error) error {
	if j.State != StateRollbackUncertain && j.State != StateFailed {
		j.Error = cause.Error()
		j.RollbackSteps = nil
		for _, name := range c.rollbackPlan(j) {
			j.RollbackSteps = append(j.RollbackSteps, StepRecord{Name: name, Status: StepPending})
		}
		c.logf("%s update to %s failed: %s; rolling back", j.Component, j.To, j.Error)
	}
	j.State = StateRollingBack
	if err := c.saveJournal(j); err != nil {
		return err
	}
	opErr := &OpError{Component: j.Component, To: j.To, Step: j.FailedStep, Err: cause}
	err := c.runSteps(ctx, j, j.RollbackSteps, c.rollbackSteps(j))
	var se *stepError
	switch {
	case errors.As(err, &se):
		j.State = StateFailed
		if err := c.saveJournal(j); err != nil {
			return err
		}
		opErr.Result, opErr.RollbackErr = ResultRollbackFailed, se
		if err := c.finish(j, ResultRollbackFailed); err != nil {
			return err
		}
		c.logf("Rollback failed: %v", se)
		return opErr
	case err != nil:
		if ctx.Err() != nil {
			j.State = StateRollbackUncertain
			_ = c.saveJournal(j)
		}
		return err
	}
	opErr.Result = ResultRolledBack
	if err := c.finish(j, ResultRolledBack); err != nil {
		return err
	}
	if err := c.st.remove(JournalFile); err != nil {
		return err
	}
	c.logf("Rolled back the %s update to %s", j.Component, j.To)
	return opErr
}

// commit records the new installed version and closes the journal.
func (c *Coordinator) commit(j *Journal) error {
	in, err := c.st.installed()
	if err != nil {
		return err
	}
	switch j.Component {
	case feed.ComponentCare:
		in.Care = &InstalledCare{Version: j.Care.Version, ReleasedAt: j.Care.ReleasedAt, Images: j.Care.Images}
	case feed.ComponentEngine:
		in.Engine = &InstalledEngine{Version: j.Engine.Version, DataSchema: j.Engine.DataSchema, SHA256: j.Engine.SHA256}
	}
	in.Desktop = c.o.DesktopVersion
	in.UpdatedAt = c.now()
	if err := c.st.save(InstalledFile, in); err != nil {
		return err
	}
	if err := c.finish(j, ResultApplied); err != nil {
		return err
	}
	c.logf("Updated %s to %s", j.Component, j.To)
	return c.st.remove(JournalFile)
}

func (c *Coordinator) finish(j *Journal, result string) error {
	e := HistoryEntry{
		ID: j.ID, Component: j.Component, From: j.From, To: j.To, Revision: j.Revision,
		Result: result, BackupID: j.Rollback.BackupID, StartedAt: j.StartedAt, FinishedAt: c.now(),
	}
	if result != ResultApplied {
		e.Error, e.FailedStep = j.Error, j.FailedStep
	}
	return c.st.appendHistory(e)
}

func (c *Coordinator) newJournal(m *feed.Manifest, rev uint64, in Installed, s feed.Step) (*Journal, error) {
	now := c.now()
	j := &Journal{
		ID:        fmt.Sprintf("%s-%s-%d", s.Component, now.Format("20060102T150405Z"), now.Nanosecond()),
		Component: s.Component, From: s.From, To: s.To, Revision: rev,
		StartedAt: now,
	}
	var names []string
	switch s.Component {
	case feed.ComponentCare:
		j.Care = &CareTarget{Version: s.To, ReleasedAt: s.Care.ReleasedAt, Images: careImages(s.Care, m)}
		if in.Care != nil {
			j.Rollback.PreviousImages = in.Care.Images
		}
		names = careSteps
	case feed.ComponentEngine:
		a, ok := s.Engine.Assets[c.o.EngineArch]
		if !ok {
			return nil, fmt.Errorf("engine %s has no %s asset", s.To, c.o.EngineArch)
		}
		j.Engine = &EngineTarget{Version: s.To, DataSchema: s.Engine.DataSchema, URL: a.URL, SHA256: a.SHA256, Size: a.Size, WithCare: in.Care != nil}
		if in.Engine != nil {
			j.Rollback.PreviousEngine = in.Engine.Version
		}
		names = engineSteps
	default:
		return nil, fmt.Errorf("cannot apply %s updates", s.Component)
	}
	for _, n := range names {
		j.Steps = append(j.Steps, StepRecord{Name: n, Status: StepPending})
	}
	return j, nil
}

func (c *Coordinator) forwardSteps(j *Journal) map[string]stepFunc {
	if j.Component == feed.ComponentEngine {
		return c.engineForward()
	}
	return c.careForward()
}

func (c *Coordinator) rollbackPlan(j *Journal) []string {
	if j.Component == feed.ComponentEngine {
		return engineRollbackPlan(j)
	}
	return careRollbackPlan(j)
}

func (c *Coordinator) rollbackSteps(j *Journal) map[string]stepFunc {
	if j.Component == feed.ComponentEngine {
		return c.engineRollback()
	}
	return c.careRollback()
}

// careImages is every image the stack env pins for r: its CARE images plus
// the feed's support images, in a fixed order.
func careImages(r *feed.CareRelease, m *feed.Manifest) []Image {
	var out []Image
	add := func(role protocol.ImageRole, ref feed.ImageRef) {
		repo, _, _ := strings.Cut(ref.Ref, "@")
		out = append(out, Image{Role: role, Repo: repo, Digest: ref.Digest, URL: ref.Tar})
	}
	for _, role := range protocol.ImageRoles {
		if ref, ok := r.Images[role]; ok {
			add(role, ref)
		} else if ref, ok := m.SupportImages[role]; ok {
			add(role, ref)
		}
	}
	return out
}
