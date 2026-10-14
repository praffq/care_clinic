package updates

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/ohcnetwork/care_desktop/app/internal/release"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/atomicfile"
)

type UpdateType string

const (
	CAREImages UpdateType = "care-images"
	Engine     UpdateType = "engine"
	Desktop    UpdateType = "desktop"
)

type PlanState string

const (
	PendingApproval PlanState = "pending_approval"
	Running         PlanState = "running"
	RollingBack     PlanState = "rolling_back"
	Done            PlanState = "done"
	RolledBack      PlanState = "rolled_back"
	Failed          PlanState = "failed"
)

type StepStatus string

const (
	StepPending StepStatus = "pending"
	StepRunning StepStatus = "running"
	StepDone    StepStatus = "done"
	StepFailed  StepStatus = "failed"
	StepSkipped StepStatus = "skipped"
	StepUndone  StepStatus = "undone"
)

type FileDigest struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// Snapshot deliberately has no field for settings contents or secret values.
type Snapshot struct {
	DesktopVersion string       `json:"desktop_version,omitempty"`
	EngineVersion  string       `json:"engine_version,omitempty"`
	BackendDigest  string       `json:"care_backend_digest,omitempty"`
	FrontendDigest string       `json:"care_frontend_digest,omitempty"`
	LocalImageIDs  []string     `json:"local_image_ids,omitempty"`
	SettingsHash   string       `json:"settings_hash,omitempty"`
	Files          []FileDigest `json:"files,omitempty"`
}

type Rollback struct {
	BackupID       string   `json:"backup_id,omitempty"`
	DBMigrated     bool     `json:"db_migrated"`
	EnginePrevious string   `json:"engine_previous,omitempty"`
	ImagesKept     []string `json:"images_kept,omitempty"`
	PreviousApp    string   `json:"previous_app_path,omitempty"`
}

type Step struct {
	Name       string     `json:"name"`
	Status     StepStatus `json:"status"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Attempts   int        `json:"attempts"`
	Error      string     `json:"error,omitempty"`
	Undo       string     `json:"undo,omitempty"`
}

var ErrHandoffPending = errors.New("update step was handed off to another process")

type Journal struct {
	PlanID           string     `json:"plan_id"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	ManifestRevision uint64     `json:"manifest_revision"`
	State            PlanState  `json:"state"`
	ApprovedBy       string     `json:"approved_by,omitempty"`
	ApprovedAt       *time.Time `json:"approved_at,omitempty"`
	From             Snapshot   `json:"from"`
	To               Snapshot   `json:"to"`
	Rollback         Rollback   `json:"rollback"`
	Steps            []Step     `json:"steps"`
}

type HistoryEntry struct {
	PlanID           string    `json:"plan_id"`
	CreatedAt        time.Time `json:"created_at"`
	FinishedAt       time.Time `json:"finished_at"`
	ManifestRevision uint64    `json:"manifest_revision"`
	State            PlanState `json:"state"`
	ApprovedBy       string    `json:"approved_by,omitempty"`
	From             Snapshot  `json:"from"`
	To               Snapshot  `json:"to"`
	Rollback         Rollback  `json:"rollback"`
	Steps            []Step    `json:"steps"`
}

type Hooks struct {
	Preflight func(context.Context, *Journal) error
	Apply     func(context.Context, string, *Journal, Checkpoint) (undo string, err error)
	Rollback  func(context.Context, string, Journal) error
}

// Checkpoint persists in-step recovery metadata before an irreversible action.
// Apply must call it synchronously and stop immediately if it returns an error.
type Checkpoint func() error

type Coordinator struct {
	dir     string
	mu      sync.Mutex
	checkMu sync.Mutex
	now     func() time.Time
}

// Check routes update discovery and advance staging through the coordinator.
// The callback must not activate an update or alter installed-version records.
func (c *Coordinator) Check(ctx context.Context, check func(context.Context) error) error {
	if check == nil {
		return errors.New("update check handler is required")
	}
	c.checkMu.Lock()
	defer c.checkMu.Unlock()
	return check(ctx)
}

