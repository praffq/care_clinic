// Package api holds the JSON types of the host service's control API, shared
// by the service and the app's control client. It has no dependency on the
// hypervisor driver so the app does not link it.
package api

import (
	"errors"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
	"github.com/ohcnetwork/care_desktop/app/internal/service/slots"
)

// EngineState is the engine VM state reported by the service.
type EngineState string

const (
	StateNoEngine EngineState = "no_engine" // no slot is active
	StateStopped  EngineState = "stopped"
	StateStarting EngineState = "starting" // VM booting, agent not ready yet
	StateReady    EngineState = "ready"    // agent answers and docker is ready
	StateNotReady EngineState = "not_ready"
	StateStopping EngineState = "stopping"
	StateCrashed  EngineState = "crashed" // stopped on its own; a restart follows when wanted
	StateBackoff  EngineState = "backoff" // waiting to retry after a crash or failed boot
)

// EngineStatus is a snapshot of the engine VM.
type EngineStatus struct {
	State         EngineState      `json:"state"`
	EngineVersion string           `json:"engine_version,omitempty"`
	Agent         *protocol.Status `json:"agent,omitempty"`
	StartedAt     time.Time        `json:"started_at,omitzero"`
	UptimeSeconds int64            `json:"uptime_seconds,omitempty"`
	Restarts      int              `json:"restarts"`
	LastError     string           `json:"last_error,omitempty"`
	LastErrorAt   time.Time        `json:"last_error_at,omitzero"`
	ConsoleLog    string           `json:"console_log,omitempty"`
	NextRetryAt   time.Time        `json:"next_retry_at,omitzero"`
	// Stack is set once the agent is ready.
	Stack *StackStatus `json:"stack,omitempty"`
}

// StackState is the outcome of the service's own clinic stack actions: it
// starts the stack after every boot and stops it before shutting the VM
// down. Per-service state is the agent's stack.status op.
type StackState string

const (
	StackNotInstalled StackState = "not_installed" // no clinic config in the engine
	StackStarting     StackState = "starting"
	StackRunning      StackState = "running" // up and healthy after boot
	StackFailed       StackState = "failed"
	StackStopping     StackState = "stopping"
	StackStopped      StackState = "stopped"
)

// StackStatus is the service's view of the clinic stack.
type StackStatus struct {
	State StackState `json:"state"`
	Error string     `json:"error,omitempty"`
	Since time.Time  `json:"since"`
}

// AgentReachable reports whether the agent answers (ready or not_ready).
func (s EngineStatus) AgentReachable() bool {
	return s.State == StateReady || s.State == StateNotReady
}

// ServiceStatus is GET /v1/service/status.
type ServiceStatus struct {
	// Version is the care-clinic-service build ("dev" when built without
	// a version).
	Version string         `json:"version,omitempty"`
	Engine  EngineStatus   `json:"engine"`
	Slots   slots.Pointer  `json:"slots"`
	Forward *ForwardStatus `json:"forward,omitempty"`
	MDNS    *MDNSStatus    `json:"mdns,omitempty"`
	Backups *BackupStatus  `json:"backups,omitempty"`
}

// BackupStatus is the state of the backup export: the service copies every
// complete set out of the engine into Dir, then deletes it there.
type BackupStatus struct {
	Dir string `json:"dir"`
	// LastExported is the newest set copied out and verified, at VerifiedAt.
	LastExported string    `json:"last_exported,omitempty"`
	VerifiedAt   time.Time `json:"verified_at,omitzero"`
	LastError    string    `json:"last_error,omitempty"`
	LastErrorAt  time.Time `json:"last_error_at,omitzero"`
	// CheckedAt is the last export pass that reached the engine.
	CheckedAt time.Time `json:"checked_at,omitzero"`
	// Pending complete sets are still in the engine, oldest from
	// OldestPendingAt.
	Pending         int       `json:"pending"`
	OldestPendingAt time.Time `json:"oldest_pending_at,omitzero"`
	// Critical is set when MaxPendingBackups sets are waiting: the engine
	// keeps no more, so the next set replaces the oldest one there.
	Critical bool `json:"critical,omitempty"`
	// Message explains a problem, e.g. "Backup not copied out since ...".
	Message string `json:"message,omitempty"`
	// Copy is the extra copy to BackupCopyDir; nil when none is set.
	Copy *BackupCopyStatus `json:"copy,omitempty"`
}

// MaxPendingBackups is how many complete sets the engine keeps while the
// host cannot copy them out (backup.sh KEEP_SETS); at this many the backup
// status is critical.
const MaxPendingBackups = 2

