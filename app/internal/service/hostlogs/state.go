package hostlogs

import (
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"os"
	"sync"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/atomicfile"
)

type containerPos struct {
	Since time.Time `json:"since"`
	Seen  time.Time `json:"seen"`
}

type positions struct {
	System     map[string]protocol.FileCursor `json:"system,omitempty"`
	Containers map[string]containerPos        `json:"containers,omitempty"`
}

type state struct {
	path string

	mu    sync.Mutex
	pos   positions
	dirty bool
}

func loadState(path string) (*state, error) {
	s := &state{path: path, pos: positions{System: map[string]protocol.FileCursor{}, Containers: map[string]containerPos{}}}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	var p positions
	if err := json.Unmarshal(b, &p); err != nil {
		return s, err
	}
	if p.System != nil {
		s.pos.System = p.System
	}
	if p.Containers != nil {
		s.pos.Containers = p.Containers
	}
	return s, nil
}

func (s *state) systemCursors() map[string]protocol.FileCursor {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.pos.System)
}

func (s *state) containerSince() map[string]time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]time.Time, len(s.pos.Containers))
	for k, v := range s.pos.Containers {
		if !v.Since.IsZero() && protocol.ValidContainerName(k) && len(out) < protocol.MaxLogContainers {
			out[k] = v.Since
		}
	}
	return out
}

func (s *state) setSystem(source string, c protocol.FileCursor) {
	s.mu.Lock()
	s.pos.System[source] = c
	s.dirty = true
	s.mu.Unlock()
}

func (s *state) setContainer(name string, since, now time.Time) {
	s.mu.Lock()
	p := s.pos.Containers[name]
	if since.After(p.Since) {
		p.Since = since
	}
	p.Seen = now
	s.pos.Containers[name] = p
	s.dirty = true
	s.mu.Unlock()
}

func (s *state) prune(before time.Time) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var gone []string
	for name, p := range s.pos.Containers {
		if p.Seen.Before(before) {
			delete(s.pos.Containers, name)
			gone = append(gone, name)
			s.dirty = true
		}
	}
	return gone
}

func (s *state) save(force bool) error {
	s.mu.Lock()
	if !s.dirty && !force {
		s.mu.Unlock()
		return nil
	}
	b, err := json.MarshalIndent(s.pos, "", "  ")
	s.dirty = false
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if err := atomicfile.Write(s.path, append(b, '\n'), 0o600); err != nil {
		s.mu.Lock()
		s.dirty = true
		s.mu.Unlock()
		return err
	}
	return nil
}
