package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
)

// Default locations inside the engine VM.
const (
	DefaultDataRoot    = "/var/lib/care"
	DefaultSchemaFile  = "/var/lib/care/.data-schema"
	DefaultFilesRoot   = "/var/lib/care/files"
	DefaultBackupsRoot = "/var/lib/care/backups"

	DefaultMaxFileBytes         = 64 << 20
	DefaultMaxRestoreInputBytes = 64 << 30
	DefaultMaxImageBytes        = 32 << 30
	DefaultMaxOpDuration        = 30 * time.Minute

	pingTimeout = 5 * time.Second
)

// Config configures a Server. Zero values take the defaults above.
type Config struct {
	EngineVersion string
	// DataRoot is the filesystem whose free/total space is reported.
	DataRoot string
	// SchemaFile holds the data schema version as a decimal integer; a
	// missing file reports schema 0.
	SchemaFile string
	// FilesRoot holds one subdirectory per protocol.FileArea.
	FilesRoot   string
	BackupsRoot string

	MaxFileBytes         int64 // upload limit for the config and build areas
	MaxRestoreInputBytes int64 // upload limit for the restore-input area
	MaxImageBytes        int64 // image.load body limit

	// MaxOpDuration bounds mutating ops, which keep running when the client
	// disconnects (except image.load, whose input is the request body).
	MaxOpDuration time.Duration
	// ShutdownDelay is how long /v1/shutdown waits after responding before
	// powering off, so the response reaches the caller.
	ShutdownDelay time.Duration
	// Log receives one line per op and request failure; nil allocates a
	// 500-line ring.
	Log *LogRing
}

func (c *Config) setDefaults() {
	def := func(p *string, v string) {
		if *p == "" {
			*p = v
		}
	}
	def(&c.DataRoot, DefaultDataRoot)
	def(&c.SchemaFile, DefaultSchemaFile)
	def(&c.FilesRoot, DefaultFilesRoot)
	def(&c.BackupsRoot, DefaultBackupsRoot)
	defN := func(p *int64, v int64) {
		if *p <= 0 {
			*p = v
		}
	}
	defN(&c.MaxFileBytes, DefaultMaxFileBytes)
	defN(&c.MaxRestoreInputBytes, DefaultMaxRestoreInputBytes)
	defN(&c.MaxImageBytes, DefaultMaxImageBytes)
	if c.MaxOpDuration <= 0 {
		c.MaxOpDuration = DefaultMaxOpDuration
	}
	if c.Log == nil {
		c.Log = NewLogRing(500)
	}
}

// Server is the agent's http.Handler.
type Server struct {
	cfg  Config
	exec Executor
	mux  *http.ServeMux

	mu   sync.Mutex
	busy string // running mutating op, or "" when idle
}