// BackupCopyStatus is the extra copy of the newest host set to a removable
// drive or network share (ServiceConfig.BackupCopyDir).
type BackupCopyStatus struct {
	Dir string `json:"dir"`
	// LastCopied is the newest set copied there and verified, at CopiedAt.
	LastCopied string    `json:"last_copied,omitempty"`
	CopiedAt   time.Time `json:"copied_at,omitzero"`
	// Connected reports whether Dir was there at the last attempt.
	Connected   bool      `json:"connected"`
	LastError   string    `json:"last_error,omitempty"`
	LastErrorAt time.Time `json:"last_error_at,omitzero"`
	// Message explains a problem, e.g. "... isn't connected; last copied ...".
	Message string `json:"message,omitempty"`
}

// HostBackup is an exported set in the host backup directory.
type HostBackup struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	Size      int64     `json:"size"`
	Path      string    `json:"path"`
}

// BackupList is GET /v1/backups: sets on the host and still in the engine.
type BackupList struct {
	Dir    string               `json:"dir"`
	Host   []HostBackup         `json:"host"`
	Engine []protocol.BackupSet `json:"engine"`
	// EngineError is set when the engine's sets could not be listed.
	EngineError string `json:"engine_error,omitempty"`
}

// ForwardStatus describes the host listeners that forward to the engine.
type ForwardStatus struct {
	// ProxyProtocol reports whether each forwarded connection starts with a
	// PROXY protocol v2 header carrying the client address.
	ProxyProtocol bool              `json:"proxy_protocol"`
	Listeners     []ForwardListener `json:"listeners"`
}

// ForwardListener is one host listener. A listener that could not bind
// reports Error and is retried.
type ForwardListener struct {
	Name      string `json:"name"`
	Listen    string `json:"listen"`
	VsockPort uint32 `json:"vsock_port"`
	Listening bool   `json:"listening"`
	Error     string `json:"error,omitempty"`
}

// MDNSStatus describes the service's <name>.local advertisement.
type MDNSStatus struct {
	Enabled     bool          `json:"enabled"`
	Name        string        `json:"name"` // label; the host is Name + ".local"
	Advertising bool          `json:"advertising"`
	Since       time.Time     `json:"since,omitzero"`
	Conflict    *MDNSConflict `json:"conflict,omitempty"`
	LastError   string        `json:"last_error,omitempty"`
}

// MDNSConflict is another device answering for the advertised name.
type MDNSConflict struct {
	Host    string `json:"host"`
	Address string `json:"address"`
}

// ServiceConfig is the persisted service configuration: GET
// /v1/service/config, and the result of POST.
type ServiceConfig struct {
	MDNSName string `json:"mdns_name"`
	// ProxyProtocol prefixes every forwarded connection with a PROXY v2
	// header; the guest's Caddy must expect it.
	ProxyProtocol bool `json:"proxy_protocol"`
	// BackupDir is the host directory backups are exported to.
	BackupDir string `json:"backup_dir"`
	// BackupCopyDir, when set, gets an extra copy of the newest set: a
	// folder on a removable drive or network share. It is never created;
	// while it is missing the copy waits for it.
	BackupCopyDir string `json:"backup_copy_dir,omitempty"`
}

// ServiceConfigUpdate is the body of POST /v1/service/config; omitted
// fields keep their current value.
type ServiceConfigUpdate struct {
	MDNSName      *string `json:"mdns_name,omitempty"`
	ProxyProtocol *bool   `json:"proxy_protocol,omitempty"`
	BackupDir     *string `json:"backup_dir,omitempty"`
	// BackupCopyDir "" turns the extra copy off.
	BackupCopyDir *string `json:"backup_copy_dir,omitempty"`
}

// SlotList is GET /v1/engine/slots.
type SlotList struct {
	slots.Pointer
	Slots []slots.Slot `json:"slots"`
}

// ActivateRequest is POST /v1/engine/activate.
type ActivateRequest struct {
	Version string `json:"version"`
}

// PruneResult is POST /v1/engine/prune.
type PruneResult struct {
	Removed []string `json:"removed"`
}

// Error is the body of every non-2xx control API response.
type Error struct {
	Code    string `json:"error"`
	Message string `json:"message,omitempty"`
}

// Error codes.
const (
	CodeNoEngine          = "no_engine"
	CodeEngineUnavailable = "engine_unavailable"
	CodeNotInstalled      = "not_installed"
	CodeChecksumMismatch  = "checksum_mismatch"
	CodeIsCurrent         = "is_current"
	CodeInvalidBundle     = "invalid_bundle"
	CodeBadRequest        = "bad_request"
	CodeInvalidConfig     = "invalid_config"
	CodeTooLarge          = "too_large"
	CodeNotFound          = "not_found"
	CodeAgentUnreachable  = "agent_unreachable"
	CodeInternal          = "internal"
)

var (
	ErrNoEngine          = errors.New("no engine is installed")
	ErrEngineUnavailable = errors.New("engine is not running")
	ErrInvalidConfig     = errors.New("invalid service configuration")
)

// AgentPrefix is the path prefix of the agent relay: /v1/agent/v1/status
// reaches the agent's /v1/status.
const AgentPrefix = "/v1/agent"
