// Package updatestest is a fake engine, service and stack for testing the
// update coordinator: a World implements updates.Service, updates.Agent and
// updates.Stack over in-memory state that behaves like the real thing
// closely enough to catch a wrong order of steps (e.g. a rollback that
// skips the database restore leaves the database ahead of the env and
// fails health).
package updatestest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
	"github.com/ohcnetwork/care_desktop/app/internal/service/api"
	"github.com/ohcnetwork/care_desktop/app/internal/service/slots"
	"github.com/ohcnetwork/care_desktop/app/internal/stack"
)

// EngineBundle is a fake engine tarball: the fake parses version and
// data_schema from it.
func EngineBundle(version string, dataSchema int) []byte {
	return fmt.Appendf(nil, "engine:%s:%d", version, dataSchema)
}

// SHA256 is the hex SHA-256 of b.
func SHA256(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// ErrInjected is returned by injected failures.
var ErrInjected = errors.New("injected failure")

// World is the fake. Its exported fields are its state; lock with Lock /
// Unlock when changing them while an operation runs. The zero value is not
// usable; use New.
type World struct {
	mu sync.Mutex

	// Engine slots and the active one; Booted is false until Activate.
	Slots    map[string]slots.Slot
	Current  string
	Previous string
	Booted   bool
	// BadEngines boot but never report docker ready.
	BadEngines map[string]bool
	// WrongSchema makes an engine report this data_schema instead.
	WrongSchema map[string]int
	DiskFree    uint64
	DBBytes     int64

	// Images present in the engine, the env (role -> digest) and the
	// running services.
	Images  map[protocol.Digest]bool
	Env     map[protocol.ImageRole]protocol.Digest
	Running map[protocol.Service]bool
	// DBSchema is the backend digest the database was last migrated by;
	// Backups maps backup ids to the DBSchema they captured.
	DBSchema protocol.Digest
	Backups  map[string]protocol.Digest
	// BadBackends fail health once running.
	BadBackends map[protocol.Digest]bool
	// Unhealthy makes Health fail regardless.
	Unhealthy bool

	// Calls logs every call by name ("Activate 0.1.1", "op stack.up", ...).
	Calls []string
	fail  map[string]int
	seq   int
}

// New returns an empty world: no engine installed, plenty of disk.
func New() *World {
	return &World{
		Slots:       map[string]slots.Slot{},
		BadEngines:  map[string]bool{},
		WrongSchema: map[string]int{},
		DiskFree:    100 << 30,
		Images:      map[protocol.Digest]bool{},
		Running:     map[protocol.Service]bool{},
		Backups:     map[string]protocol.Digest{},
		BadBackends: map[protocol.Digest]bool{},
	}
}

func (w *World) Lock()   { w.mu.Lock() }
func (w *World) Unlock() { w.mu.Unlock() }

// FailOn makes the next n calls named name fail with ErrInjected. Names
// are "Slots", "InstallEngine", "Activate", "Status", "EnsureImages",
// "WriteEnv", "Health", "DBSize" and "op <op name>" (e.g. "op db.dump").
func (w *World) FailOn(name string, n int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.fail == nil {
		w.fail = map[string]int{}
	}
	w.fail[name] = n
}

// CallLog returns a copy of Calls.
func (w *World) CallLog() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.Calls)
}