func New(dir string) (*Coordinator, error) {
	if !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("update state directory must be absolute: %q", dir)
	}
	c := &Coordinator{dir: dir, now: time.Now}
	if _, err := c.Journal(); err != nil {
		return nil, err
	}
	if _, err := c.History(); err != nil {
		return nil, err
	}
	if _, err := c.State(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Coordinator) JournalPath() string { return filepath.Join(c.dir, "journal.json") }
func (c *Coordinator) HistoryPath() string { return filepath.Join(c.dir, "history.json") }

type PersistentState struct {
	HighestManifestRevision uint64       `json:"highest_manifest_revision"`
	VerifiedManifest        []byte       `json:"verified_manifest,omitempty"`
	Installed               Snapshot     `json:"installed"`
	PreviousArtifacts       []FileDigest `json:"previous_artifacts,omitempty"`
}

func (c *Coordinator) State() (PersistentState, error) {
	var state PersistentState
	err := readJSON(filepath.Join(c.dir, "state.json"), &state)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, fmt.Errorf("read update state: %w", err)
	}
	return state, nil
}

func (c *Coordinator) SaveVerifiedManifest(m release.Manifest, raw []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := m.Validate(); err != nil {
		return fmt.Errorf("refuse unverified update manifest: %w", err)
	}
	state, err := c.State()
	if err != nil {
		return err
	}
	if m.Revision < state.HighestManifestRevision {
		return fmt.Errorf("manifest rollback: revision %d below %d", m.Revision, state.HighestManifestRevision)
	}
	if m.Revision == state.HighestManifestRevision && string(raw) != string(state.VerifiedManifest) {
		return errors.New("manifest revision reused with different contents")
	}
	state.HighestManifestRevision = m.Revision
	state.VerifiedManifest = append([]byte(nil), raw...)
	return c.write(filepath.Join(c.dir, "state.json"), state)
}

func (c *Coordinator) SaveInstalled(snapshot Snapshot, previous ...FileDigest) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	state, err := c.State()
	if err != nil {
		return err
	}
	state.Installed = snapshot
	if previous != nil {
		state.PreviousArtifacts = append([]FileDigest(nil), previous...)
	}
	return c.write(filepath.Join(c.dir, "state.json"), state)
}

func (c *Coordinator) Journal() (*Journal, error) {
	var j Journal
	err := readJSON(c.JournalPath(), &j)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read update journal: %w", err)
	}
	if err := validateJournal(j); err != nil {
		return nil, fmt.Errorf("invalid update journal: %w", err)
	}
	return &j, nil
}

