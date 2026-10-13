package main

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMirrorCapacityExactAllocationBoundary(t *testing.T) {
	for _, tt := range []struct {
		free uint64
		size int64
		ok   bool
	}{
		{mirrorReserveFree + 4096, 4096, true},
		{mirrorReserveFree + 4095, 4096, false},
		{mirrorReserveFree + 8192, 4097, true},
		{mirrorReserveFree + 8191, 4097, false},
		{mirrorReserveFree, 0, true},
		{mirrorReserveFree - 1, 0, false},
	} {
		a := &api{space: func(string) (uint64, uint64, error) { return tt.free, 64 << 30, nil }}
		err := a.checkMirrorSpace(t.TempDir(), tt.size)
		if (err == nil) != tt.ok {
			t.Fatalf("free=%d size=%d error=%v", tt.free, tt.size, err)
		}
	}
}

func TestMirrorCapacityFailureKeepsReplacementAndRemovesTemporaryFile(t *testing.T) {
	a := newAPI("")
	a.root = t.TempDir()
	original := filepath.Join(a.root, "backup")
	if err := os.WriteFile(original, []byte("old backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	a.space = func(string) (uint64, uint64, error) {
		calls++
		if calls == 1 {
			return mirrorReserveFree + 4096, 64 << 30, nil
		}
		return mirrorReserveFree, 64 << 30, nil
	}
	w := httptest.NewRecorder()
	a.putFile(w, httptest.NewRequest(http.MethodPut, "/v1/fs/file?path=/backup", strings.NewReader("replacement")))
	if w.Code != http.StatusInsufficientStorage || !strings.Contains(w.Body.String(), "5 GiB") {
		t.Fatalf("capacity failure not surfaced: %d %s", w.Code, w.Body.String())
	}
	b, err := os.ReadFile(original)
	if err != nil || string(b) != "old backup" {
		t.Fatalf("original replacement target lost: %q %v", b, err)
	}
	entries, err := os.ReadDir(a.root)
	if err != nil || len(entries) != 1 {
		t.Fatalf("failed upload left temporary allocation: %v %v", entries, err)
	}
}

type gatedUpload struct {
	io.Reader
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (g *gatedUpload) Read(p []byte) (int, error) {
	g.once.Do(func() {
		close(g.entered)
		<-g.release
	})
	return g.Reader.Read(p)
}

func TestConcurrentMirrorUploadsCannotSpendTheSameSpace(t *testing.T) {
	a := newAPI("")
	a.root = t.TempDir()
	a.space = func(string) (uint64, uint64, error) {
		var used uint64
		err := filepath.WalkDir(a.root, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			fi, err := d.Info()
			if err == nil && fi.Size() > 0 {
				used += (uint64(fi.Size()) + 4095) / 4096 * 4096
			}
			return err
		})
		return mirrorReserveFree + 4096 - used, 64 << 30, err
	}
	firstBody := &gatedUpload{Reader: bytes.NewReader(make([]byte, 4096)), entered: make(chan struct{}), release: make(chan struct{})}
	first, second := httptest.NewRecorder(), httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		a.putFile(first, httptest.NewRequest(http.MethodPut, "/v1/fs/file?path=/first", firstBody))
		close(done)
	}()
	<-firstBody.entered
	secondDone := make(chan struct{})
	go func() {
		a.putFile(second, httptest.NewRequest(http.MethodPut, "/v1/fs/file?path=/second", bytes.NewReader(make([]byte, 4096))))
		close(secondDone)
	}()
	select {
	case <-secondDone:
		t.Fatal("second upload did not wait for the first allocation")
	case <-time.After(20 * time.Millisecond):
	}
	close(firstBody.release)
	<-done
	<-secondDone
	if first.Code != http.StatusNoContent || second.Code != http.StatusInsufficientStorage {
		t.Fatalf("upload outcomes = %d %d", first.Code, second.Code)
	}
}

func TestMirrorCapacityInspectionErrorsAndTruncatedUploadsFailClosed(t *testing.T) {
	a := newAPI("")
	a.root = t.TempDir()
	problem := errors.New("cannot stat destination")
	a.space = func(string) (uint64, uint64, error) { return 0, 0, problem }
	if err := a.checkMirrorSpace(a.root, 1); !errors.Is(err, problem) {
		t.Fatalf("space inspection error swallowed: %v", err)
	}
	a.space = func(string) (uint64, uint64, error) { return 64 << 30, 64 << 30, nil }
	req := httptest.NewRequest(http.MethodPut, "/v1/fs/file?path=/truncated", strings.NewReader("short"))
	req.ContentLength = 100
	w := httptest.NewRecorder()
	a.putFile(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("truncated upload returned %d", w.Code)
	}
	if _, err := os.Stat(filepath.Join(a.root, "truncated")); !os.IsNotExist(err) {
		t.Fatalf("truncated upload became a completed file: %v", err)
	}
}
