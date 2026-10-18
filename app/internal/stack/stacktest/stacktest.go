// Package stacktest provides a fake host service, relaying to a real agent
// server over a fake executor, for stack and backup tests.
package stacktest

import (
	"context"
	"net"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/agent"
	"github.com/ohcnetwork/care_desktop/app/internal/engine/client"
	"github.com/ohcnetwork/care_desktop/app/internal/service/api"
)

// Service is a control client whose relay reaches a real agent server
// backed by Fake.
type Service struct {
	Fake    *agent.FakeExecutor
	Files   string // agent files root
	Backups string // agent backups root

	srv     *httptest.Server
	mu      sync.Mutex
	updates []api.ServiceConfigUpdate
}

// New starts a fake service closed with t.
func New(t testing.TB) *Service {
	t.Helper()
	dir := t.TempDir()
	s := &Service{Fake: &agent.FakeExecutor{}, Files: filepath.Join(dir, "files"), Backups: filepath.Join(dir, "backups")}
	s.srv = httptest.NewServer(agent.NewServer(agent.Config{
		DataRoot: dir, FilesRoot: s.Files, BackupsRoot: s.Backups, Log: agent.NewLogRing(10),
	}, s.Fake))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *Service) Config(context.Context) (api.ServiceConfig, error) {
	return api.ServiceConfig{MDNSName: "care"}, nil
}

func (s *Service) UpdateConfig(_ context.Context, u api.ServiceConfigUpdate) (api.ServiceConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updates = append(s.updates, u)
	return api.ServiceConfig{}, nil
}

// Updates returns the config updates received so far.
func (s *Service) Updates() []api.ServiceConfigUpdate {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]api.ServiceConfigUpdate(nil), s.updates...)
}

func (s *Service) Agent(opts ...client.Option) *client.Client {
	var d net.Dialer
	return client.New(func(ctx context.Context) (net.Conn, error) {
		return d.DialContext(ctx, "tcp", s.srv.Listener.Addr().String())
	}, opts...)
}

// Methods lists the executor calls in order.
func (s *Service) Methods() []string {
	var out []string
	for _, c := range s.Fake.Calls() {
		out = append(out, c.Method)
	}
	return out
}
