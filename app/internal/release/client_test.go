package release

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestClientBuildTimeTrustModes(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	oldTrust, oldKeys := updateTrust, PublicKeys
	t.Cleanup(func() { updateTrust, PublicKeys = oldTrust, oldKeys })
	PublicKeys = []string{base64.StdEncoding.EncodeToString(pub)}
	m := testManifest(t)
	data, _ := json.Marshal(m)
	signature := ed25519.Sign(key, data)
	for _, tc := range []struct {
		name, mode string
		status     int
		signature  []byte
		ok         bool
		paths      string
	}{
		{"signed", "signed", 200, signature, true, "/updates.json,/updates.json.sig"},
		{"bad signature", "signed", 200, make([]byte, 64), false, "/updates.json,/updates.json.sig"},
		{"missing signature", "signed", 404, nil, false, "/updates.json,/updates.json.sig"},
		{"network failure", "signed", 503, nil, false, "/updates.json"},
		{"unsigned development", "unsigned-development", 200, nil, true, "/updates-unsigned-development.json"},
		{"development network failure", "unsigned-development", 503, nil, false, "/updates-unsigned-development.json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			updateTrust = tc.mode
			var paths []string
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths = append(paths, r.URL.Path)
				if strings.HasSuffix(r.URL.Path, ".sig") {
					w.WriteHeader(tc.status)
					_, _ = w.Write(tc.signature)
					return
				}
				if tc.status == 503 {
					w.WriteHeader(tc.status)
					return
				}
				_, _ = w.Write(data)
			}))
			defer server.Close()
			c := NewClient(t.TempDir())
			c.HTTP = server.Client()
			_, err := c.Fetch(context.Background(), server.URL+"/updates.json")
			if (err == nil) != tc.ok || strings.Join(paths, ",") != tc.paths {
				t.Fatalf("fetch error=%v paths=%v", err, paths)
			}
			if !tc.ok {
				if _, err := os.Stat(c.StatePath); !os.IsNotExist(err) {
					t.Fatalf("failed fetch persisted metadata: %v", err)
				}
				return
			}
			if cached, _, err := c.ReadVerified(); err != nil || cached.Revision != m.Revision {
				t.Fatalf("cached manifest: %+v, %v", cached, err)
			}
		})
	}
}

func TestUnsignedDevelopmentRetainsValidationAndSeparateTrustState(t *testing.T) {
	old := updateTrust
	t.Cleanup(func() { updateTrust = old })
	updateTrust = "unsigned-development"
	dir := t.TempDir()
	c := NewClient(dir)
	m := testManifest(t)
	m.Revision = 2
	data, _ := json.Marshal(m)
	if _, err := c.accept(data, nil); err != nil {
		t.Fatal(err)
	}
	restarted := NewClient(dir)
	for _, mutate := range []func(*Manifest){
		func(m *Manifest) { m.Revision = 1 },
		func(m *Manifest) { m.PublishedAt = "2026-10-14T14:01:00Z" },
		func(m *Manifest) { m.Care.Backend.Image = "care:latest" },
		func(m *Manifest) { delete(m.Engine.Assets, "linux-arm64") },
	} {
		candidate := testManifest(t)
		candidate.Revision = 2
		mutate(&candidate)
		bad, _ := json.Marshal(candidate)
		if _, err := restarted.accept(bad, nil); err == nil {
			t.Fatal("invalid development manifest accepted")
		}
	}
	updateTrust = "signed"
	signed := NewClient(dir)
	if signed.StatePath == c.StatePath {
		t.Fatal("signed and development clients share trust state")
	}
	if _, err := signed.accept(data, nil); err == nil {
		t.Fatal("signed client downgraded to unsigned metadata")
	}
	if err := os.WriteFile(c.StatePath, []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.accept(data, nil); err == nil {
		t.Fatal("development client silently reset corrupt state")
	}
}

func TestDevelopmentEndpoint(t *testing.T) {
	got, err := DevelopmentEndpoint("https://example.test/clinic/updates.json")
	if err != nil || got != "https://example.test/clinic/updates-unsigned-development.json" {
		t.Fatalf("endpoint=%s error=%v", got, err)
	}
	for _, endpoint := range []string{"http://example.test/updates.json", "https://example.test/updates.json?query=1", "https://example.test/updates"} {
		if _, err := DevelopmentEndpoint(endpoint); err == nil {
			t.Fatalf("invalid development endpoint accepted: %s", endpoint)
		}
	}
}
