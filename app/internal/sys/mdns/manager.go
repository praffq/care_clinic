package mdns

import (
	"context"
	"errors"
	"sync"
)

// AdvertisementStatus distinguishes a live responder from a successful LAN probe.
type AdvertisementStatus struct {
	Name     string `json:"name"`
	Running  bool   `json:"running"`
	Verified bool   `json:"verified"`
	Error    string `json:"error,omitempty"`
	Conflict bool   `json:"conflict"`
}

type managedAdvertiser interface {
	Name() string
	Stop()
	IPsChanged() (bool, error)
	Resolves() error
}

// Manager serializes name changes, conflict checks and network-change recovery.
// The window and persistent host helper use the same policy.
type Manager struct {
	mu        sync.Mutex
	adv       managedAdvertiser
	status    AdvertisementStatus
	misses    int
	log       func(string)
	advertise func(context.Context, string, func(string)) (managedAdvertiser, error)
	check     func(context.Context, string) error
}

func NewManager(log func(string)) *Manager {
	return &Manager{
		log: log, check: CheckAvailableContext,
		advertise: func(ctx context.Context, name string, log func(string)) (managedAdvertiser, error) {
			return AdvertiseContext(ctx, name, log)
		},
	}
}

func (m *Manager) Status() AdvertisementStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

func (m *Manager) stop() {
	if m.adv != nil {
		m.adv.Stop()
		m.adv = nil
	}
	m.misses = 0
	m.status.Running, m.status.Verified = false, false
}

func (m *Manager) Stop() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stop()
}

func (m *Manager) failure(err error) {
	if m.log != nil && m.status.Error != err.Error() {
		m.log("mDNS: " + err.Error())
	}
	m.status.Error = err.Error()
	var conflict *ConflictError
	m.status.Conflict = errors.As(err, &conflict)
	m.status.Verified = false
}

// Refresh withdraws a disabled name and retries failed advertisements. Callers
// pass an empty name when their clinic is unavailable, not merely Docker-ready.
func (m *Manager) Refresh(name string) AdvertisementStatus {
	return m.RefreshContext(context.Background(), name)
}

func (m *Manager) RefreshContext(ctx context.Context, name string) AdvertisementStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	name = Label(name)
	if name != m.status.Name || name == "" {
		m.stop()
		m.status = AdvertisementStatus{Name: name}
	}
	if name == "" {
		return m.status
	}
	if ctx.Err() != nil {
		m.stop()
		return m.status
	}
	if m.adv != nil {
		changed, err := m.adv.IPsChanged()
		if changed || err != nil {
			m.stop()
			if err != nil {
				m.failure(err)
			}
		}
	}
	if m.adv == nil {
		adv, err := m.advertise(ctx, name, m.log)
		if err != nil {
			if ctx.Err() != nil {
				m.stop()
				return m.status
			}
			m.failure(err)
			return m.status
		}
		m.adv = adv
		m.status.Running = true
	}
	if err := m.check(ctx, name); err != nil {
		if ctx.Err() != nil {
			m.stop()
			return m.status
		}
		m.failure(err)
		if m.status.Conflict {
			m.stop()
		}
		return m.status
	}
	probe := m.adv.Resolves
	if adv, ok := m.adv.(interface{ ResolvesContext(context.Context) error }); ok {
		probe = func() error { return adv.ResolvesContext(ctx) }
	}
	if err := probe(); err != nil {
		if ctx.Err() != nil {
			m.stop()
			return m.status
		}
		m.failure(err)
		m.misses++
		if m.misses >= 2 {
			m.stop()
		}
		return m.status
	}
	m.misses = 0
	if ctx.Err() != nil {
		m.stop()
		return m.status
	}
	m.status.Error, m.status.Conflict, m.status.Verified = "", false, true
	return m.status
}