// NewServer returns a Server executing ops with ex.
func NewServer(cfg Config, ex Executor) *Server {
	cfg.setDefaults()
	s := &Server{cfg: cfg, exec: ex, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /v1/status", s.handleStatus)
	s.mux.HandleFunc("POST /v1/ops/{name}", s.handleOp)
	s.mux.HandleFunc("PUT /v1/files/{area}/{name}", s.handleFilePut)
	s.mux.HandleFunc("GET /v1/files/{area}/{name}", s.handleFileGet)
	s.mux.HandleFunc("DELETE /v1/files/{area}/{name}", s.handleFileDelete)
	s.mux.HandleFunc("GET /v1/backups", s.handleBackupList)
	s.mux.HandleFunc("GET /v1/backups/{id}/{file}", s.handleBackupExport)
	s.mux.HandleFunc("DELETE /v1/backups/{id}", s.handleBackupDelete)
	s.mux.HandleFunc("GET /v1/diagnostics", s.handleDiagnostics)
	s.mux.HandleFunc("POST /v1/time", s.handleTime)
	s.mux.HandleFunc("POST /v1/shutdown", s.handleShutdown)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

func (s *Server) logf(format string, args ...any) {
	_, _ = fmt.Fprintf(s.cfg.Log, time.Now().UTC().Format(time.RFC3339)+" "+format+"\n", args...)
}

// acquire marks op as the running mutating op; it returns the op already
// running when busy.
func (s *Server) acquire(op string) (running string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy != "" {
		return s.busy, false
	}
	s.busy = op
	return "", true
}

func (s *Server) release() {
	s.mu.Lock()
	s.busy = ""
	s.mu.Unlock()
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

type errorBody struct {
	Error   string `json:"error"`
	Message string `json:"message,omitempty"`
	Op      string `json:"op,omitempty"`
}

func writeError(w http.ResponseWriter, code int, kind, msg string) {
	writeJSON(w, code, errorBody{Error: kind, Message: Redact(msg)})
}

func writeBusy(w http.ResponseWriter, running string) {
	writeJSON(w, http.StatusConflict, errorBody{Error: "busy", Op: running})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	st := protocol.Status{EngineVersion: s.cfg.EngineVersion}
	ctx, cancel := context.WithTimeout(r.Context(), pingTimeout)
	st.DockerReady = s.exec.Ping(ctx) == nil
	cancel()
	if b, err := os.ReadFile(s.cfg.SchemaFile); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
			st.DataSchema = n
		} else {
			s.logf("status: invalid data schema file %s", s.cfg.SchemaFile)
		}
	}
	if free, total, err := diskUsage(s.cfg.DataRoot); err == nil {
		st.DiskFreeBytes, st.DiskTotalBytes = free, total
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleOp(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var (
		op   protocol.Op
		err  error
		body io.Reader
	)
	q := r.URL.Query()
	if name == protocol.OpImageLoad {
		op, err = protocol.DecodeOp(name, strings.NewReader(q.Get(protocol.ParamsQuery)))
		body = http.MaxBytesReader(w, r.Body, s.cfg.MaxImageBytes)
		// Frames are streamed while the tarball is still being read.
		_ = http.NewResponseController(w).EnableFullDuplex()
	} else {
		if q.Has(protocol.ParamsQuery) {
			writeJSON(w, http.StatusBadRequest, &protocol.DecodeError{Msg: "the params query parameter is only accepted for " + protocol.OpImageLoad})
			return
		}
		op, err = protocol.DecodeOp(name, r.Body)
	}
	if err != nil {
		var unknown *protocol.UnknownOpError
		if errors.As(err, &unknown) {
			writeJSON(w, http.StatusNotFound, unknown)
			return
		}
		writeJSON(w, http.StatusBadRequest, err)
		return
	}

	var password []byte
	if m, ok := op.(*protocol.ManageCmd); ok && m.PasswordFile != "" {
		password, err = s.readAreaFile(protocol.FileAreaConfig, m.PasswordFile)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, &protocol.ValidationError{Errors: []protocol.FieldError{
				{Field: "password_file", Msg: "not found in area " + string(protocol.FileAreaConfig)},
			}})
			return
		}
	}

	if !readOnlyOps[name] {
		if running, ok := s.acquire(name); !ok {
			writeBusy(w, running)
			return
		}
		defer s.release()
	}

	// A mutating op killed half-way is worse than one that finishes, so it
	// outlives the client, bounded by MaxOpDuration. image.load reads the
	// request body and read-only ops have nothing to finish, so they keep the
	// request context. The op lock is held until dispatch returns.
	ctx := r.Context()
	if !readOnlyOps[name] && name != protocol.OpImageLoad {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), s.cfg.MaxOpDuration)
		defer cancel()
	}

	s.logf("op %s: started", name)
	start := time.Now()
	fw := newFrameWriter(w)
	result, err := dispatch(ctx, s.exec, op, body, password, fw)
	if err != nil {
		oe := toOpError(ctx, err)
		s.logf("op %s: failed after %s: %s", name, time.Since(start).Round(time.Millisecond), oe.Error())
		fw.finish(protocol.Frame{Error: oe})
		return
	}
	raw, err := json.Marshal(result)
	if err != nil {
		fw.finish(protocol.Frame{Error: &protocol.OpError{Code: protocol.CodeOpFailed, Message: "encode result: " + err.Error()}})
		return
	}
	s.logf("op %s: done in %s", name, time.Since(start).Round(time.Millisecond))
	fw.finish(protocol.Frame{Result: raw})
}

