package fetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var body = strings.Repeat("care clinic qemu ", 1000)

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func server(t *testing.T, ranges bool, requests *atomic.Int32) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if !ranges {
			r.Header.Del("Range")
		}
		http.ServeContent(w, r, "q.exe", time.Time{}, strings.NewReader(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFile(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ranges bool
		part   string
	}{
		{"fresh", true, ""},
		{"resume", true, body[:1234]},
		{"complete part", true, body},
		{"no range support", false, body[:1234]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var n atomic.Int32
			srv := server(t, tc.ranges, &n)
			dest := filepath.Join(t.TempDir(), "q.exe")
			if tc.part != "" {
				if err := os.WriteFile(dest+".part", []byte(tc.part), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := File(context.Background(), srv.Client(), srv.URL, strings.ToUpper(sum(body)), dest); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(dest)
			if err != nil || string(got) != body {
				t.Fatalf("dest = %d bytes, %v", len(got), err)
			}
			if _, err := os.Stat(dest + ".part"); !os.IsNotExist(err) {
				t.Errorf(".part left behind: %v", err)
			}
			// A second call trusts the verified file.
			if err := File(context.Background(), srv.Client(), srv.URL, sum(body), dest); err != nil {
				t.Fatal(err)
			}
			if n.Load() != 1 {
				t.Errorf("%d requests, want 1", n.Load())
			}
		})
	}
}

func TestFileChecksumMismatch(t *testing.T) {
	var n atomic.Int32
	srv := server(t, true, &n)
	dest := filepath.Join(t.TempDir(), "q.exe")
	err := File(context.Background(), srv.Client(), srv.URL, sum("other"), dest)
	if !errors.Is(err, ErrChecksum) {
		t.Fatalf("err = %v", err)
	}
	for _, p := range []string{dest, dest + ".part"} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s exists: %v", p, err)
		}
	}
}

func TestFileHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	err := File(context.Background(), srv.Client(), srv.URL, sum(body), filepath.Join(t.TempDir(), "q.exe"))
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err = %v", err)
	}
}
