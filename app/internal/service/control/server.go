// Package control is the host service's control API: JSON over HTTP on a
// Unix socket (see Listen for socket permissions and peer checks), and the
// Client the app uses. The service is the only process that talks to the
// engine VM; the app reaches the agent through the relay under
// api.AgentPrefix.
//
//	GET  /v1/service/status[?watch=1]   api.ServiceStatus (NDJSON stream with watch)
//	POST /v1/vm/{start,stop,restart}    api.EngineStatus
//	GET  /v1/engine/slots               api.SlotList
//	POST /v1/engine/install             api.InstallRequest -> slots.Slot
//	POST /v1/engine/activate            api.ActivateRequest -> api.EngineStatus
//	POST /v1/engine/prune               api.PruneResult
//	*    /v1/agent/v1/...               agent routes (status, ops, files, backups, diagnostics)
package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"path"
	"path/filepath"
	"strings"

	"github.com/ohcnetwork/care_desktop/app/internal/service/api"
	"github.com/ohcnetwork/care_desktop/app/internal/service/slots"
)

// Engine is the supervisor as seen by the control API.
type Engine interface {
	Status() api.EngineStatus
	Subscribe() (<-chan api.EngineStatus, func())
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Restart(ctx context.Context) error
	Switch(ctx context.Context, version string) error
	DialAgent(ctx context.Context) (net.Conn, error)
}

// Slots is the slot store as seen by the control API.
type Slots interface {
	Pointer() (slots.Pointer, error)
	List() ([]slots.Slot, error)
	Install(ctx context.Context, tarball, sha256 string) (slots.Slot, error)
	Prune() ([]string, error)
}

const maxRequestBody = 64 << 10

// Server is the control API's http.Handler.
type Server struct {
	engine Engine
	slots  Slots
	log    *slog.Logger
	mux    *http.ServeMux
	relay  *httputil.ReverseProxy
}

// NewServer returns the control API for engine and store.
func NewServer(engine Engine, store Slots, log *slog.Logger) *Server {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &Server{engine: engine, slots: store, log: log, mux: http.NewServeMux()}
	s.relay = &httputil.ReverseProxy{
		Rewrite: s.rewrite,
		Transport: &http.Transport{
			DialContext:       func(ctx context.Context, _, _ string) (net.Conn, error) { return engine.DialAgent(ctx) },
			DisableKeepAlives: true,
		},
		FlushInterval: -1, // op streams are NDJSON frames; deliver each at once
		ErrorHandler:  s.relayError,
	}
	s.mux.HandleFunc("GET /v1/service/status", s.handleStatus)
	s.mux.HandleFunc("POST /v1/vm/start", s.vmCommand(engine.Start))
	s.mux.HandleFunc("POST /v1/vm/stop", s.vmCommand(engine.Stop))
	s.mux.HandleFunc("POST /v1/vm/restart", s.vmCommand(engine.Restart))
	s.mux.HandleFunc("GET /v1/engine/slots", s.handleSlots)
	s.mux.HandleFunc("POST /v1/engine/install", s.handleInstall)
	s.mux.HandleFunc("POST /v1/engine/activate", s.handleActivate)
	s.mux.HandleFunc("POST /v1/engine/prune", s.handlePrune)
	s.mux.HandleFunc(api.AgentPrefix+"/", s.handleAgent)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, kind, msg string) {
	writeJSON(w, code, api.Error{Code: kind, Message: msg})
}

func (s *Server) serviceStatus() api.ServiceStatus {
	p, err := s.slots.Pointer()
	if err != nil {
		s.log.Warn("read slot pointer", "err", err)
	}
	return api.ServiceStatus{Engine: s.engine.Status(), Slots: p}
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("watch") == "" {
		writeJSON(w, http.StatusOK, s.serviceStatus())
		return
	}
	ch, cancel := s.engine.Subscribe()
	defer cancel()
	w.Header().Set("Content-Type", "application/x-ndjson")
	enc := json.NewEncoder(w)
	rc := http.NewResponseController(w)
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ch:
		}
		if enc.Encode(s.serviceStatus()) != nil || rc.Flush() != nil {
			return
		}
	}
}

func (s *Server) vmCommand(f func(context.Context) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := f(r.Context()); err != nil {
			s.commandError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, s.engine.Status())
	}
}

