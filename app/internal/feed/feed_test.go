package feed

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
)

var testNow = time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)

func newKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

func pub(k ed25519.PrivateKey) ed25519.PublicKey { return k.Public().(ed25519.PublicKey) }

func fixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "updates.json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func replace(t *testing.T, data []byte, old, new string) []byte {
	t.Helper()
	if !strings.Contains(string(data), old) {
		t.Fatalf("fixture does not contain %q", old)
	}
	return []byte(strings.Replace(string(data), old, new, 1))
}

func TestVerify(t *testing.T) {
	current, next, stranger := newKey(t), newKey(t), newKey(t)
	keys := NewTrustedKeys(pub(current), pub(next))
	digestA := "sha256:" + strings.Repeat("a", 64)

	type tc struct {
		name     string
		edit     func([]byte) []byte
		signer   ed25519.PrivateKey
		tamper   func(data, sig []byte) ([]byte, []byte)
		opts     func(*VerifyOptions)
		wantErr  error
		contains string
	}
	tests := []tc{
		{name: "valid"},
		{name: "next key accepted", signer: next},
		{name: "wrong key", signer: stranger, wantErr: ErrUnknownKey},
		{
			name: "tampered byte",
			tamper: func(d, s []byte) ([]byte, []byte) {
				d = append([]byte(nil), d...)
				d[len(d)/2] ^= 0x01
				return d, s
			},
			wantErr: ErrBadSignature,
		},
		{
			name: "key id swapped to other trusted key",
			tamper: func(d, s []byte) ([]byte, []byte) {
				sig, _ := ParseSignature(s)
				sig.KeyID = KeyID(pub(next))
				return d, sig.Marshal()
			},
			wantErr: ErrBadSignature,
		},
		{
			name:    "malformed signature file",
			tamper:  func(d, _ []byte) ([]byte, []byte) { return d, []byte(`{"key_id":"x","sig":"y","extra":1}`) },
			wantErr: ErrBadSignature,
		},
		{name: "older revision", opts: func(o *VerifyOptions) { o.MinRevision = 43 }, wantErr: ErrRollback},
		{name: "same revision", opts: func(o *VerifyOptions) { o.MinRevision = 42 }},
		{name: "expired", opts: func(o *VerifyOptions) { o.Now = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC) }, wantErr: ErrExpired},
		{
			name: "published after expires",
			edit: func(d []byte) []byte {
				return replace(t, d, `"published_at": "2026-10-01T00:00:00Z"`, `"published_at": "2026-12-01T00:00:00Z"`)
			},
			wantErr:  ErrInvalid,
			contains: "published_at: must not be after expires",
		},
		{
			name:     "wrong schema",
			edit:     func(d []byte) []byte { return replace(t, d, `"schema": 1`, `"schema": 2`) },
			wantErr:  ErrInvalid,
			contains: "schema",
		},
		{
			name: "http URL rejected in release mode",
			edit: func(d []byte) []byte {
				return replace(t, d, "https://github.com/praffq", "http://localhost:8787/praffq")
			},
			wantErr:  ErrInvalid,
			contains: "must be https",
		},
		{
			name: "http URL accepted with AllowInsecureURLs",
			edit: func(d []byte) []byte {
				return replace(t, d, "https://github.com/praffq", "http://localhost:8787/praffq")
			},
			opts: func(o *VerifyOptions) { o.AllowInsecureURLs = true },
		},
		{
			name: "dev tar accepted with AllowInsecureURLs",
			edit: func(d []byte) []byte {
				return replace(t, d, `"digest": "`+digestA+`" }`, `"digest": "`+digestA+`", "tar": "http://localhost:8787/images/care-backend.tar" }`)
			},
			opts: func(o *VerifyOptions) { o.AllowInsecureURLs = true },
		},
		{
			name:     "unknown field",
			edit:     func(d []byte) []byte { return replace(t, d, `"migrations": true`, `"migrations": true, "surprise": 1`) },
			wantErr:  ErrInvalid,
			contains: "unknown field",
		},
		{
			name:     "bad digest",
			edit:     func(d []byte) []byte { return replace(t, d, `"digest": "`+digestA, `"digest": "sha256:XYZ`) },
			wantErr:  ErrInvalid,
			contains: "care[0].images.backend.digest",
		},
		{
			name: "ref digest mismatch",
			edit: func(d []byte) []byte {
				return replace(t, d, "care-backend@sha256:"+strings.Repeat("a", 64), "care-backend@sha256:"+strings.Repeat("9", 64))
			},
			wantErr:  ErrInvalid,
			contains: "embedded digest does not match",
		},
		{
			name: "ref without digest",
			edit: func(d []byte) []byte {
				return replace(t, d, "care-backend@sha256:"+strings.Repeat("a", 64), "care-backend")
			},
			wantErr:  ErrInvalid,
			contains: "must embed @sha256 digest",
		},
		{
			name:     "unknown image role",
			edit:     func(d []byte) []byte { return replace(t, d, `"backup": {`, `"bogus": {`) },
			wantErr:  ErrInvalid,
			contains: "care[0].images.backup: required",
		},
		{
			name: "duplicate version",
			edit: func(d []byte) []byte {
				var m map[string]any
				if err := json.Unmarshal(d, &m); err != nil {
					t.Fatal(err)
				}
				e := m["engine"].([]any)
				m["engine"] = append(e, e[0])
				out, _ := json.Marshal(m)
				return out
			},
			wantErr:  ErrInvalid,
			contains: `engine[1].version: duplicate version "0.1.1"`,
		},
		{
			name:     "bad blocked component",
			edit:     func(d []byte) []byte { return replace(t, d, `"engine:0.1.0"`, `"vm:0.1.0"`) },
			wantErr:  ErrInvalid,
			contains: "blocked[0]",
		},
		{
			name:     "bad blocked version",
			edit:     func(d []byte) []byte { return replace(t, d, `"engine:0.1.0"`, `"engine:v0.1"`) },
			wantErr:  ErrInvalid,
			contains: "blocked[0]",
		},
		{
			name:     "bad semver",
			edit:     func(d []byte) []byte { return replace(t, d, `"version": "0.2.0"`, `"version": "0.2"`) },
			wantErr:  ErrInvalid,
			contains: "desktop[0].version",
		},
		{
			name: "self requirement",
			edit: func(d []byte) []byte {
				return replace(t, d, `"requires": { "desktop": ">=0.2.0" }`, `"requires": { "desktop": ">=0.2.0", "engine": ">=0.1.0" }`)
			},
			wantErr:  ErrInvalid,
			contains: "engine[0].requires.engine: a component cannot require itself",
		},
		{
			name: "missing requirement",
			edit: func(d []byte) []byte {
				return replace(t, d, `"requires": { "engine": ">=0.1.0 <0.2.0" }`, `"requires": {}`)
			},
			wantErr:  ErrInvalid,
			contains: "desktop[0].requires.engine: required",
		},
		{
			name:     "bad constraint",
			edit:     func(d []byte) []byte { return replace(t, d, `">=0.1.0 <0.2.0"`, `"~0.1"`) },
			wantErr:  ErrInvalid,
			contains: "desktop[0].requires.engine",
		},
		{
			name:     "unknown asset platform",
			edit:     func(d []byte) []byte { return replace(t, d, `"windows-amd64": {`, `"linux-amd64": {`) },
			wantErr:  ErrInvalid,
			contains: "desktop[0].assets.linux-amd64: unknown key",
		},
		{
			name:     "bad build env key",
			edit:     func(d []byte) []byte { return replace(t, d, `"REACT_CARE_API_URL"`, `"PATH"`) },
			wantErr:  ErrInvalid,
			contains: "care[0].build_env.PATH",
		},
		{
			name:     "trailing data",
			edit:     func(d []byte) []byte { return append(d, []byte(`{}`)...) },
			wantErr:  ErrInvalid,
			contains: "trailing data",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := fixture(t)
			if tt.edit != nil {
				data = tt.edit(data)
			}
			signer := tt.signer
			if signer == nil {
				signer = current
			}
			sig := Sign(data, signer).Marshal()
			if tt.tamper != nil {
				data, sig = tt.tamper(data, sig)
			}
			opts := VerifyOptions{Keys: keys, Now: testNow}
			if tt.opts != nil {
				tt.opts(&opts)
			}
			m, err := Verify(data, sig, opts)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if m == nil || m.Revision != 42 {
					t.Fatalf("bad manifest: %+v", m)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("got %v, want %v", err, tt.wantErr)
			}
			if tt.contains != "" && !strings.Contains(err.Error(), tt.contains) {
				t.Fatalf("error %q does not contain %q", err, tt.contains)
			}
		})
	}
}

