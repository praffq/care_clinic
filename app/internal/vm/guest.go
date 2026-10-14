package vm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"
)

// guest talks to the agent's control API inside the VM.
type guest struct {
	http *http.Client
}

func newGuest(dial func(ctx context.Context) (net.Conn, error)) *guest {
	tr := &http.Transport{
		DialContext:         func(ctx context.Context, _, _ string) (net.Conn, error) { return dial(ctx) },
		MaxIdleConns:        2,
		IdleConnTimeout:     30 * time.Second,
		DisableCompression:  true,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	return &guest{http: &http.Client{Transport: tr}}
}

type guestStatus struct {
	Docker  bool   `json:"docker"`
	Version string `json:"version"`
	Uptime  int64  `json:"uptime"`
}

func (g *guest) do(ctx context.Context, method, path string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, "http://agent"+path, body)
	if err != nil {
		return err
	}
	if file, ok := body.(*os.File); ok {
		info, err := file.Stat()
		if err != nil {
			return fmt.Errorf("could not measure mirrored file: %w", err)
		}
		offset, err := file.Seek(0, io.SeekCurrent)
		if err != nil {
			return fmt.Errorf("could not measure mirrored file position: %w", err)
		}
		req.ContentLength = info.Size() - offset
	}
	resp, err := g.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("engine agent %s %s: %s %s", method, path, resp.Status, bytes.TrimSpace(msg))
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if w, ok := out.(io.Writer); ok {
		_, err = io.Copy(w, resp.Body)
		return err
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func jsonBody(v any) io.Reader {
	b, _ := json.Marshal(v)
	return bytes.NewReader(b)
}

func (g *guest) status(ctx context.Context) (guestStatus, error) {
	var s guestStatus
	err := g.do(ctx, http.MethodGet, "/v1/status", nil, &s)
	return s, err
}

func (g *guest) setTime(ctx context.Context, t time.Time) error {
	return g.do(ctx, http.MethodPost, "/v1/time", jsonBody(map[string]int64{"unix_nano": t.UnixNano()}), nil)
}

func (g *guest) shutdown(ctx context.Context) error {
	return g.do(ctx, http.MethodPost, "/v1/shutdown", nil, nil)
}

func (g *guest) df(ctx context.Context) (free, total uint64, err error) {
	var out struct{ Free, Total uint64 }
	err = g.do(ctx, http.MethodGet, "/v1/df", nil, &out)
	return out.Free, out.Total, err
}

func (g *guest) journal(ctx context.Context, w io.Writer) error {
	return g.do(ctx, http.MethodGet, "/v1/journal", nil, w)
}
