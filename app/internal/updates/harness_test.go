package updates_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
	"github.com/ohcnetwork/care_desktop/app/internal/feed"
	"github.com/ohcnetwork/care_desktop/app/internal/updates"
	"github.com/ohcnetwork/care_desktop/app/internal/updates/updatestest"
)

var (
	base     = time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	errCrash = errors.New("simulated crash")
)

// published is when test feed revision rev is published.
func published(rev uint64) time.Time { return base.Add(time.Duration(rev) * time.Hour) }

// spec describes a test feed. CARE releases are named by backend rev
// ("A", "B", ...); each has its own backend image.
type spec struct {
	rev       uint64
	desktops  []string // desktop versions (default "1.0.0")
	engines   []eng
	cares     []string
	blocked   []string
	published time.Time // zero: published(rev)
	expires   time.Time // zero: a year later
	careNeeds string    // care requires.desktop (default ">=1.0.0")
	// engineNeeds is requires.desktop of engines after the first (default
	// ">=1.0.0").
	engineNeeds string
	builders    bool // CARE releases publish builders
}

type eng struct {
	version string
	schema  int
}

// harness is a coordinator over a fake world with a test feed and an
// asset server.
type harness struct {
	t     *testing.T
	dir   string
	w     *updatestest.World
	key   ed25519.PrivateKey
	keys  feed.TrustedKeys
	srv   *httptest.Server
	feed  *memFeed
	now   time.Time
	opts  func(*updates.Options)
	hook  hookFn
	c     *updates.Coordinator
	files map[string][]byte
	// requests is every asset request: "path range".
	requests []string
	mu       sync.Mutex
}

type hookFn struct {
	mu sync.Mutex
	fn func(string) error
}

func (h *hookFn) set(fn func(string) error) { h.mu.Lock(); h.fn = fn; h.mu.Unlock() }
func (h *hookFn) call(p string) error {
	h.mu.Lock()
	fn := h.fn
	h.mu.Unlock()
	if fn == nil {
		return nil
	}
	return fn(p)
}

// crashAt returns a hook that fails once at point.
func crashAt(point string) func(string) error {
	done := false
	return func(p string) error {
		if p == point && !done {
			done = true
			return errCrash
		}
		return nil
	}
}

type memFeed struct {
	mu        sync.Mutex
	data, sig []byte
}

func (f *memFeed) Fetch(context.Context) ([]byte, []byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.data == nil {
		return nil, nil, errors.New("no feed")
	}
	return f.data, f.sig, nil
}

func newHarness(t *testing.T, opts ...func(*updates.Options)) *harness {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{
		t: t, dir: t.TempDir(), w: updatestest.New(), key: key, keys: feed.NewTrustedKeys(pub),
		feed: &memFeed{}, now: base.Add(1000 * time.Hour), files: map[string][]byte{},
	}
	h.opts = func(o *updates.Options) {
		for _, f := range opts {
			f(o)
		}
	}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		b, ok := h.files[r.URL.Path]
		h.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		h.mu.Lock()
		h.requests = append(h.requests, r.URL.Path+" "+r.Header.Get("Range"))
		h.mu.Unlock()
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(b))
	}))
	t.Cleanup(h.srv.Close)
	h.c = h.open()
	return h
}

func (h *harness) options() updates.Options {
	o := updates.Options{
		Dir: h.dir, Feed: h.feed, Keys: h.keys, AllowInsecureURLs: true,
		Service: h.w.Service(), Agent: h.w, Stack: h.w,
		DesktopVersion: "1.0.0", DesktopPlatform: feed.PlatformDarwinARM64, EngineArch: feed.ArchARM64,
		RetryDelay: -1, BootTimeout: 50 * time.Millisecond, PollInterval: time.Millisecond,
		Now:      func() time.Time { return h.now },
		Log:      func(s string) { h.t.Log(s) },
		TestHook: h.hook.call,
	}
	h.opts(&o)
	return o
}

// open opens a new coordinator on the same dir, as a restarted process
// would.
func (h *harness) open() *updates.Coordinator {
	h.t.Helper()
	c, err := updates.Open(h.options())
	if err != nil {
		h.t.Fatal(err)
	}
	return c
}

func (h *harness) restart() { h.c = h.open() }

func (h *harness) serve(path string, b []byte) string {
	h.mu.Lock()
	h.files[path] = b
	h.mu.Unlock()
	return h.srv.URL + path
}

// Digest is the fake image digest of name.
func Digest(name string) protocol.Digest {
	s := sha256.Sum256([]byte("image " + name))
	return protocol.Digest("sha256:" + hex.EncodeToString(s[:]))
}

func careVersion(name string) string { return "3.1.0+be." + name }

// careReleased orders CARE releases by their name.
func careReleased(name string) time.Time { return base.Add(time.Duration(name[0]-'A') * time.Minute) }

func (h *harness) image(repo, name string, withTar bool) map[string]any {
	img := map[string]any{"ref": "example.org/" + repo + "@" + string(Digest(name)), "digest": string(Digest(name)), "size": imageSize}
	if withTar {
		img["tar"] = h.serve("/images/"+name+".tar", updatestest.ImageTar("example.org/"+repo))
	}
	return img
}

// publish signs s and serves it as the feed.
func (h *harness) publish(s spec) {
	h.t.Helper()
	data := h.build(s)
	sig := feed.Sign(data, h.key).Marshal()
	h.setFeed(data, sig)
}

func (h *harness) setFeed(data, sig []byte) {
	h.feed.mu.Lock()
	h.feed.data, h.feed.sig = data, sig
	h.feed.mu.Unlock()
}

