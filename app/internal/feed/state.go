package feed

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/sys/atomicfile"
)

// State is the client's record of the last verified feed (verified.json). Its
// HighestRevision is passed back as VerifyOptions.MinRevision to block rollback.
type State struct {
	HighestRevision uint64    `json:"highest_revision"`
	LastVerifiedAt  time.Time `json:"last_verified_at"`
	ManifestSHA256  string    `json:"manifest_sha256"`
}

// Record updates s after Verify accepted data with manifest m at now. The
// highest revision never decreases.
func (s *State) Record(data []byte, m *Manifest, now time.Time) {
	if m.Revision > s.HighestRevision {
		s.HighestRevision = m.Revision
	}
	sum := sha256.Sum256(data)
	s.ManifestSHA256 = hex.EncodeToString(sum[:])
	s.LastVerifiedAt = now.UTC()
}

// LoadState reads path. A missing file yields the zero State.
func LoadState(path string) (State, error) {
	var s State
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := decodeStrict(data, &s); err != nil {
		return State{}, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

// SaveState atomically writes s to path.
func SaveState(path string, s State) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(path, append(data, '\n'), 0o644)
}
