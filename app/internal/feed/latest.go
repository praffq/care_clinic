package feed

import (
	"fmt"
	"strings"

	"github.com/ohcnetwork/care_desktop/app/internal/versions"
)

type NoCompatibleError struct {
	Reason string
}

func (e *NoCompatibleError) Error() string { return e.Reason }

func LatestCompatibleCare(m *Manifest, desktop, engine versions.Semver) (*CareRelease, error) {
	blocked := blockedSet(m.Blocked)
	var newest, best *CareRelease
	for i := range m.Care {
		r := &m.Care[i]
		if isBlocked(blocked, ComponentCare, r.Version.Semver) {
			continue
		}
		if newest == nil || r.Newer(*newest) {
			newest = r
		}
		if careFits(r, desktop, engine) && (best == nil || r.Newer(*best)) {
			best = r
		}
	}
	switch {
	case best != nil:
		return best, nil
	case len(m.Care) == 0:
		return nil, &NoCompatibleError{Reason: "The update server has no CARE release to install yet. Try again later."}
	case newest == nil:
		return nil, &NoCompatibleError{Reason: "Every CARE release on the update server has been withdrawn. Try again later."}
	}
	v := coreVersion(newest.Version.Semver)
	if c := newest.Requires.Desktop; c != nil && !c.Check(desktop) {
		return nil, &NoCompatibleError{Reason: fmt.Sprintf("CARE %s needs app %s (this app is %s); update the app first.", v, readable(c), desktop)}
	}
	return nil, &NoCompatibleError{Reason: fmt.Sprintf("CARE %s needs engine %s (this computer runs %s); update the app first.", v, readable(newest.Requires.Engine), engine)}
}

func careFits(r *CareRelease, desktop, engine versions.Semver) bool {
	if c := r.Requires.Desktop; c != nil && !c.Check(desktop) {
		return false
	}
	if c := r.Requires.Engine; c != nil && !c.Check(engine) {
		return false
	}
	return true
}

func coreVersion(v versions.Semver) string {
	s, _, _ := strings.Cut(v.String(), "+")
	return s
}

var readableOps = strings.NewReplacer(">=", "≥ ", "<=", "≤ ", ">", "> ", "<", "< ", "=", "")

func readable(c *Constraint) string {
	if c == nil {
		return "a newer version"
	}
	return strings.Join(strings.Fields(readableOps.Replace(c.String())), " ")
}
