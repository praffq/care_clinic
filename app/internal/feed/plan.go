package feed

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/versions"
)

// MaxCandidates bounds how many releases per component the resolver
// considers, newest first.
const MaxCandidates = 5

// CareVersion identifies an installed CARE release. ReleasedAt orders it
// against feed entries of equal precedence when its exact version is not in
// the feed.
type CareVersion struct {
	Version    versions.Semver
	ReleasedAt time.Time
}

// Installed describes the components on this machine. A zero Engine means the
// engine is not installed (fresh install); a nil Care means CARE is not
// installed.
type Installed struct {
	Desktop      versions.Semver
	Engine       versions.Semver
	Care         *CareVersion
	DesktopAsset string // Platform*
	EngineArch   string // Arch*
}

// Step installs one release of one component. Exactly one of Desktop, Engine
// and Care is set, matching Component. From is empty when the component is
// not installed yet.
type Step struct {
	Component string
	From      string
	To        string
	Desktop   *DesktopRelease
	Engine    *EngineRelease
	Care      *CareRelease
}

// Plan is an ordered list of steps; after each one every installed
// component's requirements hold. Urgent is set when the plan moves off a
// blocked installed version. Held explains components left below their
// newest installable release.
type Plan struct {
	Steps  []Step
	Urgent bool
	Held   []string
}

// NoPathError reports that newer releases exist but none can be installed.
type NoPathError struct {
	Reason  string
	Details []string
}

func (e *NoPathError) Error() string {
	if len(e.Details) == 0 {
		return e.Reason
	}
	return e.Reason + ": " + strings.Join(e.Details, "; ")
}

// node is one component's state: absent, or a version with requirements
// (nil when the installed version is not in the feed).
type node struct {
	present bool
	version versions.Semver
	label   string
	req     *Requires
	blocked bool

	desktop *DesktopRelease
	engine  *EngineRelease
	care    *CareRelease
}

var planComponents = [3]string{ComponentDesktop, ComponentEngine, ComponentCare}

// ResolvePlan picks target releases and an order to install them in. See
// docs/updates-feed.md ("Update plans") for the rules and tie-breaks.
func ResolvePlan(in Installed, m *Manifest) (*Plan, error) {
	blocked := blockedSet(m.Blocked)
	cur := installedNodes(in, m, blocked)
	fresh := !cur[1].present

	// Options per component: candidates newest first, then "stay" when the
	// component is installed (and, for desktop, always: an install never
	// updates the desktop app).
	var opts [3][]node
	opts[0] = candidatesDesktop(in, m, blocked)
	opts[1] = candidatesEngine(in, m, blocked)
	opts[2] = candidatesCare(in, m, blocked)
	if fresh {
		opts[0] = nil
	}
	newest := [3]*node{}
	for i := range opts {
		if len(opts[i]) > 0 {
			newest[i] = &opts[i][0]
		}
		if cur[i].present || i == 0 {
			opts[i] = append(opts[i], cur[i])
		}
	}

	var best *candidatePlan
	for a := range opts[0] {
		for b := range opts[1] {
			for c := range opts[2] {
				target := [3]node{opts[0][a], opts[1][b], opts[2][c]}
				order, ok := findOrder(cur, target)
				if !ok {
					continue
				}
				cp := &candidatePlan{target: target, order: order, ranks: [3]int{a, b, c}}
				for i := range 3 {
					if cur[i].blocked && target[i].label != cur[i].label {
						cp.unblocked++
					}
					if cp.ranks[i] == 0 && newest[i] != nil {
						cp.newest++
					}
				}
				if best == nil || cp.better(best) {
					best = cp
				}
			}
		}
	}

	var reasons []string
	for i := range 3 {
		if newest[i] != nil && (best == nil || best.target[i].label != newest[i].label) {
			reasons = append(reasons, explain(i, *newest[i], cur, opts, best, fresh))
		}
		if cur[i].blocked && (best == nil || best.target[i].label == cur[i].label) {
			reasons = append(reasons, fmt.Sprintf("installed %s %s is blocked and no compatible replacement exists", planComponents[i], cur[i].label))
		}
	}

	if best == nil || len(best.order) == 0 {
		if len(reasons) == 0 {
			return &Plan{}, nil
		}
		return nil, &NoPathError{Reason: "no compatible update path", Details: reasons}
	}

	p := &Plan{Urgent: best.unblocked > 0, Held: reasons}
	for _, i := range best.order {
		t := best.target[i]
		s := Step{Component: planComponents[i], To: t.label, Desktop: t.desktop, Engine: t.engine, Care: t.care}
		if cur[i].present {
			s.From = cur[i].label
		}
		p.Steps = append(p.Steps, s)
	}
	return p, nil
}

