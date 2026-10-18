package updates_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/feed"
	"github.com/ohcnetwork/care_desktop/app/internal/updates"
)

func TestUnsignedFeedWithoutProductionKey(t *testing.T) {
	var mu sync.Mutex
	var logs []string
	h := newHarness(t, func(o *updates.Options) {
		o.Keys, o.AllowUnsigned = nil, true
		o.Log = func(s string) { mu.Lock(); logs = append(logs, s); mu.Unlock() }
	})
	s := spec{rev: 1, engines: []eng{{"0.1.0", 1}}, cares: []string{"A"}}
	h.setFeed(h.build(s), nil)
	res := h.check()
	if !res.Unsigned || !res.Available {
		t.Fatalf("check = %+v, want an unsigned, available plan", res)
	}
	if st := h.status(); !st.Unsigned {
		t.Fatalf("status = %+v, want unsigned", st)
	}
	mu.Lock()
	warned := strings.Contains(strings.Join(logs, "\n"), updates.UnsignedFeedWarning)
	mu.Unlock()
	if !warned {
		t.Fatalf("no unsigned-feed warning in %q", logs)
	}
	h.mustApply()
	h.wantOn("0.1.0", "A")
}

func TestUnsignedFeedRejectedWithKey(t *testing.T) {
	h := newHarness(t)
	h.setFeed(h.build(spec{rev: 1, engines: []eng{{"0.1.0", 1}}, cares: []string{"A"}}), nil)
	if _, err := h.c.Check(context.Background()); !errors.Is(err, feed.ErrBadSignature) {
		t.Fatalf("unsigned feed with a trusted key: %v, want ErrBadSignature", err)
	}
	// Allowing unsigned feeds never skips checking a signature that is there.
	h = newHarness(t, func(o *updates.Options) { o.AllowUnsigned = true })
	data := h.build(spec{rev: 1, engines: []eng{{"0.1.0", 1}}, cares: []string{"A"}})
	sig := feed.Sign(data, h.key).Marshal()
	h.setFeed(append(append([]byte(nil), data[:len(data)-1]...), ' ', '}'), sig)
	if _, err := h.c.Check(context.Background()); !errors.Is(err, feed.ErrBadSignature) {
		t.Fatalf("tampered feed: %v, want ErrBadSignature", err)
	}
	h.publish(spec{rev: 2, engines: []eng{{"0.1.0", 1}}, cares: []string{"A"}})
	if res := h.check(); res.Unsigned {
		t.Fatalf("signed feed reported unsigned: %+v", res)
	}
}

func TestOpenRequiresKeysUnlessUnsignedAllowed(t *testing.T) {
	h := newHarness(t)
	o := h.options()
	o.Keys = nil
	if _, err := updates.Open(o); err == nil {
		t.Fatal("opened with no trusted keys")
	}
	o.AllowUnsigned = true
	if _, err := updates.Open(o); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPFetcherMissingSignature(t *testing.T) {
	status := http.StatusNotFound
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sig") {
			http.Error(w, "no", status)
			return
		}
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()
	f := updates.HTTPFetcher{URL: srv.URL + "/updates.json"}
	data, sig, err := f.Fetch(context.Background())
	if err != nil || string(data) != "{}" || sig != nil {
		t.Fatalf("404 signature: %q, %q, %v", data, sig, err)
	}
	status = http.StatusInternalServerError
	if _, _, err := f.Fetch(context.Background()); err == nil {
		t.Fatal("a failing signature download was treated as unsigned")
	}
}