func (c *Coordinator) History() ([]HistoryEntry, error) {
	var history []HistoryEntry
	err := readJSON(c.HistoryPath(), &history)
	if errors.Is(err, os.ErrNotExist) {
		return []HistoryEntry{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read update history: %w", err)
	}
	if history == nil {
		return nil, errors.New("update history must be a JSON array")
	}
	return history, nil
}

func (c *Coordinator) Create(revision uint64, from, to Snapshot, steps []string) (Journal, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	current, err := c.Journal()
	if err != nil {
		return Journal{}, err
	}
	if current != nil {
		return Journal{}, fmt.Errorf("update plan %s is still %s", current.PlanID, current.State)
	}
	if revision == 0 {
		return Journal{}, errors.New("update plan requires a verified manifest revision")
	}
	if len(steps) == 0 {
		return Journal{}, errors.New("update plan has no steps")
	}
	for _, name := range steps {
		if name != string(CAREImages) && name != string(Engine) && name != string(Desktop) {
			return Journal{}, fmt.Errorf("unknown update step %q", name)
		}
	}
	now := c.now().UTC()
	id, err := planID()
	if err != nil {
		return Journal{}, err
	}
	j := Journal{
		PlanID: id, CreatedAt: now, UpdatedAt: now, ManifestRevision: revision,
		State: PendingApproval, From: from, To: to,
		Steps: make([]Step, len(steps)),
	}
	for i, name := range steps {
		j.Steps[i] = Step{Name: name, Status: StepPending}
	}
	return j, c.write(c.JournalPath(), j)
}

func (c *Coordinator) Approve(planID, approvedBy string) (Journal, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	j, err := c.Journal()
	if err != nil {
		return Journal{}, err
	}
	if j == nil || j.PlanID != planID || j.State != PendingApproval {
		return Journal{}, errors.New("update plan is not awaiting approval")
	}
	if strings.TrimSpace(approvedBy) == "" {
		return Journal{}, errors.New("an approving staff identity is required")
	}
	now := c.now().UTC()
	j.ApprovedBy, j.ApprovedAt, j.State, j.UpdatedAt = approvedBy, &now, Running, now
	return *j, c.write(c.JournalPath(), j)
}

func (c *Coordinator) Execute(ctx context.Context, planID string, hooks Hooks) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	j, err := c.Journal()
	if err != nil {
		return err
	}
	if j == nil || j.PlanID != planID || j.State != Running {
		return errors.New("approved update plan is not ready to run")
	}
	if hooks.Preflight == nil || hooks.Apply == nil || hooks.Rollback == nil {
		return errors.New("update execution requires preflight, apply and rollback handlers")
	}
	if err := hooks.Preflight(ctx, j); err != nil {
		j.State, j.UpdatedAt = Failed, c.now().UTC()
		if saveErr := c.write(c.JournalPath(), j); saveErr != nil {
			return errors.Join(err, saveErr)
		}
		if historyErr := c.finish(*j); historyErr != nil {
			return errors.Join(err, historyErr)
		}
		return fmt.Errorf("update preflight failed: %w", err)
	}
	j.UpdatedAt = c.now().UTC()
	if err := c.write(c.JournalPath(), j); err != nil {
		return err
	}
	for i := range j.Steps {
		step := &j.Steps[i]
		if step.Status == StepDone || step.Status == StepSkipped {
			continue
		}
		if step.Attempts >= 3 {
			return c.rollback(ctx, j, hooks, fmt.Errorf("step %s exhausted three attempts", step.Name))
		}
		for step.Attempts < 3 {
			now := c.now().UTC()
			step.Attempts++
			step.Status, step.Error = StepRunning, ""
			if step.StartedAt == nil {
				step.StartedAt = &now
			}
			j.UpdatedAt = now
			if err := c.write(c.JournalPath(), j); err != nil {
				return err
			}
			var checkpointErr error
			checkpoint := func() error {
				j.UpdatedAt = c.now().UTC()
				// Execute already owns mu. Re-entering a public locking method
				// here would deadlock the apply hook.
				if err := c.write(c.JournalPath(), j); err != nil {
					checkpointErr = fmt.Errorf("checkpoint update recovery metadata: %w", err)
				}
				return checkpointErr
			}
			undo, applyErr := hooks.Apply(ctx, step.Name, j, checkpoint)
			if checkpointErr != nil {
				return errors.Join(applyErr, checkpointErr)
			}
			if j.State == RollingBack {
				return c.rollback(ctx, j, hooks, applyErr)
			}
			finished := c.now().UTC()
			step.Undo, j.UpdatedAt = undo, finished
			if errors.Is(applyErr, ErrHandoffPending) {
				step.Status, step.Error, step.FinishedAt = StepRunning, "", nil
				return c.write(c.JournalPath(), j)
			}
			step.FinishedAt = &finished
			if applyErr == nil {
				step.Status = StepDone
				if err := c.write(c.JournalPath(), j); err != nil {
					return err
				}
				break
			}
			step.Status, step.Error = StepFailed, applyErr.Error()
			if err := c.write(c.JournalPath(), j); err != nil {
				return errors.Join(applyErr, err)
			}
			if step.Attempts < 3 {
				step.Status = StepPending
				if err := c.write(c.JournalPath(), j); err != nil {
					return errors.Join(applyErr, err)
				}
				continue
			}
			return c.rollback(ctx, j, hooks, fmt.Errorf("step %s failed after three attempts: %w", step.Name, applyErr))
		}
	}
	j.State, j.UpdatedAt = Done, c.now().UTC()
	if err := c.write(c.JournalPath(), j); err != nil {
		return err
	}
	return c.finish(*j)
}

