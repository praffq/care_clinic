// Package engine is the desktop's operation and file-transfer client. It never
// invokes a local Docker executable.
package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Operation string

type Request struct {
	Operation Operation `json:"operation"`
	Args      []string  `json:"args"`
	Dir       string    `json:"dir,omitempty"`
	Env       []string  `json:"env,omitempty"`
}

type Event struct {
	Stream string `json:"stream,omitempty"`
	Data   string `json:"data,omitempty"`
	Error  string `json:"error,omitempty"`
	Exit   *int   `json:"exit,omitempty"`
}

type Entry struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	Mode    uint32 `json:"mode"`
	MtimeNs int64  `json:"mtime_ns"`
}

type Client struct {
	HTTP *http.Client
	URL  string
}

func ForURL(url string) *Client { return &Client{HTTP: &http.Client{}, URL: url} }

// Default is wired to the protected agent transport at application startup.
// Tests inject an HTTP fixture, never a host-command fallback.
var Default = New(func(context.Context) (net.Conn, error) {
	return nil, fmt.Errorf("CARE agent connection has not been configured")
})

func New(dial func(context.Context) (net.Conn, error)) *Client {
	return &Client{HTTP: &http.Client{Transport: &http.Transport{
		DialContext:        func(ctx context.Context, _, _ string) (net.Conn, error) { return dial(ctx) },
		DisableCompression: true, IdleConnTimeout: 30 * time.Second,
	}}}
}

func (c *Client) request(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	base := c.URL
	if base == "" {
		base = "http://agent"
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, body)
	if err != nil {
		return nil, err
	}
	if f, ok := body.(*os.File); ok {
		info, err := f.Stat()
		if err != nil {
			return nil, err
		}
		req.ContentLength = info.Size()
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("CARE agent: %w", err)
	}
	if resp.StatusCode >= 300 {
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("CARE agent %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return resp, nil
}

type ExitError struct{ Code int }

func (e *ExitError) Error() string {
	return fmt.Sprintf("agent operation exited with status %d", e.Code)
}

// Execute reads framed output until the final exit frame. EOF without that
// frame is an interrupted job, never a successful operation.
func (c *Client) Execute(ctx context.Context, in Request, output func(string, string)) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	resp, err := c.request(ctx, http.MethodPost, "/v1/jobs", bytes.NewReader(b))
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	dec := json.NewDecoder(resp.Body)
	for {
		var event Event
		if err := dec.Decode(&event); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("agent job stream interrupted: %w", err)
		}
		if event.Data != "" && output != nil {
			output(event.Stream, event.Data)
		}
		if event.Error != "" {
			return fmt.Errorf("agent job: %s", event.Error)
		}
		if event.Exit != nil {
			if *event.Exit != 0 {
				return &ExitError{Code: *event.Exit}
			}
			return nil
		}
	}
}

func (c *Client) Put(ctx context.Context, local, remote string) error {
	return c.PutMode(ctx, local, remote, 0)
}

// PutMode overrides the destination mode for public container inputs or
// private recovery material without changing the source file.
func (c *Client) PutMode(ctx context.Context, local, remote string, mode os.FileMode) error {
	f, err := os.Open(local)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("agent upload requires a regular file: %s", local)
	}
	if mode == 0 {
		mode = info.Mode().Perm()
	}
	q := url.Values{"path": {remote}, "mode": {fmt.Sprintf("%o", mode.Perm())}}
	resp, err := c.request(ctx, http.MethodPut, "/v1/fs/file?"+q.Encode(), f)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

func (c *Client) List(ctx context.Context, remote string) ([]Entry, error) {
	resp, err := c.request(ctx, http.MethodGet, "/v1/fs/list?root="+url.QueryEscape(remote), nil)
	if err != nil {
		return nil, err
	}

	defer func() { _ = resp.Body.Close() }()
	var entries []Entry
	err = json.NewDecoder(resp.Body).Decode(&entries)
	return entries, err
}

func (c *Client) Mkdir(ctx context.Context, remote string, mode os.FileMode) error {
	q := url.Values{"path": {remote}, "mode": {fmt.Sprintf("%o", mode.Perm())}}
	resp, err := c.request(ctx, http.MethodPut, "/v1/fs/dir?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

func (c *Client) Get(ctx context.Context, remote, local string) error {
	resp, err := c.request(ctx, http.MethodGet, "/v1/fs/file?path="+url.QueryEscape(remote), nil)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if err := os.MkdirAll(filepath.Dir(local), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(local), ".agent-download-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	n, err := io.Copy(f, resp.Body)
	if err == nil && resp.ContentLength >= 0 && n != resp.ContentLength {
		err = io.ErrUnexpectedEOF
	}
	if stamp := resp.Header.Get("X-Mtime-Ns"); err == nil && stamp != "" {
		ns, parseErr := strconv.ParseInt(stamp, 10, 64)
		switch {
		case parseErr != nil:
			err = fmt.Errorf("invalid agent file timestamp: %w", parseErr)
		case ns <= 0:
			err = fmt.Errorf("invalid agent file timestamp")
		default:
			t := time.Unix(0, ns)
			err = os.Chtimes(f.Name(), t, t)
		}
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), local)
}

func (c *Client) Remove(ctx context.Context, remote string) error {
	resp, err := c.request(ctx, http.MethodDelete, "/v1/fs/file?path="+url.QueryEscape(remote), nil)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

func (c *Client) Journal(ctx context.Context, out io.Writer) error {
	resp, err := c.request(ctx, http.MethodGet, "/v1/journal", nil)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, err = io.Copy(out, resp.Body)
	return err
}
