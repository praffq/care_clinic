package feed

import (
	"errors"
	"strings"
	"testing"
)

func TestVerifyBuildersAndSources(t *testing.T) {
	key := newKey(t)
	rev := strings.Repeat("1", 40)
	digest := "sha256:" + strings.Repeat("e", 64)
	builder := func(name string) string {
		return `{ "ref": "ghcr.io/praffq/care_clinic/` + name + `@` + digest + `", "digest": "` + digest + `" }`
	}
	builders := `"builders": { "backend-builder": ` + builder("care-backend-builder") +
		`, "frontend-builder": ` + builder("care-frontend-builder") + ` },`
	sources := `"sources": {
	  "backend": { "repo": "ohcnetwork/care", "ref": "develop", "rev": "` + rev + `" },
	  "frontend": { "repo": "ohcnetwork/care_fe", "ref": "develop", "rev": "` + rev + `" },
	  "plugins": { "care_filly": { "repo": "ohcnetwork/care_filly", "ref": "main", "rev": "` + rev + `", "kind": "backend" } }
	},`
	anchor := `"build_env": { "REACT_CARE_API_URL": "https://care.local" }`

	tests := []struct {
		name, old, new, contains string
	}{
		{name: "valid"},
		{name: "unknown builder role", old: `"frontend-builder": `, new: `"caddy": `, contains: "care[0].builders.caddy: unexpected image role"},
		{name: "missing builder", old: `, "frontend-builder": ` + builder("care-frontend-builder"), new: "", contains: "care[0].builders.frontend-builder: required"},
		{name: "short rev", old: `"main", "rev": "` + rev, new: `"main", "rev": "1234567`, contains: "care[0].sources.plugins.care_filly.rev: must be a full 40-hex commit SHA"},
		{name: "bad repo", old: `"ohcnetwork/care_fe"`, new: `"care_fe"`, contains: "care[0].sources.frontend.repo: must be owner/name"},
		{name: "empty ref", old: `"repo": "ohcnetwork/care", "ref": "develop"`, new: `"repo": "ohcnetwork/care", "ref": ""`, contains: "care[0].sources.backend.ref: required"},
		{name: "bad kind", old: `"kind": "backend"`, new: `"kind": "both"`, contains: `care[0].sources.plugins.care_filly.kind: must be "backend" or "frontend"`},
		{name: "bad plugin id", old: `"care_filly": {`, new: `"Care-Filly": {`, contains: "care[0].sources.plugins.Care-Filly: id must be"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := replace(t, fixture(t), anchor, builders+sources+anchor)
			if tt.old != "" {
				data = replace(t, data, tt.old, tt.new)
			}
			m, err := Verify(data, Sign(data, key).Marshal(), VerifyOptions{Keys: NewTrustedKeys(pub(key)), Now: testNow})
			if tt.contains == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				c := m.Care[0]
				if c.Sources.Plugins["care_filly"].Rev != rev || string(c.Builders["backend-builder"].Digest) != digest {
					t.Fatalf("decode: %+v %+v", c.Sources, c.Builders)
				}
				return
			}
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tt.contains) {
				t.Fatalf("error %v does not contain %q", err, tt.contains)
			}
		})
	}
}
