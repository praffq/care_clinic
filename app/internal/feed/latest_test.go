package feed

import (
	"errors"
	"testing"
	"time"
)

func careAt(t *testing.T, v string, minute int, desktop, engine string) CareRelease {
	t.Helper()
	ver, err := NewVersion(v)
	if err != nil {
		t.Fatal(err)
	}
	r := CareRelease{Version: ver, ReleasedAt: time.Date(2027, 1, 1, 0, minute, 0, 0, time.UTC)}
	if desktop != "" {
		if r.Requires.Desktop, err = NewConstraint(desktop); err != nil {
			t.Fatal(err)
		}
	}
	if engine != "" {
		if r.Requires.Engine, err = NewConstraint(engine); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func TestLatestCompatibleCare(t *testing.T) {
	m := &Manifest{Care: []CareRelease{
		careAt(t, "3.0.0", 0, ">=0.1.0", ">=0.1.0"),
		careAt(t, "3.1.0+be.a", 1, ">=0.1.0", ">=0.1.0"),
		careAt(t, "3.1.0+be.b", 2, ">=0.1.0", ">=0.1.0"),
		careAt(t, "3.2.0", 3, ">=0.3.0", ">=0.1.0"),
		careAt(t, "3.3.0", 4, ">=0.1.0", ">=0.5.0"),
	}}
	cases := []struct {
		name, desktop, engine string
		blocked               []string
		want                  string
	}{
		{"newest that fits app and engine", "0.2.0", "0.4.0", nil, "3.1.0+be.b"},
		{"newer app and engine take the newest", "0.3.0", "0.5.0", nil, "3.3.0"},
		{"engine too old for the newest", "0.3.0", "0.4.0", nil, "3.2.0"},
		{"blocked release skipped", "0.2.0", "0.4.0", []string{"care:3.1.0+be.b"}, "3.1.0+be.a"},
		{"blocking another build keeps this one", "0.2.0", "0.4.0", []string{"care:3.1.0+be.x"}, "3.1.0+be.b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mm := *m
			mm.Blocked = tc.blocked
			r, err := LatestCompatibleCare(&mm, semver(t, tc.desktop), semver(t, tc.engine))
			if err != nil || r.Version.String() != tc.want {
				t.Fatalf("got %v, %v; want %s", r, err, tc.want)
			}
		})
	}
}

func TestLatestCompatibleCareExplainsWhyNoneFits(t *testing.T) {
	cases := []struct {
		name    string
		care    []CareRelease
		blocked []string
		want    string
	}{
		{"app too old", []CareRelease{careAt(t, "3.1.0+be.a", 0, ">=0.3.0", ">=0.1.0")}, nil,
			"CARE 3.1.0 needs app ≥ 0.3.0 (this app is 0.2.0); update the app first."},
		{"engine too old", []CareRelease{careAt(t, "3.1.0", 0, ">=0.1.0", ">=0.9.0")}, nil,
			"CARE 3.1.0 needs engine ≥ 0.9.0 (this computer runs 0.4.0); update the app first."},
		{"blocked newest explains the next", []CareRelease{
			careAt(t, "3.1.0", 0, ">=0.3.0", ""), careAt(t, "3.2.0", 1, ">=0.1.0", ">=0.1.0"),
		}, []string{"care:3.2.0"}, "CARE 3.1.0 needs app ≥ 0.3.0 (this app is 0.2.0); update the app first."},
		{"all blocked", []CareRelease{careAt(t, "3.1.0", 0, "", "")}, []string{"care:3.1.0"},
			"Every CARE release on the update server has been withdrawn. Try again later."},
		{"empty feed", nil, nil, "The update server has no CARE release to install yet. Try again later."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LatestCompatibleCare(&Manifest{Care: tc.care, Blocked: tc.blocked}, semver(t, "0.2.0"), semver(t, "0.4.0"))
			var nc *NoCompatibleError
			if !errors.As(err, &nc) || nc.Reason != tc.want {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}
