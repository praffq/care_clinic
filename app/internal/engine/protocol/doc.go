// Package protocol defines the typed contract between the CARE desktop app and
// care-engine-agent, the agent running inside the engine VM. Callers never send
// raw CLI arguments or shell strings: every operation is a Go struct identified
// by an op name (see OpStackUp and friends), decoded strictly with DecodeOp
// (unknown fields and trailing data are rejected) and validated field by field
// before the agent builds any docker or compose command itself. The compose
// project name, compose file path and working directory are agent constants and
// are deliberately absent from this package. Validation failures are reported
// as a ValidationError, which serialises to {"error":"invalid_params",...} so
// the agent can return it verbatim with HTTP 400. Long-running ops stream
// newline-delimited JSON Frames carrying progress, log lines, a final result or
// an error. This package is pure types and validation; it performs no I/O
// beyond decoding a request body.
package protocol
