package protocol

import (
	"encoding/json"
	"time"
)

// Status is returned by the agent's status endpoint.
type Status struct {
	EngineVersion  string `json:"engine_version"`
	DockerReady    bool   `json:"docker_ready"`
	DataSchema     int    `json:"data_schema"`
	DiskFreeBytes  uint64 `json:"disk_free_bytes"`
	DiskTotalBytes uint64 `json:"disk_total_bytes"`
}

// ServiceState is the state of one compose service.
type ServiceState struct {
	Name   Service `json:"name"`
	State  string  `json:"state"`            // e.g. running, exited
	Health string  `json:"health,omitempty"` // healthy, unhealthy, starting or empty
	Image  Digest  `json:"image,omitempty"`
}

// StackStatusResult is the result of OpStackStatus.
type StackStatusResult struct {
	Services []ServiceState `json:"services"`
}

// ImageBuildResult is the result of OpImageBuild.
type ImageBuildResult struct {
	Digest Digest `json:"digest"`
}

// DBDumpResult is the result of OpDBDump.
type DBDumpResult struct {
	BackupID string `json:"backup_id"`
}

// BackupFile is one file in a BackupSet.
type BackupFile struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// BackupSet describes a backup available for restore.
type BackupSet struct {
	ID        string       `json:"id"`
	Complete  bool         `json:"complete"`
	CreatedAt time.Time    `json:"created_at"`
	Files     []BackupFile `json:"files"`
}

// TimeSync carries the host clock to the VM.
type TimeSync struct {
	Now time.Time `json:"now"`
}

// Progress reports incremental progress of a long-running op.
type Progress struct {
	Service Service `json:"service,omitempty"`
	State   string  `json:"state,omitempty"`
	Bytes   int64   `json:"bytes,omitempty"`
	Total   int64   `json:"total,omitempty"`
	Message string  `json:"message,omitempty"`
}

// LogLine is one line of service or command output.
type LogLine struct {
	Service Service   `json:"service,omitempty"`
	Time    time.Time `json:"time,omitzero"`
	Stream  string    `json:"stream,omitempty"` // stdout or stderr
	Line    string    `json:"line"`
}

// OpError is a terminal failure of a streamed op.
type OpError struct {
	Code    string       `json:"code"`
	Message string       `json:"message"`
	Fields  []FieldError `json:"fields,omitempty"`
}

func (e *OpError) Error() string { return e.Code + ": " + e.Message }

// Frame is one NDJSON line of a streamed response. Exactly one field is set;
// a stream ends with a Result or Error frame.
type Frame struct {
	Progress *Progress       `json:"progress,omitempty"`
	Log      *LogLine        `json:"log,omitempty"`
	Result   json.RawMessage `json:"result,omitempty"`
	Error    *OpError        `json:"error,omitempty"`
}

// Terminal reports whether the frame ends the stream.
func (f Frame) Terminal() bool { return f.Result != nil || f.Error != nil }