func (h *harness) build(s spec) []byte {
	h.t.Helper()
	if s.published.IsZero() {
		s.published = published(s.rev)
	}
	if s.expires.IsZero() {
		s.expires = s.published.Add(365 * 24 * time.Hour)
	}
	if s.desktops == nil {
		s.desktops = []string{"1.0.0"}
	}
	if s.careNeeds == "" {
		s.careNeeds = ">=1.0.0"
	}
	var desktops, engines, cares []any
	for _, v := range s.desktops {
		b := []byte("installer " + v)
		desktops = append(desktops, map[string]any{
			"version":  v,
			"requires": map[string]any{"engine": ">=0.0.1"},
			"assets":   map[string]any{feed.PlatformDarwinARM64: map[string]any{"url": h.serve("/desktop/"+v, b), "sha256": updatestest.SHA256(b), "size": len(b)}},
		})
	}
	for i, e := range s.engines {
		b := updatestest.EngineBundle(e.version, e.schema)
		needs := ">=1.0.0"
		if i > 0 && s.engineNeeds != "" {
			needs = s.engineNeeds
		}
		engines = append(engines, map[string]any{
			"version": e.version, "data_schema": e.schema,
			"requires": map[string]any{"desktop": needs},
			"assets":   map[string]any{feed.ArchARM64: map[string]any{"url": h.serve("/engine/"+e.version, b), "sha256": updatestest.SHA256(b), "size": len(b)}},
		})
	}
	for _, name := range s.cares {
		r := map[string]any{
			"version": careVersion(name), "released_at": careReleased(name), "migrations": true,
			"requires": map[string]any{"desktop": s.careNeeds, "engine": ">=0.0.1"},
			"images": map[string]any{
				"backend":  h.image("care-backend", "backend-"+name, true),
				"frontend": h.image("care-frontend", "frontend", true),
				"caddy":    h.image("caddy", "caddy", false),
				"backup":   h.image("backup", "backup", false),
			},
		}
		if s.builders {
			r["builders"] = map[string]any{
				"backend-builder":  h.image("care-backend-builder", "builder-"+name, true),
				"frontend-builder": h.image("care-frontend-builder", "fe-builder-"+name, true),
			}
		}
		cares = append(cares, r)
	}
	m := map[string]any{
		"schema": 1, "revision": s.rev, "published_at": s.published, "expires": s.expires,
		"desktop": desktops, "engine": engines, "care": cares,
		"support_images": map[string]any{
			"postgres": h.image("postgres", "postgres", false),
			"redis":    h.image("redis", "redis", false),
			"silo":     h.image("silo", "silo", false),
		},
		"blocked": append([]string{}, s.blocked...),
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := feed.Verify(data, feed.Sign(data, h.key).Marshal(), feed.VerifyOptions{Keys: h.keys, Now: s.published, AllowInsecureURLs: true}); err != nil {
		h.t.Fatalf("test feed invalid: %v", err)
	}
	return data
}

func (h *harness) check() *updates.CheckResult {
	h.t.Helper()
	r, err := h.c.Check(context.Background())
	if err != nil {
		h.t.Fatalf("Check: %v", err)
	}
	return r
}

func (h *harness) apply() (*updates.ApplyResult, error) {
	return h.c.Apply(context.Background())
}

func (h *harness) mustApply() *updates.ApplyResult {
	h.t.Helper()
	r, err := h.apply()
	if err != nil {
		h.t.Fatalf("Apply: %v", err)
	}
	return r
}

func (h *harness) status() updates.Status {
	h.t.Helper()
	s, err := h.c.Status()
	if err != nil {
		h.t.Fatal(err)
	}
	return s
}

// installCare brings the world to engine 0.1.0 and CARE name, through the
// coordinator, at feed revision 1.
func (h *harness) installCare(name string) {
	h.t.Helper()
	h.publish(spec{rev: 1, engines: []eng{{"0.1.0", 1}}, cares: []string{name}})
	h.check()
	h.mustApply()
	h.wantOn("0.1.0", name)
}

// wantOn asserts the world and installed.json are on engine and CARE name,
// healthy, with no journal.
func (h *harness) wantOn(engine, name string) {
	h.t.Helper()
	if err := h.w.Healthy(); err != nil {
		h.t.Fatalf("world not healthy: %v\ncalls: %s", err, strings.Join(h.w.CallLog(), "\n  "))
	}
	h.w.Lock()
	cur, be := h.w.Current, h.w.Env[protocol.ImageRoleBackend]
	h.w.Unlock()
	if cur != engine {
		h.t.Fatalf("engine = %s, want %s", cur, engine)
	}
	if be != Digest("backend-"+name) {
		h.t.Fatalf("env backend = %s, want backend-%s", be, name)
	}
	st := h.status()
	if st.Journal != nil {
		h.t.Fatalf("journal left: %+v", st.Journal)
	}
	if st.Installed.Engine == nil || st.Installed.Engine.Version != engine {
		h.t.Fatalf("installed engine = %+v, want %s", st.Installed.Engine, engine)
	}
	if st.Installed.Care == nil || st.Installed.Care.Version != careVersion(name) {
		h.t.Fatalf("installed care = %+v, want %s", st.Installed.Care, careVersion(name))
	}
}

func (h *harness) lastHistory() updates.HistoryEntry {
	h.t.Helper()
	hs, err := h.c.History()
	if err != nil || len(hs) == 0 {
		h.t.Fatalf("history: %v (%d entries)", err, len(hs))
	}
	return hs[len(hs)-1]
}

func steps(r *updates.CheckResult) string {
	var s []string
	for _, st := range r.Steps {
		s = append(s, fmt.Sprintf("%s %s->%s", st.Component, st.From, st.To))
	}
	return strings.Join(s, ", ")
}
