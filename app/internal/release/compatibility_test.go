package release

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
)

func publishedCompatibilityFeed(t *testing.T) ([]byte, []byte) {
	t.Helper()
	original, err := os.ReadFile("testdata/published-revision1.json")
	if err != nil {
		t.Fatal(err)
	}
	var candidate Manifest
	if err := json.Unmarshal(original, &candidate); err != nil {
		t.Fatal(err)
	}
	for key, ref := range candidate.SupportImages {
		name, digest, _ := strings.Cut(ref, "@")
		if strings.Contains(name[strings.LastIndex(name, "/")+1:], ":") {
			name = name[:strings.LastIndex(name, ":")]
		}
		candidate.SupportImages[key] = name + "@" + digest
	}
	candidate.Revision = 2
	data, err := json.Marshal(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if path := os.Getenv("COMPAT_FEED_PATH"); path != "" {
		data, err = os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var actual Manifest
		if err := json.Unmarshal(data, &actual); err != nil {
			t.Fatal(err)
		}
		// Publication time is the only nondeterministic field.
		actual.PublishedAt = candidate.PublishedAt
		actualData, _ := json.Marshal(actual)
		if string(actualData) != stringMustJSON(t, candidate) {
			t.Fatal("published feed differs from the exact digest-only revision 2 candidate")
		}
	}
	return original, data
}

func stringMustJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestShippedDevelopmentClientCompatibility(t *testing.T) {
	original, candidate := publishedCompatibilityFeed(t)
	oldPattern, oldTrust := digestRefPattern, updateTrust
	t.Cleanup(func() { digestRefPattern, updateTrust = oldPattern, oldTrust })
	// The exact validator linked into the released d216653 installers.
	digestRefPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._/:-]*@sha256:[0-9a-f]{64}$`)
	updateTrust = "unsigned-development"
	served := original
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/updates-unsigned-development.json" {
			t.Errorf("unexpected endpoint: %s", r.URL.Path)
		}
		_, _ = w.Write(served)
	}))
	defer server.Close()
	client := NewClient(t.TempDir())
	client.HTTP = server.Client()
	if _, err := client.Fetch(context.Background(), server.URL+"/updates.json"); err == nil ||
		!strings.Contains(err.Error(), "support image silo must be pinned by digest") {
		t.Fatalf("original symptom was not reproduced: %v", err)
	}
	served = candidate
	if manifest, err := client.Fetch(context.Background(), server.URL+"/updates.json"); err != nil || manifest.Revision != 2 {
		t.Fatalf("original client could not fetch revision 2: revision=%d error=%v", manifest.Revision, err)
	}
	if manifest, _, err := client.ReadVerified(); err != nil || manifest.Revision != 2 {
		t.Fatalf("original client could not reload revision 2: revision=%d error=%v", manifest.Revision, err)
	}
	if endpoint := os.Getenv("COMPAT_LIVE_ENDPOINT"); endpoint != "" {
		live := NewClient(t.TempDir())
		if manifest, err := live.Fetch(context.Background(), endpoint); err != nil || manifest.Revision != 2 {
			t.Fatalf("original client could not fetch live revision 2: revision=%d error=%v", manifest.Revision, err)
		}
		if manifest, _, err := live.ReadVerified(); err != nil || manifest.Revision != 2 {
			t.Fatalf("original client could not reload live revision 2: revision=%d error=%v", manifest.Revision, err)
		}
	}
}

func TestPatchedClientUpgradesPersistedRevisionOne(t *testing.T) {
	original, candidate := publishedCompatibilityFeed(t)
	oldTrust := updateTrust
	t.Cleanup(func() { updateTrust = oldTrust })
	updateTrust = "unsigned-development"
	served := original
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(served)
	}))
	defer server.Close()
	client := NewClient(t.TempDir())
	client.HTTP = server.Client()
	if _, err := client.Fetch(context.Background(), server.URL+"/updates.json"); err != nil {
		t.Fatal(err)
	}
	served = candidate
	if _, err := client.Fetch(context.Background(), server.URL+"/updates.json"); err != nil {
		t.Fatal(err)
	}
	if manifest, _, err := client.ReadVerified(); err != nil || manifest.Revision != 2 {
		t.Fatalf("persisted revision 1 did not upgrade to 2: revision=%d error=%v", manifest.Revision, err)
	}
}
