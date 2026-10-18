// Package advertise keeps the clinic's <name>.local mDNS advertisement up in
// the host service: it advertises once the LAN is up, re-advertises when the
// LAN addresses change or the name stops resolving, and stops (and reports)
// when another device on the network answers for the same name. It is the
// service's copy of the desktop app's watcher (app.go) and uses the same
// internal/sys/mdns responder.
//
// The app and the service must not advertise at the same time: two
// responders on one Mac answer with the same addresses, but whichever stops
// sends a goodbye that makes clients drop the name until the other
// re-announces. Until the app hands advertising to the service, run the
// service with --mdns=false on a Mac whose app advertises the clinic.
package advertise

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/service/api"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/mdns"
)

// Advertiser is a running advertisement (*mdns.Advertiser).
type Advertiser interface {
	Name() string
	IPsChanged() (bool, error)
	CheckAvailable() error
	Resolves() error
	Stop()
}

// Config configures a Service; zero durations take the defaults.
type Config struct {
	Name     string // label, e.g. "care"
	Disabled bool
	Log      *slog.Logger
	// Advertise starts an advertisement; mdns.Advertise when nil.
	Advertise func(name string, logf func(string)) (Advertiser, error)

	Interval time.Duration // between checks (30s)
	Recheck  time.Duration // confirm a conflict, or retry a failed start (5s)
	Settle   time.Duration // after starting, before probing for conflicts (12s)
}

// Service owns the advertisement. Create it with New, then call Run.
type Service struct {
	cfg    Config
	rename chan struct{}

	// Owned by the Run goroutine.
	adv       Advertiser
	since     time.Time
	sightings int
	misses    int
	lastErr   error
	loggedAt  time.Time

	mu      sync.Mutex
	st      api.MDNSStatus
	pending string
}

// New returns a Service that is not advertising yet.
func New(cfg Config) *Service {
	def := func(p *time.Duration, v time.Duration) {
		if *p <= 0 {
			*p = v
		}
	}
	def(&cfg.Interval, 30*time.Second)
	def(&cfg.Recheck, 5*time.Second)
	def(&cfg.Settle, 12*time.Second)
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.Advertise == nil {
		cfg.Advertise = func(name string, logf func(string)) (Advertiser, error) {
			a, err := mdns.Advertise(name, logf)
			if err != nil {
				return nil, err
			}
			return a, nil
		}
	}
	return &Service{cfg: cfg, rename: make(chan struct{}, 1), st: api.MDNSStatus{Enabled: !cfg.Disabled, Name: cfg.Name}}
}

// Status returns the current advertisement state.
func (s *Service) Status() api.MDNSStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.st
	if st.Conflict != nil {
		c := *st.Conflict
		st.Conflict = &c
	}
	return st
}

// SetName switches the advertisement to name (a validated label).
func (s *Service) SetName(name string) {
	s.mu.Lock()
	s.pending = name
	if s.cfg.Disabled {
		s.st.Name = name
	}
	s.mu.Unlock()
	select {
	case s.rename <- struct{}{}:
	default:
	}
}

// Run advertises until ctx ends, then withdraws the name.
func (s *Service) Run(ctx context.Context) {
	if s.cfg.Disabled {
		return
	}
	timer := time.NewTimer(s.start(time.Now()))
	defer timer.Stop()
	defer s.stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.rename:
			s.mu.Lock()
			name := s.pending
			s.mu.Unlock()
			if name == s.cfg.Name {
				continue
			}
			s.cfg.Log.Info("mDNS: changing the advertised name", "from", s.cfg.Name, "to", name)
			s.cfg.Name = name
			s.lastErr = nil
			s.stop()
			s.reset()
			timer.Reset(s.start(time.Now()))
		case now := <-timer.C:
			timer.Reset(s.check(now))
		}
	}
}