func (s *Server) commandError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, api.ErrNoEngine):
		writeError(w, http.StatusConflict, api.CodeNoEngine, err.Error())
	case errors.Is(err, slots.ErrNotInstalled):
		writeError(w, http.StatusNotFound, api.CodeNotInstalled, err.Error())
	case errors.Is(err, slots.ErrChecksum):
		writeError(w, http.StatusUnprocessableEntity, api.CodeChecksumMismatch, err.Error())
	case errors.Is(err, slots.ErrIsCurrent):
		writeError(w, http.StatusConflict, api.CodeIsCurrent, err.Error())
	default:
		s.log.Error("control request failed", "err", err)
		writeError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
	}
}

func (s *Server) handleSlots(w http.ResponseWriter, _ *http.Request) {
	p, err := s.slots.Pointer()
	if err != nil {
		s.commandError(w, err)
		return
	}
	list, err := s.slots.List()
	if err != nil {
		s.commandError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, api.SlotList{Pointer: p, Slots: append([]slots.Slot{}, list...)})
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, api.CodeBadRequest, err.Error())
		return false
	}
	return true
}

func (s *Server) handleInstall(w http.ResponseWriter, r *http.Request) {
	var req api.InstallRequest
	if !decode(w, r, &req) {
		return
	}
	if !filepath.IsAbs(req.Path) {
		writeError(w, http.StatusBadRequest, api.CodeBadRequest, "path must be absolute")
		return
	}
	if b, err := hex.DecodeString(req.SHA256); err != nil || len(b) != sha256.Size {
		writeError(w, http.StatusBadRequest, api.CodeBadRequest, "sha256 must be 64 hex digits")
		return
	}
	s.log.Info("installing engine", "path", req.Path, "sha256", req.SHA256)
	sl, err := s.slots.Install(r.Context(), req.Path, req.SHA256)
	switch {
	case err == nil:
		s.log.Info("installed engine", "version", sl.Version)
		writeJSON(w, http.StatusOK, sl)
	case errors.Is(err, slots.ErrChecksum), errors.Is(err, slots.ErrIsCurrent):
		s.commandError(w, err)
	default:
		s.log.Warn("engine install failed", "err", err)
		writeError(w, http.StatusUnprocessableEntity, api.CodeInvalidBundle, err.Error())
	}
}

func (s *Server) handleActivate(w http.ResponseWriter, r *http.Request) {
	var req api.ActivateRequest
	if !decode(w, r, &req) {
		return
	}
	if err := s.engine.Switch(r.Context(), req.Version); err != nil {
		s.commandError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.engine.Status())
}

func (s *Server) handlePrune(w http.ResponseWriter, _ *http.Request) {
	removed, err := s.slots.Prune()
	if err != nil {
		s.commandError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, api.PruneResult{Removed: append([]string{}, removed...)})
}

// relayable reports whether p is an agent route apps may use. Clock and
// shutdown are the service's: a relayed shutdown would look like a crash.
func relayable(method, p string) bool {
	if p != path.Clean(p) {
		return false
	}
	switch {
	case p == "/v1/status", p == "/v1/diagnostics", p == "/v1/backups":
		return method == http.MethodGet
	case strings.HasPrefix(p, "/v1/ops/"):
		return method == http.MethodPost
	case strings.HasPrefix(p, "/v1/files/"), strings.HasPrefix(p, "/v1/backups/"):
		return true
	}
	return false
}

func (s *Server) handleAgent(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, api.AgentPrefix)
	if !relayable(r.Method, p) || !relayable(r.Method, strings.TrimPrefix(r.URL.EscapedPath(), api.AgentPrefix)) {
		writeError(w, http.StatusNotFound, api.CodeNotFound, "not an agent route")
		return
	}
	if !s.engine.Status().AgentReachable() {
		writeError(w, http.StatusServiceUnavailable, api.CodeEngineUnavailable, "the engine is not running")
		return
	}
	s.relay.ServeHTTP(w, r)
}

func (s *Server) rewrite(pr *httputil.ProxyRequest) {
	pr.Out.URL.Scheme = "http"
	pr.Out.URL.Host = "care-engine-agent"
	pr.Out.Host = ""
	pr.Out.URL.Path = strings.TrimPrefix(pr.In.URL.Path, api.AgentPrefix)
	pr.Out.URL.RawPath = strings.TrimPrefix(pr.In.URL.RawPath, api.AgentPrefix)
}

func (s *Server) relayError(w http.ResponseWriter, r *http.Request, err error) {
	if r.Context().Err() != nil {
		return
	}
	if errors.Is(err, api.ErrEngineUnavailable) {
		writeError(w, http.StatusServiceUnavailable, api.CodeEngineUnavailable, err.Error())
		return
	}
	s.log.Warn("agent relay failed", "path", r.URL.Path, "err", err)
	writeError(w, http.StatusBadGateway, api.CodeAgentUnreachable, err.Error())
}
