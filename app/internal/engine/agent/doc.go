// Package agent implements care-engine-agent's HTTP API: the server that runs
// inside the engine VM and turns typed protocol ops into docker and compose
// commands. It is transport-agnostic (the caller supplies the listener) and
// does all of its I/O through an Executor, so the HTTP layer is fully testable
// on the host with FakeExecutor. DockerExecutor is the production executor; it
// builds every argv itself from fixed constants and validated op fields and
// never invokes a shell.
//
// Routes:
//
//	GET    /v1/status                 protocol.Status
//	POST   /v1/ops/{name}             NDJSON stream of protocol.Frame
//	PUT    /v1/files/{area}/{name}    upload (atomic, mode 0600)
//	GET    /v1/files/{area}/{name}    download
//	DELETE /v1/files/{area}/{name}    remove
//	GET    /v1/backups                []protocol.BackupSet
//	GET    /v1/backups/{id}/{file}    stream a file listed in the set's manifest
//	DELETE /v1/backups/{id}           remove a backup set
//	GET    /v1/diagnostics            DiagnosticsReport (redacted)
//	POST   /v1/time                   protocol.TimeSync, sets the VM clock
//	POST   /v1/shutdown               202, then powers the VM off
//
// Op requests: the JSON params are the request body, except for image.load
// whose body is the image tarball; its params are passed URL-encoded in the
// "params" query parameter (POST /v1/ops/image.load?params=%7B...%7D). The
// "params" query parameter is rejected for every other op.
//
// image.load verifies the requested digest against the loaded image's ID,
// which with Docker's containerd image store (Docker 29) is the manifest
// digest, or against a RepoDigest when one exists; tarballs produced locally
// by "docker save" carry no RepoDigests.
//
// Mutating ops other than image.load are detached from the request context:
// if the client disconnects they run to completion (bounded by
// Config.MaxOpDuration) and keep holding the op lock until they finish;
// frames written after the disconnect are dropped. Read-only ops and
// image.load are cancelled on disconnect.
//
// Unknown ops get 404, bad params 400 (the protocol error JSON verbatim). Only
// one mutating op runs at a time; a second one gets 409
// {"error":"busy","op":"<running op>"}. stack.status and stack.logs are
// read-only and bypass the lock. Once accepted, an op responds 200 and streams
// newline-delimited protocol.Frames, flushed one by one, ending with exactly
// one result or error frame. Every log line, progress message and error
// message passes through Redact first.
//
// Backup sets live in Config.BackupsRoot, one directory per set named by its
// backup ID. A set is complete when it holds a manifest.json of the form
//
//	{"created_at":"<RFC 3339>","files":[{"name":"db.dump","size":123,"sha256":"<64 hex>"}]}
//
// in which every listed file exists with the listed size. Only listed files
// can be exported.
package agent