// start advertises when not advertising and returns the delay to the next
// check.
func (s *Service) start(now time.Time) time.Duration {
	if s.adv != nil {
		return s.cfg.Interval
	}
	adv, err := s.cfg.Advertise(s.cfg.Name, func(msg string) { s.cfg.Log.Info("mDNS: " + msg) })
	if err != nil {
		s.report(err)
		var conflict *mdns.ConflictError
		if errors.As(err, &conflict) {
			return s.cfg.Interval
		}
		return s.cfg.Recheck // e.g. the LAN is not up yet after boot
	}
	s.adv, s.since, s.lastErr = adv, now, nil
	s.cfg.Log.Info("mDNS: advertising", "host", s.cfg.Name+".local")
	s.publish()
	return s.cfg.Interval
}

func (s *Service) stop() {
	if s.adv != nil {
		s.adv.Stop()
		s.adv = nil
		s.publish()
	}
}

func (s *Service) restart(now time.Time) time.Duration {
	s.stop()
	return s.start(now)
}

func (s *Service) reset() { s.sightings, s.misses = 0, 0 }

// check is one watcher tick; it returns the delay to the next one.
func (s *Service) check(now time.Time) time.Duration {
	if s.adv == nil {
		s.reset()
		return s.start(now)
	}
	if s.lanChanged() {
		s.reset()
		s.cfg.Log.Info("mDNS: the LAN addresses changed - re-advertising")
		s.restart(now)
		return s.cfg.Interval
	}
	if !now.Before(s.since.Add(s.cfg.Settle)) {
		err := s.adv.CheckAvailable()
		var conflict *mdns.ConflictError
		inUse := errors.As(err, &conflict)
		if mdns.Unsettled(err) || (inUse && s.lanChanged()) {
			s.cfg.Log.Info("mDNS: the network changed during the name check - re-advertising", "err", err)
			s.reset()
			s.restart(now)
			return s.cfg.Interval
		}
		if inUse {
			s.sightings++
			if s.sightings < 2 {
				s.cfg.Log.Warn("mDNS: possible name conflict; checking again before reporting it", "err", err)
				return s.cfg.Recheck
			}
			s.stop()
			s.report(err)
			s.reset()
			return s.cfg.Interval
		}
		if err != nil {
			s.report(err)
		} else if s.lastErr != nil {
			s.lastErr = nil
			s.publish()
		}
	}
	s.sightings = 0
	err := s.adv.Resolves()
	if err == nil {
		s.misses = 0
		return s.cfg.Interval
	}
	s.cfg.Log.Warn("mDNS: hostname probe failed", "err", err)
	s.misses++
	if s.misses >= 2 {
		s.reset()
		s.cfg.Log.Warn("mDNS: name stopped resolving - re-advertising", "host", s.cfg.Name+".local")
		s.restart(now)
	}
	return s.cfg.Interval
}

func (s *Service) lanChanged() bool {
	changed, err := s.adv.IPsChanged()
	if err != nil {
		s.cfg.Log.Warn("mDNS: couldn't inspect the LAN", "err", err)
	}
	return changed || err != nil
}

// report records err. While failures continue it logs at most once a minute:
// retries run every few seconds and their messages differ (source ports).
func (s *Service) report(err error) {
	now := time.Now()
	if s.lastErr == nil || (s.lastErr.Error() != err.Error() && now.Sub(s.loggedAt) >= time.Minute) {
		s.cfg.Log.Error("mDNS: "+err.Error(), "host", s.cfg.Name+".local")
		s.loggedAt = now
	}
	s.lastErr = err
	s.publish()
}

func (s *Service) publish() {
	st := api.MDNSStatus{Enabled: true, Name: s.cfg.Name, Advertising: s.adv != nil}
	if s.adv != nil {
		st.Since = s.since
	}
	if s.lastErr != nil {
		st.LastError = s.lastErr.Error()
		var conflict *mdns.ConflictError
		if errors.As(s.lastErr, &conflict) {
			st.Conflict = &api.MDNSConflict{Host: conflict.Host, Address: conflict.Address.String()}
		}
	}
	s.mu.Lock()
	s.st = st
	s.mu.Unlock()
}