// CompleteHandoff records that a delegated update helper installed and launched
// the requested desktop version. It is called by the newly launched app.
func (c *Coordinator) CompleteHandoff(planID, version string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	j, err := c.Journal()
	if err != nil {
		return err
	}
	if j == nil || j.PlanID != planID || j.State != Running || j.To.DesktopVersion != version {
		return errors.New("desktop update handoff does not match the active plan")
	}
	for i := range j.Steps {
		if j.Steps[i].Name != string(Desktop) {
			continue
		}
		if j.Steps[i].Status != StepRunning {
			return errors.New("desktop update step is not awaiting handoff")
		}
		now := c.now().UTC()
		j.Steps[i].Status, j.Steps[i].FinishedAt = StepDone, &now
		j.UpdatedAt = now
		return c.write(c.JournalPath(), j)
	}
	return errors.New("active update plan has no desktop handoff step")
}

func (c *Coordinator) Recover(ctx context.Context, hooks Hooks) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	j, err := c.Journal()
	if err != nil || j == nil {
		return err
	}
	switch j.State {
	case PendingApproval:
		j.State, j.UpdatedAt = Failed, c.now().UTC()
		for i := range j.Steps {
			j.Steps[i].Error = "plan was not approved before CARE Clinic closed"
		}
		if err := c.write(c.JournalPath(), j); err != nil {
			return err
		}
		return c.finish(*j)
	case Running, RollingBack:
		if hooks.Rollback == nil {
			return errors.New("interrupted update requires a rollback handler")
		}
		cause := errors.New("interrupted update was rolled back during startup")
		if err := c.rollback(ctx, j, hooks, cause); err == cause {
			return nil
		} else {
			return err
		}
	case Done, RolledBack, Failed:
		return c.finish(*j)
	default:
		return fmt.Errorf("unsupported update journal state %q", j.State)
	}
}