func TestVerifyParsesValues(t *testing.T) {
	k := newKey(t)
	data := fixture(t)
	m, err := Verify(data, Sign(data, k).Marshal(), VerifyOptions{Keys: NewTrustedKeys(pub(k)), Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Care[0].Version.String(); got != "3.1.0+be.4f2a1c.fe.9b33d0" {
		t.Errorf("care version = %q", got)
	}
	if m.Care[0].Version.Major != 3 || m.Care[0].Version.Build != "be.4f2a1c.fe.9b33d0" {
		t.Errorf("care version not parsed: %+v", m.Care[0].Version.Semver)
	}
	engine, _ := NewVersion("0.1.5")
	if !m.Desktop[0].Requires.Engine.Check(engine.Semver) {
		t.Error("desktop requires.engine should accept 0.1.5")
	}
	if m.Desktop[0].Requires.Desktop != nil {
		t.Error("desktop requires.desktop should be nil")
	}
	if m.SupportImages[protocol.ImageRoleRedis].Digest != protocol.Digest("sha256:"+strings.Repeat("f", 64)) {
		t.Error("support image digest not decoded")
	}
}

func TestVerifyNoKeys(t *testing.T) {
	k := newKey(t)
	data := fixture(t)
	if _, err := Verify(data, Sign(data, k).Marshal(), VerifyOptions{Now: testNow}); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("got %v", err)
	}
}

func TestKeyID(t *testing.T) {
	k := newKey(t)
	id := KeyID(pub(k))
	if len(id) != 16 {
		t.Fatalf("key id %q: want 16 hex chars", id)
	}
	if Sign([]byte("x"), k).KeyID != id {
		t.Fatal("Sign key id mismatch")
	}
}

func TestParsePublicKey(t *testing.T) {
	if _, err := ParsePublicKey("AAAA"); err == nil {
		t.Error("short key accepted")
	}
	if _, err := ParsePublicKey("!!"); err == nil {
		t.Error("bad base64 accepted")
	}
}

func TestDevPublicKeyParses(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("devkeys", "dev.pub"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParsePublicKey(string(b)); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultKeys(t *testing.T) {
	keys, err := DefaultKeys()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join("devkeys", "dev.pub"))
	devPub, _ := ParsePublicKey(string(b))
	_, trustsDev := keys[KeyID(devPub)]
	if trustsDev != DevBuild {
		t.Fatalf("dev key trusted = %v, DevBuild = %v", trustsDev, DevBuild)
	}
}

func TestState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "verified.json")
	s, err := LoadState(path)
	if err != nil || s != (State{}) {
		t.Fatalf("missing file: %+v, %v", s, err)
	}
	s.Record([]byte("abc"), &Manifest{Revision: 7}, testNow)
	s.Record([]byte("abc"), &Manifest{Revision: 5}, testNow)
	if s.HighestRevision != 7 {
		t.Fatalf("highest revision decreased: %d", s.HighestRevision)
	}
	if s.ManifestSHA256 != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("sha = %s", s.ManifestSHA256)
	}
	if err := SaveState(path, s); err != nil {
		t.Fatal(err)
	}
	got, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if !got.LastVerifiedAt.Equal(s.LastVerifiedAt) || got.HighestRevision != 7 || got.ManifestSHA256 != s.ManifestSHA256 {
		t.Fatalf("round trip: %+v != %+v", got, s)
	}
	if err := os.WriteFile(path, []byte(`{"highest_revision":1,"x":2}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadState(path); err == nil {
		t.Fatal("unknown field accepted")
	}
}