type candidatePlan struct {
	target    [3]node
	order     []int
	ranks     [3]int
	unblocked int
	newest    int
}

// better orders plans by: more blocked installed versions replaced, then more
// components at their newest release, then newer desktop, engine, care (in
// that priority).
func (p *candidatePlan) better(o *candidatePlan) bool {
	if p.unblocked != o.unblocked {
		return p.unblocked > o.unblocked
	}
	if p.newest != o.newest {
		return p.newest > o.newest
	}
	for i := range 3 {
		if p.ranks[i] != o.ranks[i] {
			return p.ranks[i] < o.ranks[i]
		}
	}
	return false
}

// permutations of component indexes, tried in this order.
var permutations = [][3]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}}

// findOrder returns the first permutation of changed components such that
// the state is valid after every step.
func findOrder(cur, target [3]node) ([]int, bool) {
	var changed []int
	for i := range 3 {
		if target[i].label != cur[i].label || target[i].present != cur[i].present {
			changed = append(changed, i)
		}
	}
	if len(changed) == 0 {
		return nil, true
	}
	for _, perm := range permutations {
		state := cur
		var order []int
		ok := true
		for _, i := range perm {
			if !slices.Contains(changed, i) {
				continue
			}
			state[i] = target[i]
			order = append(order, i)
			if !valid(state) {
				ok = false
				break
			}
		}
		if ok {
			return order, true
		}
	}
	return nil, false
}

// valid reports whether every present component's known requirements are met
// by the other present components. CARE cannot run without an engine.
func valid(s [3]node) bool {
	if s[2].present && !s[1].present {
		return false
	}
	for i := range 3 {
		if _, ok := violation(s, i); ok {
			return false
		}
	}
	return true
}

// violation returns the first requirement of s[i] not met by s.
func violation(s [3]node, i int) (string, bool) {
	n := s[i]
	if !n.present || n.req == nil {
		return "", false
	}
	check := func(j int, c *Constraint) (string, bool) {
		if c == nil || !s[j].present || c.Check(s[j].version) {
			return "", false
		}
		return fmt.Sprintf("%s %s requires %s %s, installed would be %s", planComponents[i], n.label, planComponents[j], c, s[j].label), true
	}
	if msg, bad := check(0, n.req.Desktop); bad {
		return msg, true
	}
	return check(1, n.req.Engine)
}

// explain says why the newest release of component i is not in the plan.
func explain(i int, want node, cur [3]node, opts [3][]node, best *candidatePlan, fresh bool) string {
	reqs := [3]*Constraint{want.req.Desktop, want.req.Engine, nil}
	for j, c := range reqs {
		if c == nil || j == i {
			continue
		}
		found := false
		for _, o := range opts[j] {
			if o.present && c.Check(o.version) {
				found = true
				break
			}
		}
		if !found && fresh && j == 0 {
			return fmt.Sprintf("%s %s requires desktop %s, which is not installed (the desktop app is not updated during install)", planComponents[i], want.label, c)
		}
		if !found {
			return fmt.Sprintf("%s %s requires %s %s, which is not published for this machine", planComponents[i], want.label, planComponents[j], c)
		}
	}
	base := cur
	if best != nil {
		base = best.target
	}
	base[i] = want
	for j := range 3 {
		if msg, bad := violation(base, j); bad {
			return msg
		}
	}
	return fmt.Sprintf("%s %s: no update order keeps every component compatible", planComponents[i], want.label)
}

func blockedSet(list []string) map[string][]versions.Semver {
	out := map[string][]versions.Semver{}
	for _, b := range list {
		comp, ver, ok := strings.Cut(b, ":")
		if !ok {
			continue
		}
		if sv, err := versions.ParseSemver(ver); err == nil {
			out[comp] = append(out[comp], sv)
		}
	}
	return out
}

