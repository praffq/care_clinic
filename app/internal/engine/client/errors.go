package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
)

// Sentinel errors; see APIError and Op.
var (
	ErrBusy             = errors.New("agent is running another op")
	ErrUnknownOp        = errors.New("agent does not know the op")
	ErrNotFound         = errors.New("not found")
	ErrTooLarge         = errors.New("request body too large")
	ErrIncompleteStream = errors.New("op stream ended without a result or error frame")
	ErrLineTooLong      = errors.New("op stream frame exceeds the line limit")
	ErrBadFrame         = errors.New("malformed op stream frame")
	ErrChecksumMismatch = errors.New("checksum mismatch")
	ErrMissingChecksum  = errors.New("agent sent no " + protocol.BackupSHA256Header + " checksum header")
	// ErrUnavailable is a 503 from the host service relay: the engine is not
	// running or its agent is not answering.
	ErrUnavailable = errors.New("engine is unavailable")
)

// APIError is a non-success HTTP response from the agent other than a 400
// carrying protocol validation or decode errors.
type APIError struct {
	Status  int
	Code    string // the "error" field, e.g. "busy", "not_found", "io_error"
	Message string
	Op      string // the running op for busy, the requested op for unknown_op
}

func (e *APIError) Error() string {
	s := fmt.Sprintf("agent: HTTP %d", e.Status)
	if e.Code != "" {
		s += " " + e.Code
	}
	if e.Op != "" {
		s += " (op " + e.Op + ")"
	}
	if e.Message != "" {
		s += ": " + e.Message
	}
	return s
}

func (e *APIError) Is(target error) bool {
	switch target {
	case ErrBusy:
		return e.Status == http.StatusConflict
	case ErrUnknownOp:
		return e.Code == protocol.CodeUnknownOp
	case ErrNotFound:
		return e.Status == http.StatusNotFound && e.Code != protocol.CodeUnknownOp
	case ErrTooLarge:
		return e.Status == http.StatusRequestEntityTooLarge
	case ErrUnavailable:
		return e.Status == http.StatusServiceUnavailable
	}
	return false
}

const maxErrorBody = 64 << 10

// responseError converts a non-success response into an error.
func responseError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	var body struct {
		Error   string                `json:"error"`
		Message string                `json:"message"`
		Op      string                `json:"op"`
		Fields  []protocol.FieldError `json:"fields"`
	}
	_ = json.Unmarshal(b, &body)
	if resp.StatusCode == http.StatusBadRequest {
		switch body.Error {
		case protocol.CodeInvalidParams:
			fields := body.Fields
			if len(fields) == 0 {
				fields = []protocol.FieldError{{Field: "request", Msg: body.Message}}
			}
			return &protocol.ValidationError{Errors: fields}
		case protocol.CodeBadRequest:
			return &protocol.DecodeError{Msg: body.Message}
		}
	}
	return &APIError{Status: resp.StatusCode, Code: body.Error, Message: body.Message, Op: body.Op}
}
