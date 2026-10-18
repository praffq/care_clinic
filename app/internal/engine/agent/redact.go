package agent

import (
	"regexp"
	"strings"
	"sync"
)

const redacted = "***"

var (
	urlCredRE  = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://[^:/@\s]*:)[^@/\s]+@`)
	bearerRE   = regexp.MustCompile(`(?i)\b(bearer\s+)[A-Za-z0-9._~+/=-]+`)
	secretKVRE = regexp.MustCompile(`(?i)((?:password|passwd|secret|token|key)[a-z0-9_.-]*["']?\s*[=:]\s*)("[^"]*"|'[^']*'|[^\s,;&"']+)`)
)

// Redact masks credentials in s: values of key=value / key: value pairs whose
// key contains password, secret, token or key; bearer tokens; and passwords
// embedded in URLs such as postgres://user:pass@host/db.
func Redact(s string) string {
	s = urlCredRE.ReplaceAllString(s, "${1}"+redacted+"@")
	s = bearerRE.ReplaceAllString(s, "${1}"+redacted)
	return secretKVRE.ReplaceAllString(s, "${1}"+redacted)
}

// LogRing keeps the most recent lines written to it, for diagnostics. It is
// an io.Writer so it can back a slog handler; it is safe for concurrent use.
type LogRing struct {
	mu      sync.Mutex
	lines   []string
	next    int
	full    bool
	partial strings.Builder
}

// NewLogRing returns a LogRing holding at most n lines.
func NewLogRing(n int) *LogRing {
	if n < 1 {
		n = 1
	}
	return &LogRing{lines: make([]string, n)}
}

func (r *LogRing) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, b := range p {
		if b == '\n' {
			r.add(r.partial.String())
			r.partial.Reset()
			continue
		}
		if r.partial.Len() < maxLineBytes {
			r.partial.WriteByte(b)
		}
	}
	return len(p), nil
}

// add stores one complete line; r.mu must be held.
func (r *LogRing) add(line string) {
	r.lines[r.next] = Redact(line)
	r.next = (r.next + 1) % len(r.lines)
	if r.next == 0 {
		r.full = true
	}
}

// Lines returns the buffered lines, oldest first, already redacted.
func (r *LogRing) Lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	if r.full {
		out = append(out, r.lines[r.next:]...)
	}
	return append(out, r.lines[:r.next]...)
}
