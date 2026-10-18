package agent

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
)

// Error codes carried in protocol.OpError.Code by error frames.
const (
	CodeNotImplemented = "not_implemented"
	CodeCommandFailed  = "command_failed"
	CodeDigestMismatch = "digest_mismatch"
	CodeOpFailed       = "op_failed"
	CodeCanceled       = "canceled"
	// CodeNotInstalled: the op needs the clinic stack, which has no compose
	// file yet.
	CodeNotInstalled = "not_installed"
)

// ErrNotImplemented is returned by executors for ops they do not support yet.
var ErrNotImplemented = &protocol.OpError{Code: CodeNotImplemented, Message: "operation not implemented yet"}

// Emitter receives progress and log output of a running op. Implementations
// are safe for concurrent use and redact everything they forward.
type Emitter interface {
	Progress(protocol.Progress)
	Log(protocol.LogLine)
}

// Executor performs ops. Errors that are a *protocol.OpError are reported as
// is (after redaction); any other error becomes CodeOpFailed. Methods must not
// call the Emitter after returning.
type Executor interface {
	// Ping reports whether the docker daemon is reachable.
	Ping(ctx context.Context) error

	StackUp(ctx context.Context, op protocol.StackUp, em Emitter) error
	StackStop(ctx context.Context, op protocol.StackStop, em Emitter) error
	StackDown(ctx context.Context, op protocol.StackDown, em Emitter) error
	StackStatus(ctx context.Context) (protocol.StackStatusResult, error)
	StackLogs(ctx context.Context, op protocol.StackLogs, em Emitter) error
	RunMigrations(ctx context.Context, em Emitter) error
	ImagePull(ctx context.Context, op protocol.ImagePull, em Emitter) error
	// ImageLoad reads the image tarball from tar.
	ImageLoad(ctx context.Context, op protocol.ImageLoad, tar io.Reader, em Emitter) error
	ImageBuild(ctx context.Context, op protocol.ImageBuild, em Emitter) (protocol.ImageBuildResult, error)
	ImagePrune(ctx context.Context, op protocol.ImagePrune, em Emitter) error
	DBDump(ctx context.Context, op protocol.DBDump, em Emitter) (protocol.DBDumpResult, error)
	DBRestore(ctx context.Context, op protocol.DBRestore, em Emitter) error
	// Manage runs a management command. password holds the contents of
	// op.PasswordFile (read by the server from the config area) or is nil.
	Manage(ctx context.Context, op protocol.ManageCmd, password []byte, em Emitter) error
	BackupRun(ctx context.Context, em Emitter) error

	// Containers returns one human-readable line per container for diagnostics.
	Containers(ctx context.Context) ([]string, error)
	// SetTime sets the VM clock.
	SetTime(ctx context.Context, now time.Time) error
	// PowerOff shuts the VM down.
	PowerOff(ctx context.Context) error
}

// readOnlyOps bypass the single mutating-op lock.
var readOnlyOps = map[string]bool{
	protocol.OpStackStatus: true,
	protocol.OpStackLogs:   true,
}

// emptyResult is the result payload of ops that return nothing.
type emptyResult struct{}

// dispatch runs op on ex and returns its result payload.
func dispatch(ctx context.Context, ex Executor, op protocol.Op, body io.Reader, password []byte, em Emitter) (any, error) {
	none := func(err error) (any, error) { return emptyResult{}, err }
	switch o := op.(type) {
	case *protocol.StackUp:
		return none(ex.StackUp(ctx, *o, em))
	case *protocol.StackStop:
		return none(ex.StackStop(ctx, *o, em))
	case *protocol.StackDown:
		return none(ex.StackDown(ctx, *o, em))
	case *protocol.StackStatus:
		return ex.StackStatus(ctx)
	case *protocol.StackLogs:
		return none(ex.StackLogs(ctx, *o, em))
	case *protocol.RunMigrations:
		return none(ex.RunMigrations(ctx, em))
	case *protocol.ImagePull:
		return none(ex.ImagePull(ctx, *o, em))
	case *protocol.ImageLoad:
		return none(ex.ImageLoad(ctx, *o, body, em))
	case *protocol.ImageBuild:
		return ex.ImageBuild(ctx, *o, em)
	case *protocol.ImagePrune:
		return none(ex.ImagePrune(ctx, *o, em))
	case *protocol.DBDump:
		return ex.DBDump(ctx, *o, em)
	case *protocol.DBRestore:
		return none(ex.DBRestore(ctx, *o, em))
	case *protocol.ManageCmd:
		return none(ex.Manage(ctx, *o, password, em))
	case *protocol.BackupRun:
		return none(ex.BackupRun(ctx, em))
	}
	return nil, &protocol.OpError{Code: CodeNotImplemented, Message: "no executor for op " + op.OpName()}
}

// toOpError converts an executor error into a redacted terminal error.
func toOpError(ctx context.Context, err error) *protocol.OpError {
	var oe *protocol.OpError
	switch {
	case errors.As(err, &oe):
		c := *oe
		c.Message = Redact(c.Message)
		return &c
	case ctx.Err() != nil:
		return &protocol.OpError{Code: CodeCanceled, Message: Redact(err.Error())}
	}
	return &protocol.OpError{Code: CodeOpFailed, Message: Redact(err.Error())}
}