func (c *Coordinator) rollback(ctx context.Context, j *Journal, hooks Hooks, cause error) error {
	j.State, j.UpdatedAt = RollingBack, c.now().UTC()
	if err := c.write(c.JournalPath(), j); err != nil {
		return errors.Join(cause, err)
	}
	var rollbackErrors []error
	for i := len(j.Steps) - 1; i >= 0; i-- {
		step := &j.Steps[i]
		if step.Status != StepDone && step.Status != StepFailed && step.Status != StepRunning {
			continue
		}
		if err := hooks.Rollback(ctx, step.Name, *j); err != nil {
			step.Error = errors.Join(errors.New(step.Error), fmt.Errorf("rollback %s: %w", step.Name, err)).Error()
			j.UpdatedAt = c.now().UTC()
			if saveErr := c.write(c.JournalPath(), j); saveErr != nil {
				rollbackErrors = append(rollbackErrors, saveErr)
			}
			rollbackErrors = append(rollbackErrors, err)
			continue
		}
		step.Status = StepUndone
		if err := c.write(c.JournalPath(), j); err != nil {
			rollbackErrors = append(rollbackErrors, err)
		}
	}
	if len(rollbackErrors) > 0 {
		j.State, j.UpdatedAt = RollingBack, c.now().UTC()
		_ = c.write(c.JournalPath(), j)
		return errors.Join(append([]error{cause}, rollbackErrors...)...)
	}
	j.State, j.UpdatedAt = RolledBack, c.now().UTC()
	if err := c.write(c.JournalPath(), j); err != nil {
		return errors.Join(cause, err)
	}
	if err := c.finish(*j); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

func (c *Coordinator) finish(j Journal) error {
	history, err := c.History()
	if err != nil {
		return err
	}
	for _, entry := range history {
		if entry.PlanID == j.PlanID {
			return c.clearJournal()
		}
	}
	history = append(history, HistoryEntry{
		PlanID: j.PlanID, CreatedAt: j.CreatedAt, FinishedAt: c.now().UTC(),
		ManifestRevision: j.ManifestRevision, State: j.State, ApprovedBy: j.ApprovedBy,
		From: j.From, To: j.To, Rollback: j.Rollback, Steps: j.Steps,
	})
	if err := c.write(c.HistoryPath(), history); err != nil {
		return err
	}
	return c.clearJournal()
}

func (c *Coordinator) clearJournal() error {
	if err := os.Remove(c.JournalPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (c *Coordinator) write(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WritePrivate(path, append(data, '\n'))
}

func readJSON(path string, out any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, out); err != nil {
		return err
	}
	return nil
}

func planID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("create update plan ID: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

func validateJournal(j Journal) error {
	if j.PlanID == "" || j.CreatedAt.IsZero() || j.UpdatedAt.IsZero() || j.ManifestRevision == 0 {
		return errors.New("required journal fields are missing")
	}
	switch j.State {
	case PendingApproval, Running, RollingBack, Done, RolledBack, Failed:
	default:
		return fmt.Errorf("unknown plan state %q", j.State)
	}
	for _, step := range j.Steps {
		switch step.Status {
		case StepPending, StepRunning, StepDone, StepFailed, StepSkipped, StepUndone:
		default:
			return fmt.Errorf("unknown status %q for step %q", step.Status, step.Name)
		}
		if step.Name != string(CAREImages) && step.Name != string(Engine) && step.Name != string(Desktop) {
			return fmt.Errorf("unknown step %q", step.Name)
		}
		if step.Attempts < 0 || step.Attempts > 3 {
			return fmt.Errorf("invalid attempt count for step %q", step.Name)
		}
	}
	return nil
}

// DependencyOrder returns an order in which every intermediate desktop/engine
// pair satisfies the signed manifest requirements. A bridge release is needed
// when neither direct transition is safe.
func DependencyOrder(currentDesktop, currentEngine string, m release.Manifest) ([]UpdateType, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	desktopFirst := compatible(m.Desktop.RequiresEngine, currentEngine) &&
		compatible(m.Engine.RequiresDesktop, m.Desktop.Version)
	engineFirst := compatible(m.Engine.RequiresDesktop, currentDesktop) &&
		compatible(m.Desktop.RequiresEngine, m.Engine.Version)
	desktopChanged, engineChanged := currentDesktop != m.Desktop.Version, currentEngine != m.Engine.Version
	switch {
	case !desktopChanged && !engineChanged:
		return nil, nil
	case desktopChanged && engineChanged && desktopFirst:
		return []UpdateType{Desktop, Engine}, nil
	case desktopChanged && engineChanged && engineFirst:
		return []UpdateType{Engine, Desktop}, nil
	case desktopChanged && !engineChanged && compatible(m.Desktop.RequiresEngine, currentEngine):
		return []UpdateType{Desktop}, nil
	case engineChanged && !desktopChanged && compatible(m.Engine.RequiresDesktop, currentDesktop):
		return []UpdateType{Engine}, nil
	default:
		return nil, errors.New("no compatible desktop/engine update step exists; a bridge release is required")
	}
}

func compatible(constraint, version string) bool {
	c, err := semver.NewConstraint(constraint)
	if err != nil {
		return false
	}
	v, err := semver.StrictNewVersion(version)
	return err == nil && c.Check(v)
}

func Compatible(constraint, version string) bool {
	return compatible(constraint, version)
}
