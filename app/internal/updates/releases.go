package updates

import (
	"errors"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/feed"
)

// ReleasesPage is the GitHub releases page of the desktop and engine
// releases; their tags are desktop-v<version> and engine-v<version>.
const ReleasesPage = "https://github.com/praffq/care_clinic/releases/tag/"

// ReleaseLink is a page about a release.
type ReleaseLink struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// ReleaseNote describes the release a planned step installs, from the
// verified feed. The feed carries no notes text, so Links point at where
// the release is described: the GitHub release of the desktop and engine,
// the source revisions of CARE.
type ReleaseNote struct {
	Component  string        `json:"component"`
	Version    string        `json:"version"`
	ReleasedAt time.Time     `json:"released_at,omitzero"`
	Migrations bool          `json:"migrations,omitempty"`
	DataSchema int           `json:"data_schema,omitempty"`
	Links      []ReleaseLink `json:"links,omitempty"`
}

// ReleaseNotes returns a note per step found in the verified feed, in step
// order. It is empty, not an error, before the first check.
func (c *Coordinator) ReleaseNotes(steps []PlannedStep) ([]ReleaseNote, error) {
	if len(steps) == 0 {
		return nil, nil
	}
	m, _, err := c.manifest()
	if errors.Is(err, ErrNotVerified) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return releaseNotes(m, steps), nil
}

func releaseNotes(m *feed.Manifest, steps []PlannedStep) []ReleaseNote {
	var out []ReleaseNote
	for _, s := range steps {
		n := ReleaseNote{Component: s.Component, Version: s.To}
		switch s.Component {
		case feed.ComponentDesktop:
			if !hasDesktop(m, s.To) {
				continue
			}
			n.Links = []ReleaseLink{{Label: "CARE Clinic " + s.To, URL: ReleasesPage + "desktop-v" + s.To}}
		case feed.ComponentEngine:
			r := findEngine(m, s.To)
			if r == nil {
				continue
			}
			n.DataSchema = r.DataSchema
			n.Links = []ReleaseLink{{Label: "Engine " + s.To, URL: ReleasesPage + "engine-v" + s.To}}
		case feed.ComponentCare:
			r := findCare(m, s.To)
			if r == nil {
				continue
			}
			n.ReleasedAt, n.Migrations = r.ReleasedAt, r.Migrations
			if src := r.Sources; src != nil {
				n.Links = append(n.Links, sourceLink("CARE backend", src.Backend), sourceLink("CARE frontend", src.Frontend))
			}
		default:
			continue
		}
		out = append(out, n)
	}
	return out
}

func sourceLink(label string, s feed.Source) ReleaseLink {
	ref := s.Rev
	if ref == "" {
		ref = s.Ref
	}
	return ReleaseLink{Label: label + " (" + s.Ref + ")", URL: "https://github.com/" + s.Repo + "/tree/" + ref}
}

func hasDesktop(m *feed.Manifest, v string) bool {
	for _, r := range m.Desktop {
		if r.Version.String() == v {
			return true
		}
	}
	return false
}

func findEngine(m *feed.Manifest, v string) *feed.EngineRelease {
	for i := range m.Engine {
		if m.Engine[i].Version.String() == v {
			return &m.Engine[i]
		}
	}
	return nil
}

func findCare(m *feed.Manifest, v string) *feed.CareRelease {
	for i := range m.Care {
		if m.Care[i].Version.String() == v {
			return &m.Care[i]
		}
	}
	return nil
}