// isBlocked matches desktop and engine by precedence (they must bump their
// version) and CARE by exact version, build metadata included.
func isBlocked(blocked map[string][]versions.Semver, comp string, v versions.Semver) bool {
	for _, b := range blocked[comp] {
		if b.Compare(v) == 0 && (comp != ComponentCare || b.Build == v.Build) {
			return true
		}
	}
	return false
}

func installedNodes(in Installed, m *Manifest, blocked map[string][]versions.Semver) [3]node {
	var cur [3]node
	cur[0] = node{present: true, version: in.Desktop, label: in.Desktop.String(), blocked: isBlocked(blocked, ComponentDesktop, in.Desktop)}
	for i := range m.Desktop {
		if r := &m.Desktop[i]; r.Version.Compare(in.Desktop) == 0 {
			cur[0].req, cur[0].desktop = &r.Requires, r
		}
	}
	if in.Engine.Compare(versions.Semver{}) != 0 {
		cur[1] = node{present: true, version: in.Engine, label: in.Engine.String(), blocked: isBlocked(blocked, ComponentEngine, in.Engine)}
		for i := range m.Engine {
			if r := &m.Engine[i]; r.Version.Compare(in.Engine) == 0 {
				cur[1].req, cur[1].engine = &r.Requires, r
			}
		}
	}
	if in.Care != nil {
		label := in.Care.Version.String()
		cur[2] = node{present: true, version: in.Care.Version, label: label, blocked: isBlocked(blocked, ComponentCare, in.Care.Version)}
		for i := range m.Care {
			if r := &m.Care[i]; r.Version.String() == label {
				cur[2].req, cur[2].care = &r.Requires, r
			}
		}
	}
	return cur
}

func candidatesDesktop(in Installed, m *Manifest, blocked map[string][]versions.Semver) []node {
	var out []node
	for i := range m.Desktop {
		r := &m.Desktop[i]
		if _, ok := r.Assets[in.DesktopAsset]; !ok || isBlocked(blocked, ComponentDesktop, r.Version.Semver) || r.Version.Compare(in.Desktop) <= 0 {
			continue
		}
		out = append(out, node{present: true, version: r.Version.Semver, label: r.Version.String(), req: &r.Requires, desktop: r})
	}
	slices.SortFunc(out, func(a, b node) int { return b.version.Compare(a.version) })
	return out[:min(len(out), MaxCandidates)]
}

func candidatesEngine(in Installed, m *Manifest, blocked map[string][]versions.Semver) []node {
	var out []node
	for i := range m.Engine {
		r := &m.Engine[i]
		if _, ok := r.Assets[in.EngineArch]; !ok || isBlocked(blocked, ComponentEngine, r.Version.Semver) || r.Version.Compare(in.Engine) <= 0 {
			continue
		}
		out = append(out, node{present: true, version: r.Version.Semver, label: r.Version.String(), req: &r.Requires, engine: r})
	}
	slices.SortFunc(out, func(a, b node) int { return b.version.Compare(a.version) })
	return out[:min(len(out), MaxCandidates)]
}

func candidatesCare(in Installed, m *Manifest, blocked map[string][]versions.Semver) []node {
	var installed *CareRelease
	if in.Care != nil {
		installed = &CareRelease{Version: Version{Semver: in.Care.Version}, ReleasedAt: in.Care.ReleasedAt}
		for i := range m.Care {
			if m.Care[i].Version.String() == in.Care.Version.String() {
				installed = &m.Care[i]
			}
		}
	}
	var rs []*CareRelease
	for i := range m.Care {
		r := &m.Care[i]
		if isBlocked(blocked, ComponentCare, r.Version.Semver) || (installed != nil && !r.Newer(*installed)) {
			continue
		}
		rs = append(rs, r)
	}
	slices.SortFunc(rs, func(a, b *CareRelease) int {
		if a.Newer(*b) {
			return -1
		}
		if b.Newer(*a) {
			return 1
		}
		return 0
	})
	var out []node
	for _, r := range rs[:min(len(rs), MaxCandidates)] {
		out = append(out, node{present: true, version: r.Version.Semver, label: r.Version.String(), req: &r.Requires, care: r})
	}
	return out
}
