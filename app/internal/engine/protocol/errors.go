package protocol

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Error codes used in JSON error bodies and OpError.Code.
const (
	CodeInvalidParams = "invalid_params"
	CodeUnknownOp     = "unknown_op"
	CodeBadRequest    = "bad_request"
)

// FieldError describes one invalid field. Field uses the JSON path, e.g.
// "services[1]" or "plugins[0].name".
type FieldError struct {
	Field string `json:"field"`
	Msg   string `json:"msg"`
}

// ValidationError aggregates every FieldError found in a request.
type ValidationError struct {
	Errors []FieldError
}

func (e *ValidationError) Error() string {
	parts := make([]string, len(e.Errors))
	for i, f := range e.Errors {
		parts[i] = f.Field + ": " + f.Msg
	}
	return CodeInvalidParams + ": " + strings.Join(parts, "; ")
}

func (e *ValidationError) MarshalJSON() ([]byte, error) {
	fields := e.Errors
	if fields == nil {
		fields = []FieldError{}
	}
	return json.Marshal(struct {
		Error  string       `json:"error"`
		Fields []FieldError `json:"fields"`
	}{CodeInvalidParams, fields})
}

// UnknownOpError is returned by DecodeOp for an unregistered op name.
type UnknownOpError struct {
	Op string
}

func (e *UnknownOpError) Error() string { return fmt.Sprintf("%s: %q", CodeUnknownOp, e.Op) }

func (e *UnknownOpError) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Error string `json:"error"`
		Op    string `json:"op"`
	}{CodeUnknownOp, e.Op})
}

// DecodeError is returned by DecodeOp when the body is not a single JSON
// object matching the op's params (malformed JSON, unknown fields, wrong
// types, trailing data, oversize body).
type DecodeError struct {
	Msg string
}

func (e *DecodeError) Error() string { return CodeBadRequest + ": " + e.Msg }

func (e *DecodeError) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}{CodeBadRequest, e.Msg})
}

// validator accumulates field errors.
type validator struct {
	errs []FieldError
}

func (v *validator) add(field, format string, args ...any) {
	v.errs = append(v.errs, FieldError{Field: field, Msg: fmt.Sprintf(format, args...)})
}

func (v *validator) check(ok bool, field, format string, args ...any) {
	if !ok {
		v.add(field, format, args...)
	}
}

func (v *validator) err() error {
	if len(v.errs) == 0 {
		return nil
	}
	return &ValidationError{Errors: v.errs}
}

// Error codes carried in OpError.Code by error frames.
const (
	CodeNotImplemented = "not_implemented"
	CodeCommandFailed  = "command_failed"
	// CodeBuildFailed: image.build failed deterministically (a package or
	// repository that does not exist, a resolver conflict, a compile error);
	// retrying with the same inputs cannot help.
	CodeBuildFailed    = "build_failed"
	CodeDigestMismatch = "digest_mismatch"
	CodeOpFailed       = "op_failed"
	CodeCanceled       = "canceled"
	// CodeNotInstalled: the op needs the clinic stack, which has no compose
	// file yet.
	CodeNotInstalled = "not_installed"
)
