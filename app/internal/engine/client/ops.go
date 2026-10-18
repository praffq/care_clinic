package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/agent"
	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
)

// FrameFunc receives the non-terminal (progress and log) frames of an op.
type FrameFunc func(protocol.Frame)

// Op runs op and streams its frames to onFrame (which may be nil). On success
// the result frame is decoded into result unless result is nil, e.g.
//
//	var st protocol.StackStatusResult
//	err := c.Op(ctx, protocol.StackStatus{}, &st, nil)
//
// An error frame is returned as a *protocol.OpError. See the package
// documentation for the other errors and for cancellation semantics.
// image.load needs a request body; use LoadImage.
func (c *Client) Op(ctx context.Context, op protocol.Op, result any, onFrame FrameFunc) error {
	name := op.OpName()
	if name == protocol.OpImageLoad {
		return errors.New("agent op image.load: use LoadImage")
	}
	if err := op.Validate(); err != nil {
		return err
	}
	b, err := json.Marshal(op)
	if err != nil {
		return err
	}
	return c.runOp(ctx, name, "", bytes.NewReader(b), int64(len(b)), "application/json", result, onFrame)
}

// LoadImage streams an image tarball (as written by "docker save") to the
// agent, which loads it and verifies op.Digest. Frames arrive while the
// tarball is still being sent. Cancelling ctx aborts the upload and the load,
// but LoadImage only returns once a pending tar.Read does, so tar should not
// block indefinitely (a file is fine).
func (c *Client) LoadImage(ctx context.Context, op protocol.ImageLoad, tar io.Reader, onFrame FrameFunc) error {
	if err := op.Validate(); err != nil {
		return err
	}
	params, err := json.Marshal(op)
	if err != nil {
		return err
	}
	q := url.Values{agent.ParamsQuery: {string(params)}}.Encode()
	return c.runOp(ctx, protocol.OpImageLoad, q, tar, -1, "application/x-tar", nil, onFrame)
}

func (c *Client) runOp(ctx context.Context, name, query string, body io.Reader, size int64, ctype string, result any, onFrame FrameFunc) error {
	path := "/v1/ops" + pathEscape(name)
	if query != "" {
		path += "?" + query
	}
	resp, err := c.do(ctx, http.MethodPost, path, body, size, ctype, http.StatusOK)
	if err != nil {
		return wrapOp(name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	return wrapOp(name, c.readFrames(ctx, resp.Body, result, onFrame))
}

func wrapOp(name string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("agent op %s: %w", name, err)
}

// readFrames consumes an NDJSON frame stream up to its terminal frame.
func (c *Client) readFrames(ctx context.Context, r io.Reader, result any, onFrame FrameFunc) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, min(64<<10, c.maxLine)), c.maxLine)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var f protocol.Frame
		if err := json.Unmarshal(line, &f); err != nil {
			return fmt.Errorf("%w: %v", ErrBadFrame, err)
		}
		switch {
		case f.Error != nil:
			return f.Error
		case f.Result != nil:
			if result == nil {
				return nil
			}
			if err := json.Unmarshal(f.Result, result); err != nil {
				return fmt.Errorf("%w: result: %v", ErrBadFrame, err)
			}
			return nil
		}
		if onFrame != nil {
			onFrame(f)
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	err := sc.Err()
	if errors.Is(err, bufio.ErrTooLong) {
		return fmt.Errorf("%w (%d bytes)", ErrLineTooLong, c.maxLine)
	}
	if err == nil {
		err = io.EOF
	}
	return fmt.Errorf("%w: %v", ErrIncompleteStream, err)
}
