// Package progress reports the ordered steps of a long job, with byte counts
// for the steps that move data, so the UI can show what is actually running
// instead of guessing from log lines.
package progress

import (
	"io"
	"sync"
	"time"
)

// State is where a step is.
type State string

const (
	Active  State = "active"
	Done    State = "done"
	Skipped State = "skipped"
)

// Event is one step update. Done/Total are bytes; Total 0 means unknown.
type Event struct {
	Step   string `json:"step"`
	State  State  `json:"state"`
	Detail string `json:"detail,omitempty"`
	Done   int64  `json:"done,omitempty"`
	Total  int64  `json:"total,omitempty"`
}

// Func receives events; a nil Func discards them.
type Func func(Event)

func (f Func) emit(e Event) {
	if f != nil {
		f(e)
	}
}

func (f Func) Start(step, detail string)  { f.emit(Event{Step: step, State: Active, Detail: detail}) }
func (f Func) Finish(step, detail string) { f.emit(Event{Step: step, State: Done, Detail: detail}) }
func (f Func) Skip(step, detail string)   { f.emit(Event{Step: step, State: Skipped, Detail: detail}) }

// Bytes reports transfer progress for an active step.
func (f Func) Bytes(step, detail string, done, total int64) {
	f.emit(Event{Step: step, State: Active, Detail: detail, Done: done, Total: total})
}

// Meter turns a series of byte counts into throttled Bytes events, so a
// fast copy doesn't flood the UI. Reaching the total is always reported.
type Meter struct {
	f     Func
	step  string
	total int64
	every time.Duration

	mu     sync.Mutex
	detail string
	done   int64
	last   time.Time
}

// Meter reports up to total bytes for step.
func (f Func) Meter(step, detail string, total int64) *Meter {
	return &Meter{f: f, step: step, detail: detail, total: total, every: 100 * time.Millisecond}
}

// SetDetail changes the detail shown with the next report.
func (m *Meter) SetDetail(detail string) {
	m.mu.Lock()
	m.detail = detail
	m.mu.Unlock()
}

// Set reports done bytes so far.
func (m *Meter) Set(done int64) {
	m.mu.Lock()
	m.done = done
	now := time.Now()
	report := (m.total > 0 && done >= m.total) || now.Sub(m.last) >= m.every
	if report {
		m.last = now
	}
	detail := m.detail
	m.mu.Unlock()
	if report {
		m.f.Bytes(m.step, detail, done, m.total)
	}
}

// Add reports n more bytes.
func (m *Meter) Add(n int64) {
	m.mu.Lock()
	done := m.done + n
	m.mu.Unlock()
	m.Set(done)
}

// Reader counts what is read through r.
func (m *Meter) Reader(r io.Reader) io.Reader { return &meterReader{r: r, m: m} }

type meterReader struct {
	r io.Reader
	m *Meter
}

func (r *meterReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if n > 0 {
		r.m.Add(int64(n))
	}
	return n, err
}