// Count is how many logged calls start with prefix.
func (w *World) Count(prefix string) int {
	n := 0
	for _, c := range w.CallLog() {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// call logs name (plus detail) and returns an injected failure if armed.
// w.mu is held.
func (w *World) call(name, detail string) error {
	if detail != "" {
		w.Calls = append(w.Calls, name+" "+detail)
	} else {
		w.Calls = append(w.Calls, name)
	}
	if w.fail[name] > 0 {
		w.fail[name]--
		return fmt.Errorf("%s: %w", name, ErrInjected)
	}
	return nil
}

// AddSlot installs an engine slot directly (test setup).
func (w *World) AddSlot(version string, dataSchema int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.Slots[version] = slots.Slot{Version: version, DataSchema: dataSchema, TarballSHA256: SHA256(EngineBundle(version, dataSchema))}
}

// Service.

// SlotsList is the slot list as the service reports it. w.mu is held.
func (w *World) SlotsList() api.SlotList {
	l := api.SlotList{Pointer: slots.Pointer{Current: w.Current, Previous: w.Previous}}
	for _, v := range slices.Sorted(maps.Keys(w.Slots)) {
		l.Slots = append(l.Slots, w.Slots[v])
	}
	return l
}

// Service is the updates.Service view of the world (its Slots method would
// clash with the Slots field).
func (w *World) Service() *ServiceView { return &ServiceView{w} }

// ServiceView implements updates.Service.
type ServiceView struct{ w *World }

func (s *ServiceView) Slots(ctx context.Context) (api.SlotList, error) {
	w := s.w
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.call("Slots", ""); err != nil {
		return api.SlotList{}, err
	}
	return w.SlotsList(), nil
}

func (s *ServiceView) InstallEngine(ctx context.Context, r io.Reader, size int64, sum string) (slots.Slot, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return slots.Slot{}, err
	}
	w := s.w
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.call("InstallEngine", ""); err != nil {
		return slots.Slot{}, err
	}
	if int64(len(b)) != size || SHA256(b) != sum {
		return slots.Slot{}, errors.New("engine bundle checksum mismatch")
	}
	parts := strings.Split(string(b), ":")
	if len(parts) != 3 || parts[0] != "engine" {
		return slots.Slot{}, errors.New("not an engine bundle")
	}
	schema, err := strconv.Atoi(parts[2])
	if err != nil {
		return slots.Slot{}, err
	}
	sl := slots.Slot{Version: parts[1], DataSchema: schema, TarballSHA256: sum}
	if _, ok := w.Slots[sl.Version]; ok {
		return slots.Slot{}, fmt.Errorf("slot %s already installed", sl.Version)
	}
	if sl.Version == w.Current {
		return slots.Slot{}, errors.New("cannot replace the active slot")
	}
	w.Slots[sl.Version] = sl
	w.Calls[len(w.Calls)-1] += " " + sl.Version
	return sl, nil
}

func (s *ServiceView) Activate(ctx context.Context, version string) (api.EngineStatus, error) {
	w := s.w
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.call("Activate", version); err != nil {
		return api.EngineStatus{}, err
	}
	if _, ok := w.Slots[version]; !ok {
		return api.EngineStatus{}, fmt.Errorf("slot %s not installed", version)
	}
	if version != w.Current {
		w.Previous, w.Current = w.Current, version
	}
	// A reboot stops every container; the env and data survive.
	w.Booted = true
	clear(w.Running)
	return api.EngineStatus{State: api.StateReady, EngineVersion: version}, nil
}

// Agent.

// Status implements updates.Agent.
func (w *World) Status(ctx context.Context) (protocol.Status, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.call("Status", ""); err != nil {
		return protocol.Status{}, err
	}
	if !w.Booted || w.Current == "" {
		return protocol.Status{}, errors.New("engine not running")
	}
	schema := w.Slots[w.Current].DataSchema
	if s, ok := w.WrongSchema[w.Current]; ok {
		schema = s
	}
	return protocol.Status{
		EngineVersion: w.Current, DockerReady: !w.BadEngines[w.Current], DataSchema: schema,
		DiskFreeBytes: w.DiskFree, DiskTotalBytes: w.DiskFree * 2,
	}, nil
}

// Backup implements updates.Agent (logged as "op db.dump").
func (w *World) Backup(ctx context.Context) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.call("op db.dump", ""); err != nil {
		return "", err
	}
	if !w.Booted || w.BadEngines[w.Current] {
		return "", errors.New("engine not ready")
	}
	w.seq++
	id := fmt.Sprintf("pre-update-%d", w.seq)
	w.Backups[id] = w.DBSchema
	w.Calls[len(w.Calls)-1] += " " + id
	return id, nil
}

// Restore implements updates.Agent (logged as "op db.restore"). Like the
// engine's db.restore it then migrates with the env currently written.
func (w *World) Restore(ctx context.Context, id string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.call("op db.restore", ""); err != nil {
		return err
	}
	if !w.Booted || w.BadEngines[w.Current] {
		return errors.New("engine not ready")
	}
	dg, ok := w.Backups[id]
	if !ok {
		return fmt.Errorf("backup %s not found", id)
	}
	w.DBSchema = dg
	if be := w.Env[protocol.ImageRoleBackend]; be != "" && w.Images[be] {
		w.DBSchema = be
	}
	w.Calls[len(w.Calls)-1] += " " + id
	return nil
}

