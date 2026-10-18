package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
)

// baseURL is a placeholder: the DialFunc decides where requests go.
const baseURL = "http://care-engine-agent"

// DefaultMaxLineBytes bounds one NDJSON frame of an op stream.
const DefaultMaxLineBytes = 1 << 20

// maxJSONBody bounds non-streamed JSON responses (status, backups, diagnostics).
const maxJSONBody = 16 << 20

// DialFunc returns a new connection to the agent.
type DialFunc func(ctx context.Context) (net.Conn, error)

// TLSDialer returns a DialFunc for the agent's mutual-TLS TCP listener at
// addr. cfg must carry the client certificate and the CA that issued the
// agent's certificate; when cfg.ServerName is empty it is taken from addr.
// TLS 1.3 is required unless cfg sets a MinVersion.
func TLSDialer(addr string, cfg *tls.Config) DialFunc {
	cfg = cfg.Clone()
	if cfg.MinVersion == 0 {
		cfg.MinVersion = tls.VersionTLS13
	}
	d := &tls.Dialer{Config: cfg}
	return func(ctx context.Context) (net.Conn, error) {
		return d.DialContext(ctx, "tcp", addr)
	}
}

// Option configures a Client.
type Option func(*Client)

// WithMaxLineBytes sets the maximum size of one op stream frame.
func WithMaxLineBytes(n int) Option { return func(c *Client) { c.maxLine = n } }

// Client talks to one agent. It is safe for concurrent use.
type Client struct {
	hc      *http.Client
	maxLine int
}

// New returns a Client that reaches the agent through dial.
func New(dial DialFunc, opts ...Option) *Client {
	c := &Client{
		hc: &http.Client{
			// Requests carry their own deadlines via ctx; ops stream for minutes.
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return dial(ctx) },
				// One connection per request: dials are local and cheap, and a
				// pooled connection can silently die when the VM restarts,
				// which would fail non-replayable op POSTs.
				DisableKeepAlives: true,
			},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		maxLine: DefaultMaxLineBytes,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// do sends a request and returns the response when its status is want; any
// other status is converted to an error and the body closed.
func (c *Client) do(ctx context.Context, method, path string, body io.Reader, size int64, ctype string, want int) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.ContentLength = size
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("agent %s %s: %w", method, path, err)
	}
	if resp.StatusCode != want {
		defer closeBody(resp)
		return nil, responseError(resp)
	}
	return resp, nil
}

// doJSON sends in (when non-nil) as JSON and decodes a want response into out
// (when non-nil).
func (c *Client) doJSON(ctx context.Context, method, path string, in, out any, want int) error {
	var body io.Reader
	size := int64(0)
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body, size = bytes.NewReader(b), int64(len(b))
	}
	resp, err := c.do(ctx, method, path, body, size, "application/json", want)
	if err != nil {
		return err
	}
	defer closeBody(resp)
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxJSONBody)).Decode(out); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("agent %s %s: decode response: %w", method, path, err)
	}
	return nil
}

func closeBody(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBody))
	_ = resp.Body.Close()
}

// Close releases idle resources.
func (c *Client) Close() { c.hc.CloseIdleConnections() }

// Status returns the agent status.
func (c *Client) Status(ctx context.Context) (protocol.Status, error) {
	var st protocol.Status
	err := c.doJSON(ctx, http.MethodGet, "/v1/status", nil, &st, http.StatusOK)
	return st, err
}

// WaitReady polls Status every interval until docker_ready is true. When ctx
// ends first it returns the last status received (zero if none) and an error
// matching ctx.Err() that also wraps the last request error, if any.
func (c *Client) WaitReady(ctx context.Context, interval time.Duration) (protocol.Status, error) {
	t := time.NewTicker(interval)
	defer t.Stop()
	var (
		last    protocol.Status
		lastErr error
	)
	for {
		st, err := c.Status(ctx)
		if err == nil {
			if st.DockerReady {
				return st, nil
			}
			last, lastErr = st, nil
		} else if ctx.Err() == nil {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return last, fmt.Errorf("agent not ready: %w", errors.Join(ctx.Err(), lastErr))
			}
			return last, fmt.Errorf("agent not ready (docker_ready=false): %w", ctx.Err())
		case <-t.C:
		}
	}
}

// Diagnostics returns the agent's redacted diagnostics report.
func (c *Client) Diagnostics(ctx context.Context) (protocol.DiagnosticsReport, error) {
	var rep protocol.DiagnosticsReport
	err := c.doJSON(ctx, http.MethodGet, "/v1/diagnostics", nil, &rep, http.StatusOK)
	return rep, err
}

// SetTime sets the VM clock.
func (c *Client) SetTime(ctx context.Context, now time.Time) error {
	return c.doJSON(ctx, http.MethodPost, "/v1/time", protocol.TimeSync{Now: now}, nil, http.StatusNoContent)
}

// Shutdown asks the agent to power the VM off. It returns once the agent has
// accepted the request; the caller waits for the VM to stop. It fails with
// ErrBusy while a mutating op runs.
func (c *Client) Shutdown(ctx context.Context) error {
	return c.doJSON(ctx, http.MethodPost, "/v1/shutdown", nil, nil, http.StatusAccepted)
}

func pathEscape(segments ...string) string {
	var b bytes.Buffer
	for _, s := range segments {
		b.WriteByte('/')
		b.WriteString(url.PathEscape(s))
	}
	return b.String()
}