// frameWriter streams NDJSON frames, flushing after each one. It implements
// Emitter and redacts everything it writes. After a write or flush fails (the
// client went away) every further write is a no-op.
type frameWriter struct {
	mu   sync.Mutex
	w    http.ResponseWriter
	rc   *http.ResponseController
	enc  *json.Encoder
	done bool
}

func newFrameWriter(w http.ResponseWriter) *frameWriter {
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	_ = rc.Flush()
	return &frameWriter{w: w, rc: rc, enc: json.NewEncoder(w)}
}

func (f *frameWriter) write(fr protocol.Frame, terminal bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.done {
		return
	}
	f.done = terminal
	if err := f.enc.Encode(fr); err != nil {
		f.done = true
		return
	}
	if err := f.rc.Flush(); err != nil {
		f.done = true
	}
}

func (f *frameWriter) Progress(p protocol.Progress) {
	p.Message = Redact(p.Message)
	f.write(protocol.Frame{Progress: &p}, false)
}

func (f *frameWriter) Log(l protocol.LogLine) {
	l.Line = Redact(l.Line)
	f.write(protocol.Frame{Log: &l}, false)
}

func (f *frameWriter) finish(fr protocol.Frame) { f.write(fr, true) }

func (s *Server) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	rep := protocol.DiagnosticsReport{EngineVersion: s.cfg.EngineVersion, Containers: []string{}}
	s.mu.Lock()
	rep.Busy = s.busy
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if lines, err := s.exec.Containers(ctx); err != nil {
		rep.ContainersError = Redact(err.Error())
	} else {
		for _, l := range lines {
			rep.Containers = append(rep.Containers, Redact(l))
		}
	}
	if free, total, err := diskUsage(s.cfg.DataRoot); err != nil {
		rep.DiskError = Redact(err.Error())
	} else {
		rep.DiskFreeBytes, rep.DiskTotalBytes = free, total
	}
	rep.AgentLog = s.cfg.Log.Lines()
	if rep.AgentLog == nil {
		rep.AgentLog = []string{}
	}
	writeJSON(w, http.StatusOK, rep)
}

func (s *Server) handleTime(w http.ResponseWriter, r *http.Request) {
	var ts protocol.TimeSync
	dec := json.NewDecoder(io.LimitReader(r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ts); err != nil {
		writeJSON(w, http.StatusBadRequest, &protocol.DecodeError{Msg: err.Error()})
		return
	}
	if ts.Now.IsZero() {
		writeJSON(w, http.StatusBadRequest, &protocol.ValidationError{Errors: []protocol.FieldError{{Field: "now", Msg: "required"}}})
		return
	}
	if err := s.exec.SetTime(r.Context(), ts.Now); err != nil {
		s.logf("time: %v", err)
		writeError(w, http.StatusInternalServerError, protocol.CodeOpFailed, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleShutdown takes the op lock for good, responds 202 and powers off.
func (s *Server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	if running, ok := s.acquire("shutdown"); !ok {
		writeBusy(w, running)
		return
	}
	s.logf("shutdown requested")
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "shutting_down"})
	_ = http.NewResponseController(w).Flush()
	go func() {
		time.Sleep(s.cfg.ShutdownDelay)
		if err := s.exec.PowerOff(context.Background()); err != nil {
			s.logf("poweroff: %v", err)
			s.release()
		}
	}()
}
