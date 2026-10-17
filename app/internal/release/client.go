package release

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/ohcnetwork/care_desktop/app/internal/sys/atomicfile"
)

type verifiedState struct {
	Highest   uint64 `json:"highest_revision"`
	Manifest  []byte `json:"manifest"`
	Signature []byte `json:"signature"`
}

// Client persists the trust watermark and exact validated manifest in one atomic
// file. A Client is shared by all requests for a clinic.
type Client struct {
	StatePath           string
	HTTP                *http.Client
	unsignedDevelopment bool
}

var manifestMu sync.Mutex

// Set only by the release build's linker flags, never by runtime configuration.
var updateTrust = "signed"

func NewClient(clinicDir string) *Client {
	unsigned := updateTrust == "unsigned-development"
	name := "verified.json"
	if unsigned {
		name = "unsigned-development.json"
	}
	return &Client{StatePath: filepath.Join(clinicDir, "updates", name), unsignedDevelopment: unsigned}
}

func (c *Client) Fetch(ctx context.Context, endpoint string) (Manifest, error) {
	manifestMu.Lock()
	defer manifestMu.Unlock()
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	get := func(endpoint string, limit int64) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		if req.URL.Scheme != "https" || req.URL.User != nil || req.URL.Fragment != "" || req.URL.RawQuery != "" {
			return nil, fmt.Errorf("updates endpoint must be an HTTPS URL without credentials, query or fragment")
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("updates download: HTTP %d", resp.StatusCode)
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
		if err != nil {
			return nil, err
		}
		if int64(len(b)) > limit {
			return nil, fmt.Errorf("updates download exceeds size limit")
		}
		return b, nil
	}
	if c.unsignedDevelopment {
		endpoint, err := DevelopmentEndpoint(endpoint)
		if err != nil {
			return Manifest{}, err
		}
		data, err := get(endpoint, 4<<20)
		if err != nil {
			return Manifest{}, err
		}
		return c.accept(data, nil)
	}
	data, err := get(endpoint, 4<<20)
	if err != nil {
		return Manifest{}, err
	}
	sig, err := get(endpoint+".sig", 64)
	if err != nil {
		return Manifest{}, err
	}
	return c.accept(data, sig)
}

// ReadVerified returns the last accepted manifest after rechecking the build's
// trust policy and persisted revision watermark.
func (c *Client) ReadVerified() (Manifest, []byte, error) {
	manifestMu.Lock()
	defer manifestMu.Unlock()
	var previous verifiedState
	stored, err := os.ReadFile(c.StatePath)
	if err != nil {
		return Manifest{}, nil, fmt.Errorf("read verified update manifest: %w", err)
	}
	if err := json.Unmarshal(stored, &previous); err != nil {
		return Manifest{}, nil, fmt.Errorf("read manifest trust state: %w", err)
	}
	m, err := c.verify(previous.Manifest, previous.Signature, previous.Highest)
	if err != nil || m.Revision != previous.Highest {
		if err == nil {
			err = fmt.Errorf("persisted revision %d does not match manifest revision %d", previous.Highest, m.Revision)
		}
		return Manifest{}, nil, fmt.Errorf("invalid persisted manifest trust state: %w", err)
	}
	return m, append([]byte(nil), previous.Manifest...), nil
}

func (c *Client) accept(data, sig []byte) (Manifest, error) {
	var previous verifiedState
	stored, err := os.ReadFile(c.StatePath)
	if err == nil {
		if err := json.Unmarshal(stored, &previous); err != nil {
			return Manifest{}, fmt.Errorf("read manifest trust state: %w", err)
		}
		old, err := c.verify(previous.Manifest, previous.Signature, previous.Highest)
		if err != nil || old.Revision != previous.Highest {
			return Manifest{}, fmt.Errorf("invalid persisted manifest trust state")
		}
	} else if !os.IsNotExist(err) {
		return Manifest{}, err
	}
	m, err := c.verify(data, sig, previous.Highest)
	if err != nil {
		return m, err
	}
	if m.Revision == previous.Highest && string(data) != string(previous.Manifest) {
		return m, fmt.Errorf("manifest revision reused with different contents")
	}
	encoded, err := json.Marshal(verifiedState{Highest: m.Revision, Manifest: data, Signature: sig})
	if err != nil {
		return m, err
	}
	if err := atomicfile.Write(c.StatePath, encoded, 0o600); err != nil {
		return m, fmt.Errorf("persist verified manifest: %w", err)
	}
	return m, nil
}

func (c *Client) verify(data, sig []byte, highest uint64) (Manifest, error) {
	if c.unsignedDevelopment {
		return VerifyDevelopmentManifest(data, highest)
	}
	return VerifyManifest(data, sig, highest)
}