// Op implements updates.Agent.
func (w *World) Op(ctx context.Context, op protocol.Op, result any) error {
	if err := op.Validate(); err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.call("op "+op.OpName(), ""); err != nil {
		return err
	}
	if !w.Booted || w.BadEngines[w.Current] {
		return errors.New("engine not ready")
	}
	switch o := op.(type) {
	case protocol.StackStop:
		if len(o.Services) == 0 {
			clear(w.Running)
		}
		for _, s := range o.Services {
			delete(w.Running, s)
		}
	case protocol.StackUp:
		if len(w.Env) == 0 {
			return errors.New("no stack configured")
		}
		for role, dg := range w.Env {
			if !w.Images[dg] {
				return fmt.Errorf("image %s (%s) not present", role, dg)
			}
		}
		for _, s := range []protocol.Service{
			protocol.ServiceDB, protocol.ServiceRedis, protocol.ServiceMinio, protocol.ServiceBackend,
			protocol.ServiceCeleryWorker, protocol.ServiceCeleryBeat, protocol.ServiceFrontend, protocol.ServiceCaddy,
		} {
			w.Running[s] = true
		}
	case protocol.RunMigrations:
		be := w.Env[protocol.ImageRoleBackend]
		if be == "" || !w.Images[be] {
			return errors.New("backend image not present")
		}
		w.DBSchema = be
	default:
		return fmt.Errorf("fake: op %s not supported", op.OpName())
	}
	return nil
}

// Stack.

// EnsureImages implements updates.Stack: tarballs must exist on disk.
func (w *World) EnsureImages(ctx context.Context, images []stack.Image) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.call("EnsureImages", ""); err != nil {
		return err
	}
	if !w.Booted {
		return errors.New("engine not running")
	}
	for _, img := range images {
		if img.Tarball != "" {
			if _, err := os.Stat(img.Tarball); err != nil {
				return err
			}
		}
		w.Images[img.Digest] = true
	}
	return nil
}

// WriteEnv implements updates.Stack.
func (w *World) WriteEnv(ctx context.Context, images []stack.Image) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.call("WriteEnv", ""); err != nil {
		return err
	}
	env := map[protocol.ImageRole]protocol.Digest{}
	for _, img := range images {
		env[img.Role] = img.Digest
	}
	w.Env = env
	w.Calls[len(w.Calls)-1] += " " + short(env[protocol.ImageRoleBackend])
	return nil
}

// Health implements updates.Stack: every app service runs, the backend is
// not a bad one and the database matches the backend.
func (w *World) Health(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.call("Health", ""); err != nil {
		return err
	}
	return w.healthLocked()
}

func (w *World) healthLocked() error {
	if w.Unhealthy {
		return errors.New("unhealthy")
	}
	for _, s := range []protocol.Service{protocol.ServiceDB, protocol.ServiceBackend, protocol.ServiceCeleryWorker, protocol.ServiceCeleryBeat, protocol.ServiceCaddy} {
		if !w.Running[s] {
			return fmt.Errorf("%s not running", s)
		}
	}
	be := w.Env[protocol.ImageRoleBackend]
	if w.BadBackends[be] {
		return errors.New("backend failing its health check")
	}
	if w.DBSchema != be {
		return fmt.Errorf("database schema %s does not match backend %s", short(w.DBSchema), short(be))
	}
	return nil
}

// Healthy reports whether Health would pass, without logging a call.
func (w *World) Healthy() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.healthLocked()
}

// DBSize implements updates.Stack.
func (w *World) DBSize(ctx context.Context) (int64, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.call("DBSize", ""); err != nil {
		return 0, err
	}
	return w.DBBytes, nil
}

func short(d protocol.Digest) string {
	s := strings.TrimPrefix(string(d), "sha256:")
	if len(s) > 8 {
		s = s[:8]
	}
	return s
}
