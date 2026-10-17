package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sort"
)

// Op names.
const (
	OpStackUp       = "stack.up"
	OpStackStop     = "stack.stop"
	OpStackDown     = "stack.down"
	OpStackStatus   = "stack.status"
	OpStackLogs     = "stack.logs"
	OpRunMigrations = "migrations.run"
	OpImagePull     = "image.pull"
	OpImageLoad     = "image.load"
	OpImageBuild    = "image.build"
	OpImagePrune    = "image.prune"
	OpDBDump        = "db.dump"
	OpDBRestore     = "db.restore"
	OpManage        = "manage"
	OpBackupRun     = "backup.run"
)

// MaxParamsBytes bounds an op's JSON params body.
const MaxParamsBytes = 1 << 20

var registry = map[string]func() Op{
	OpStackUp:       func() Op { return &StackUp{} },
	OpStackStop:     func() Op { return &StackStop{} },
	OpStackDown:     func() Op { return &StackDown{} },
	OpStackStatus:   func() Op { return &StackStatus{} },
	OpStackLogs:     func() Op { return &StackLogs{} },
	OpRunMigrations: func() Op { return &RunMigrations{} },
	OpImagePull:     func() Op { return &ImagePull{} },
	OpImageLoad:     func() Op { return &ImageLoad{} },
	OpImageBuild:    func() Op { return &ImageBuild{} },
	OpImagePrune:    func() Op { return &ImagePrune{} },
	OpDBDump:        func() Op { return &DBDump{} },
	OpDBRestore:     func() Op { return &DBRestore{} },
	OpManage:        func() Op { return &ManageCmd{} },
	OpBackupRun:     func() Op { return &BackupRun{} },
}

// OpNames returns all registered op names, sorted.
func OpNames() []string {
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// DecodeOp decodes and validates the params of op name from r. The body must
// be a single JSON object (an empty body is treated as {}); unknown fields and
// trailing data are rejected. The returned Op is a pointer to the op struct,
// e.g. *StackUp. Errors are *UnknownOpError, *DecodeError or *ValidationError.
func DecodeOp(name string, r io.Reader) (Op, error) {
	newOp, ok := registry[name]
	if !ok {
		return nil, &UnknownOpError{Op: name}
	}
	body, err := io.ReadAll(io.LimitReader(r, MaxParamsBytes+1))
	if err != nil {
		return nil, &DecodeError{Msg: "read body: " + err.Error()}
	}
	if len(body) > MaxParamsBytes {
		return nil, &DecodeError{Msg: "body too large"}
	}
	op := newOp()
	if len(bytes.TrimSpace(body)) == 0 {
		return op, op.Validate()
	}
	if t := bytes.TrimSpace(body); t[0] != '{' {
		return nil, &DecodeError{Msg: "params must be a JSON object"}
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(op); err != nil {
		return nil, &DecodeError{Msg: err.Error()}
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, &DecodeError{Msg: "unexpected data after params object"}
	}
	if err := op.Validate(); err != nil {
		return nil, err
	}
	return op, nil
}
