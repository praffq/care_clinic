package versions

import (
	"fmt"
	"strconv"
	"strings"
)

// Semver is a parsed semantic version (https://semver.org). Build metadata is
// retained but ignored for precedence.
type Semver struct {
	Major, Minor, Patch uint64
	Pre                 []string
	Build               string
}

// ParseSemver parses a strict MAJOR.MINOR.PATCH[-pre][+build] string without a
// leading "v".
func ParseSemver(s string) (Semver, error) {
	var v Semver
	if s == "" {
		return v, fmt.Errorf("empty version")
	}
	rest := s
	if i := strings.IndexByte(rest, '+'); i >= 0 {
		v.Build = rest[i+1:]
		rest = rest[:i]
		if err := checkIdents(v.Build, false); err != nil {
			return v, fmt.Errorf("invalid semver %q: build metadata: %w", s, err)
		}
	}
	if i := strings.IndexByte(rest, '-'); i >= 0 {
		pre := rest[i+1:]
		rest = rest[:i]
		if err := checkIdents(pre, true); err != nil {
			return v, fmt.Errorf("invalid semver %q: prerelease: %w", s, err)
		}
		v.Pre = strings.Split(pre, ".")
	}
	parts := strings.Split(rest, ".")
	if len(parts) != 3 {
		return v, fmt.Errorf("invalid semver %q: want MAJOR.MINOR.PATCH", s)
	}
	var nums [3]uint64
	for i, p := range parts {
		if !isNumeric(p) || (len(p) > 1 && p[0] == '0') {
			return v, fmt.Errorf("invalid semver %q: bad numeric part %q", s, p)
		}
		n, err := strconv.ParseUint(p, 10, 64)
		if err != nil {
			return v, fmt.Errorf("invalid semver %q: %w", s, err)
		}
		nums[i] = n
	}
	v.Major, v.Minor, v.Patch = nums[0], nums[1], nums[2]
	return v, nil
}

func checkIdents(s string, prerelease bool) error {
	if s == "" {
		return fmt.Errorf("empty")
	}
	for _, id := range strings.Split(s, ".") {
		if id == "" {
			return fmt.Errorf("empty identifier")
		}
		for _, c := range id {
			if (c < '0' || c > '9') && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && c != '-' {
				return fmt.Errorf("invalid character %q", c)
			}
		}
		if prerelease && isNumeric(id) && len(id) > 1 && id[0] == '0' {
			return fmt.Errorf("leading zero in %q", id)
		}
	}
	return nil
}

func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func cmpUint(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// Compare returns -1, 0 or 1 following semver precedence rules.
func (v Semver) Compare(o Semver) int {
	if c := cmpUint(v.Major, o.Major); c != 0 {
		return c
	}
	if c := cmpUint(v.Minor, o.Minor); c != 0 {
		return c
	}
	if c := cmpUint(v.Patch, o.Patch); c != 0 {
		return c
	}
	switch {
	case len(v.Pre) == 0 && len(o.Pre) == 0:
		return 0
	case len(v.Pre) == 0:
		return 1
	case len(o.Pre) == 0:
		return -1
	}
	for i := 0; i < len(v.Pre) && i < len(o.Pre); i++ {
		if c := compareIdent(v.Pre[i], o.Pre[i]); c != 0 {
			return c
		}
	}
	return cmpUint(uint64(len(v.Pre)), uint64(len(o.Pre)))
}

func compareIdent(a, b string) int {
	an, bn := isNumeric(a), isNumeric(b)
	switch {
	case an && bn:
		x, _ := strconv.ParseUint(a, 10, 64)
		y, _ := strconv.ParseUint(b, 10, 64)
		return cmpUint(x, y)
	case an:
		return -1
	case bn:
		return 1
	}
	return strings.Compare(a, b)
}

func (v Semver) String() string {
	s := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if len(v.Pre) > 0 {
		s += "-" + strings.Join(v.Pre, ".")
	}
	if v.Build != "" {
		s += "+" + v.Build
	}
	return s
}

type comparator struct {
	op string
	v  Semver
}

// Constraint is a conjunction of comparators, e.g. ">=1.2.3 <2.0.0".
// Supported operators: =, >, >=, <, <=.
type Constraint struct {
	raw string
	cs  []comparator
}

// ParseConstraint parses a space-separated list of comparators; all must hold.
func ParseConstraint(s string) (Constraint, error) {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return Constraint{}, fmt.Errorf("empty constraint")
	}
	c := Constraint{raw: s}
	for _, f := range fields {
		var op string
		for _, candidate := range []string{">=", "<=", ">", "<", "="} {
			if strings.HasPrefix(f, candidate) {
				op = candidate
				break
			}
		}
		if op == "" {
			return Constraint{}, fmt.Errorf("invalid comparator %q: missing operator (=, >, >=, <, <=)", f)
		}
		v, err := ParseSemver(f[len(op):])
		if err != nil {
			return Constraint{}, fmt.Errorf("invalid comparator %q: %w", f, err)
		}
		c.cs = append(c.cs, comparator{op: op, v: v})
	}
	return c, nil
}

// Check reports whether v satisfies every comparator. An empty Constraint
// matches nothing.
func (c Constraint) Check(v Semver) bool {
	if len(c.cs) == 0 {
		return false
	}
	for _, cmp := range c.cs {
		r := v.Compare(cmp.v)
		var ok bool
		switch cmp.op {
		case "=":
			ok = r == 0
		case ">":
			ok = r > 0
		case ">=":
			ok = r >= 0
		case "<":
			ok = r < 0
		case "<=":
			ok = r <= 0
		}
		if !ok {
			return false
		}
	}
	return true
}

func (c Constraint) String() string { return c.raw }
