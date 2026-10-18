package control

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/client"
	"github.com/ohcnetwork/care_desktop/app/internal/service/api"
	"github.com/ohcnetwork/care_desktop/app/internal/service/slots"
)

const clientBaseURL = "http://care-clinic-service"

// APIError is a non-2xx control API response.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	s := fmt.Sprintf("care-clinic-service: HTTP %d %s", e.Status, e.Code)
	if e.Message != "" {
		s += ": " + e.Message
	}
	return s
}

// Is maps error codes to the sentinel errors they report.
func (e *APIError) Is(target error) bool {
	switch target {
	case api.ErrNoEngine:
		return e.Code == api.CodeNoEngine
	case api.ErrEngineUnavailable:
		return e.Code == api.CodeEngineUnavailable
	case slots.ErrNotInstalled:
		return e.Code == api.CodeNotInstalled
	case slots.ErrChecksum:
		return e.Code == api.CodeChecksumMismatch
	case slots.ErrIsCurrent:
		return e.Code == api.CodeIsCurrent
	case api.ErrInvalidConfig:
		return e.Code == api.CodeInvalidConfig
	}
	return false
}

// Client talks to the host service over its control socket.
type Client struct {
	socket string
	hc     *http.Client
}

// NewClient returns a Client for the socket at path.
func NewClient(socket string) *Client {
	c := &Client{socket: socket}
	c.hc = &http.Client{Transport: &http.Transport{
		DialContext:       func(ctx context.Context, _, _ string) (net.Conn, error) { return c.Dial(ctx) },
		DisableKeepAlives: true,
		// Uploads wait for the service to accept their size before sending.
		ExpectContinueTimeout: 10 * time.Second,
	}}
	return c
}

// Dial opens a connection to the control socket.
func (c *Client) Dial(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", c.socket)
}

// Agent returns an agent client that goes through the service's relay.
func (c *Client) Agent(opts ...client.Option) *client.Client {
	return client.New(c.Dial, append(opts, client.WithBasePath(api.AgentPrefix))...)
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	size := int64(0)
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body, size = bytes.NewReader(b), int64(len(b))
	}
	return c.doBody(ctx, method, path, body, size, "application/json", out)
}

func (c *Client) doBody(ctx context.Context, method, path string, body io.Reader, size int64, ctype string, out any) error {
	resp, err := c.send(ctx, method, path, body, size, ctype)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("care-clinic-service %s %s: %w", method, path, err)
	}
	return nil
}

// send returns a 2xx response; anything else becomes an *APIError.
// A nil body sends none; a sized body waits for 100 Continue.
func (c *Client) send(ctx context.Context, method, path string, body io.Reader, size int64, ctype string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, clientBaseURL+path, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.ContentLength = size
		if ctype != "application/json" {
			req.Header.Set("Expect", "100-continue")
		}
		req.Header.Set("Content-Type", ctype)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("care-clinic-service: %w", err)
	}
	if resp.StatusCode/100 == 2 {
		return resp, nil
	}
	defer func() { _ = resp.Body.Close() }()
	var e api.Error
	_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&e)
	return nil, &APIError{Status: resp.StatusCode, Code: e.Code, Message: e.Message}
}

// Status returns the service status.
func (c *Client) Status(ctx context.Context) (api.ServiceStatus, error) {
	var st api.ServiceStatus
	err := c.do(ctx, http.MethodGet, "/v1/service/status", nil, &st)
	return st, err
}

// Config returns the service configuration.
func (c *Client) Config(ctx context.Context) (api.ServiceConfig, error) {
	var cfg api.ServiceConfig
	err := c.do(ctx, http.MethodGet, "/v1/service/config", nil, &cfg)
	return cfg, err
}

// SetConfig replaces the service configuration and returns it normalized.
// Invalid values match api.ErrInvalidConfig.
func (c *Client) SetConfig(ctx context.Context, cfg api.ServiceConfig) (api.ServiceConfig, error) {
	var out api.ServiceConfig
	err := c.do(ctx, http.MethodPost, "/v1/service/config", cfg, &out)
	return out, err
}

// Watch calls fn with the current status and after every change until ctx
// ends or the connection fails.
func (c *Client) Watch(ctx context.Context, fn func(api.ServiceStatus)) error {
	resp, err := c.send(ctx, http.MethodGet, "/v1/service/status?watch=1", nil, 0, "")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var st api.ServiceStatus
		if err := json.Unmarshal(sc.Bytes(), &st); err != nil {
			return fmt.Errorf("care-clinic-service: watch: %w", err)
		}
		fn(st)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return io.ErrUnexpectedEOF
}

func (c *Client) vm(ctx context.Context, action string) (api.EngineStatus, error) {
	var st api.EngineStatus
	err := c.do(ctx, http.MethodPost, "/v1/vm/"+action, nil, &st)
	return st, err
}

// Start boots the engine; it returns once the VM has started.
func (c *Client) Start(ctx context.Context) (api.EngineStatus, error) { return c.vm(ctx, "start") }

// Stop shuts the engine down gracefully.
func (c *Client) Stop(ctx context.Context) (api.EngineStatus, error) { return c.vm(ctx, "stop") }

// Restart stops and boots the engine.
func (c *Client) Restart(ctx context.Context) (api.EngineStatus, error) { return c.vm(ctx, "restart") }

// Slots lists the installed engine slots.
func (c *Client) Slots(ctx context.Context) (api.SlotList, error) {
	var l api.SlotList
	err := c.do(ctx, http.MethodGet, "/v1/engine/slots", nil, &l)
	return l, err
}

// InstallEngine uploads an engine tarball of the given size, which must have
// the given SHA-256, and installs it.
func (c *Client) InstallEngine(ctx context.Context, tarball io.Reader, size int64, sha256 string) (slots.Slot, error) {
	var sl slots.Slot
	if size <= 0 {
		return sl, errors.New("care-clinic-service: install engine: tarball size required")
	}
	path := "/v1/engine/install?" + url.Values{"sha256": {sha256}}.Encode()
	err := c.doBody(ctx, http.MethodPost, path, tarball, size, "application/gzip", &sl)
	return sl, err
}

// Activate switches the engine to an installed version and boots it.
func (c *Client) Activate(ctx context.Context, version string) (api.EngineStatus, error) {
	var st api.EngineStatus
	err := c.do(ctx, http.MethodPost, "/v1/engine/activate", api.ActivateRequest{Version: version}, &st)
	return st, err
}

// Prune deletes the slots other than current and previous.
func (c *Client) Prune(ctx context.Context) ([]string, error) {
	var r api.PruneResult
	err := c.do(ctx, http.MethodPost, "/v1/engine/prune", nil, &r)
	return r.Removed, err
}
