package feed

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/versions"
)

var planEpoch = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

func mustVersion(t *testing.T, s string) Version {
	t.Helper()
	v, err := NewVersion(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func mustConstraint(t *testing.T, s string) *Constraint {
	t.Helper()
	if s == "" {
		return nil
	}
	c, err := NewConstraint(s)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func semver(t *testing.T, s string) versions.Semver { return mustVersion(t, s).Semver }

var anyAsset = Asset{URL: "https://example.com/a", SHA256: strings.Repeat("0", 64), Size: 1}

// desktop builds a desktop release with a darwin-arm64 asset.
func desktop(t *testing.T, v, engine string) DesktopRelease {
	return DesktopRelease{Version: mustVersion(t, v), Requires: Requires{Engine: mustConstraint(t, engine)},
		Assets: map[string]Asset{PlatformDarwinARM64: anyAsset}}
}

// engine builds an engine release with an arm64 asset.
func engine(t *testing.T, v, desktop string) EngineRelease {
	return EngineRelease{Version: mustVersion(t, v), Requires: Requires{Desktop: mustConstraint(t, desktop)}, DataSchema: 1,
		Assets: map[string]Asset{ArchARM64: anyAsset}}
}

// care builds a CARE release released dayOffset days after planEpoch.
func care(t *testing.T, v string, day int, desktop, engine string) CareRelease {
	return CareRelease{Version: mustVersion(t, v), ReleasedAt: planEpoch.AddDate(0, 0, day),
		Requires: Requires{Desktop: mustConstraint(t, desktop), Engine: mustConstraint(t, engine)}}
}

type planStep struct{ comp, from, to string }

func TestResolvePlan(t *testing.T) {
	const (
		care111  = "3.0.0+be.111.fe.111"
		care4f2a = "3.1.0+be.4f2a1c.fe.9b33d0"
	)
	// The docs/updates-feed.md example.
	example := func() *Manifest {
		return &Manifest{
			Desktop: []DesktopRelease{
				desktop(t, "0.1.0", ">=0.1.0 <0.2.0"),
				desktop(t, "0.2.0", ">=0.1.0 <0.2.0"),
			},
			Engine: []EngineRelease{
				engine(t, "0.1.0", ">=0.1.0"),
				engine(t, "0.1.1", ">=0.2.0"),
			},
			Care: []CareRelease{
				care(t, care111, 0, ">=0.1.0", ">=0.1.0"),
				care(t, care4f2a, 10, ">=0.2.0", ">=0.1.1"),
			},
		}
	}
	installed := func(d, e, c string) Installed {
		in := Installed{Desktop: semver(t, d), DesktopAsset: PlatformDarwinARM64, EngineArch: ArchARM64}
		if e != "" {
			in.Engine = semver(t, e)
		}
		if c != "" {
			in.Care = &CareVersion{Version: semver(t, c)}
		}
		return in
	}

	tests := []struct {
		name     string
		in       Installed
		manifest func() *Manifest
		steps    []planStep
		urgent   bool
		held     []string // substrings, one per Held entry
		noPath   []string // substrings of NoPathError details; nil means no error
	}{
		{
			name:     "docs example",
			in:       installed("0.1.0", "0.1.0", care111),
			manifest: example,
			steps:    []planStep{{"desktop", "0.1.0", "0.2.0"}, {"engine", "0.1.0", "0.1.1"}, {"care", care111, care4f2a}},
		},
		{
			name:     "up to date",
			in:       installed("0.2.0", "0.1.1", care4f2a),
			manifest: example,
		},
		{
			name: "engine must go before desktop",
			in:   installed("0.1.0", "0.1.0", care111),
			manifest: func() *Manifest {
				m := example()
				m.Desktop[1] = desktop(t, "0.2.0", ">=0.1.1 <0.2.0")
				m.Engine[1] = engine(t, "0.1.1", ">=0.1.0")
				return m
			},
			steps: []planStep{{"engine", "0.1.0", "0.1.1"}, {"desktop", "0.1.0", "0.2.0"}, {"care", care111, care4f2a}},
		},
		{
			name: "engine needs unpublished desktop, care still updates",
			in:   installed("0.2.0", "0.1.0", care111),
			manifest: func() *Manifest {
				m := example()
				m.Engine = append(m.Engine, engine(t, "0.2.0", ">=0.3.0"))
				m.Care[1] = care(t, care4f2a, 10, ">=0.2.0", ">=0.1.0")
				return m
			},
			steps: []planStep{{"engine", "0.1.0", "0.1.1"}, {"care", care111, care4f2a}},
			held:  []string{"engine 0.2.0 requires desktop >=0.3.0, which is not published"},
		},
		{
			name: "engine needs unpublished desktop, nothing else",
			in:   installed("0.2.0", "0.1.1", care4f2a),
			manifest: func() *Manifest {
				m := example()
				m.Engine = append(m.Engine, engine(t, "0.2.0", ">=0.3.0"))
				return m
			},
			noPath: []string{"engine 0.2.0 requires desktop >=0.3.0, which is not published"},
		},
		{
			name: "incompatible with installed desktop",
			in:   installed("0.2.0", "0.1.1", care4f2a),
			manifest: func() *Manifest {
				m := example()
				m.Engine = append(m.Engine, engine(t, "0.2.0", ">=0.2.0"))
				return m
			},
			noPath: []string{"desktop 0.2.0 requires engine >=0.1.0 <0.2.0, installed would be 0.2.0"},
		},
		{
			name: "blocked latest skipped",
			in:   installed("0.1.0", "0.1.0", care111),
			manifest: func() *Manifest {
				m := example()
				m.Blocked = []string{"care:" + care4f2a}
				return m
			},
			steps: []planStep{{"desktop", "0.1.0", "0.2.0"}, {"engine", "0.1.0", "0.1.1"}},
		},
		{
			name: "blocked installed is urgent",
			in:   installed("0.2.0", "0.1.0", care4f2a),
			manifest: func() *Manifest {
				m := example()
				m.Care[1] = care(t, care4f2a, 10, ">=0.2.0", ">=0.1.0")
				m.Blocked = []string{"engine:0.1.0"}
				return m
			},
			steps:  []planStep{{"engine", "0.1.0", "0.1.1"}},
			urgent: true,
		},
		{
			name: "blocked installed preferred over newest elsewhere",
			in:   installed("0.1.0", "0.1.0", care111),
			manifest: func() *Manifest {
				m := example()
				// engine 0.1.1 needs desktop 0.1.x; desktop 0.2.0 needs engine 0.1.0.
				m.Desktop[1] = desktop(t, "0.2.0", "=0.1.0")
				m.Engine[1] = engine(t, "0.1.1", "<0.2.0")
				m.Care = m.Care[:1]
				m.Blocked = []string{"engine:0.1.0"}
				return m
			},
			steps:  []planStep{{"engine", "0.1.0", "0.1.1"}},
			urgent: true,
			held:   []string{"desktop 0.2.0 requires engine =0.1.0, installed would be 0.1.1"},
		},
		{
			name: "blocked installed without replacement",
			in:   installed("0.2.0", "0.1.1", care4f2a),
			manifest: func() *Manifest {
				m := example()
				m.Blocked = []string{"engine:0.1.1"}
				return m
			},
			noPath: []string{"installed engine 0.1.1 is blocked"},
		},
		{
			name: "no asset for this arch skipped",
			in:   installed("0.2.0", "0.1.0", care111),
			manifest: func() *Manifest {
				m := example()
				m.Engine[1].Assets = map[string]Asset{ArchAMD64: anyAsset}
				m.Desktop = append(m.Desktop, DesktopRelease{Version: mustVersion(t, "0.3.0"),
					Requires: Requires{Engine: mustConstraint(t, ">=0.1.0")}, Assets: map[string]Asset{PlatformWindowsAMD64: anyAsset}})
				m.Care[1] = care(t, care4f2a, 10, ">=0.2.0", ">=0.1.0")
				return m
			},
			steps: []planStep{{"care", care111, care4f2a}},
		},
		{
			name:     "fresh install",
			in:       installed("0.2.0", "", ""),
			manifest: example,
			steps:    []planStep{{"engine", "", "0.1.1"}, {"care", "", care4f2a}},
		},
		{
			name: "fresh install keeps desktop",
			in:   installed("0.1.0", "", ""),
			manifest: func() *Manifest {
				m := example()
				m.Desktop = append(m.Desktop, desktop(t, "0.3.0", ">=0.1.0"))
				return m
			},
			steps: []planStep{{"engine", "", "0.1.0"}, {"care", "", care111}},
			held:  []string{"engine 0.1.1 requires desktop >=0.2.0, which is not installed", "care " + care4f2a + " requires desktop >=0.2.0"},
		},
		{
			name: "fresh install impossible",
			in:   installed("0.1.0", "", ""),
			manifest: func() *Manifest {
				m := example()
				m.Engine = m.Engine[1:]
				return m
			},
			noPath: []string{"engine 0.1.1 requires desktop >=0.2.0, which is not installed", "care " + care4f2a + " requires desktop >=0.2.0"},
		},
		{
			name:     "never downgrade",
			in:       installed("0.3.0", "0.2.0", "4.0.0+be.new.fe.new"),
			manifest: example,
		},
		{
			name: "care ordered by released_at at same semver",
			in:   installed("0.2.0", "0.1.1", "3.1.0+be.aaa.fe.aaa"),
			manifest: func() *Manifest {
				m := example()
				m.Care = []CareRelease{
					care(t, "3.1.0+be.ccc.fe.ccc", 5, ">=0.2.0", ">=0.1.1"),
					care(t, "3.1.0+be.aaa.fe.aaa", 1, ">=0.2.0", ">=0.1.1"),
					care(t, "3.1.0+be.zzz.fe.zzz", 3, ">=0.2.0", ">=0.1.1"),
					care(t, "3.0.9+be.old.fe.old", 9, ">=0.2.0", ">=0.1.1"),
				}
				return m
			},
			steps: []planStep{{"care", "3.1.0+be.aaa.fe.aaa", "3.1.0+be.ccc.fe.ccc"}},
		},
		{
			name: "installed care not in feed uses released_at",
			in: func() Installed {
				in := installed("0.2.0", "0.1.1", "")
				in.Care = &CareVersion{Version: semver(t, "3.1.0+be.local.fe.local"), ReleasedAt: planEpoch.AddDate(0, 0, 11)}
				return in
			}(),
			manifest: example,
		},
		{
			name: "installed versions unknown to feed only check new entries",
			in:   installed("0.1.5", "0.1.5", "3.0.5+be.x.fe.x"),
			manifest: func() *Manifest {
				m := example()
				m.Care[1] = care(t, care4f2a, 10, ">=0.1.0", ">=0.1.0")
				return m
			},
			steps: []planStep{{"desktop", "0.1.5", "0.2.0"}, {"care", "3.0.5+be.x.fe.x", care4f2a}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := ResolvePlan(tt.in, tt.manifest())
			if tt.noPath != nil {
				var npe *NoPathError
				if !errors.As(err, &npe) {
					t.Fatalf("want NoPathError, got plan %+v err %v", p, err)
				}
				checkContains(t, "details", npe.Details, tt.noPath)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var got []planStep
			for _, s := range p.Steps {
				got = append(got, planStep{s.Component, s.From, s.To})
				if (s.Desktop != nil) != (s.Component == ComponentDesktop) || (s.Engine != nil) != (s.Component == ComponentEngine) || (s.Care != nil) != (s.Component == ComponentCare) {
					t.Errorf("step %+v: entry pointer does not match component", s)
				}
			}
			if !equalSteps(got, tt.steps) {
				t.Errorf("steps = %v, want %v", got, tt.steps)
			}
			if p.Urgent != tt.urgent {
				t.Errorf("urgent = %v, want %v", p.Urgent, tt.urgent)
			}
			checkContains(t, "held", p.Held, tt.held)
		})
	}
}

func equalSteps(a, b []planStep) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func checkContains(t *testing.T, what string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %q, want %d entries matching %q", what, got, len(want), want)
	}
	for i := range want {
		if !strings.Contains(got[i], want[i]) {
			t.Errorf("%s[%d] = %q, want it to contain %q", what, i, got[i], want[i])
		}
	}
}

func TestResolvePlanCandidateBound(t *testing.T) {
	m := &Manifest{Desktop: []DesktopRelease{desktop(t, "0.1.0", ">=0.1.0")}, Engine: []EngineRelease{engine(t, "0.1.0", ">=0.1.0")}}
	// Ten newer engines; only the oldest is compatible, and it is beyond the bound.
	m.Engine = append(m.Engine, engine(t, "0.1.1", ">=0.1.0"))
	for i := 2; i <= 10; i++ {
		m.Engine = append(m.Engine, engine(t, "0.1."+strconv.Itoa(i), ">=9.0.0"))
	}
	in := Installed{Desktop: semver(t, "0.1.0"), Engine: semver(t, "0.1.0"), DesktopAsset: PlatformDarwinARM64, EngineArch: ArchARM64}
	_, err := ResolvePlan(in, m)
	var npe *NoPathError
	if !errors.As(err, &npe) {
		t.Fatalf("want NoPathError, got %v", err)
	}
}
