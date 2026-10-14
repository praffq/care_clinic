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

// Client persists the trust watermark and exact verified manifest in one atomic
// file. A Client is shared by all requests for a clinic.
type Client struct {
	StatePath string
	HTTP      *http.Client
}

var manifestMu sync.Mutex

func NewClient(clinicDir string) *Client {
	return &Client{StatePath: filepath.Join(clinicDir, "updates", "verified.json")}
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
		defer resp.Body.Close()
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

func (c *Client) accept(data, sig []byte) (Manifest, error) {
	var previous verifiedState
	stored, err := os.ReadFile(c.StatePath)
	if err == nil {
		if err := json.Unmarshal(stored, &previous); err != nil {
			return Manifest{}, fmt.Errorf("read manifest trust state: %w", err)
		}
		old, err := VerifyManifest(previous.Manifest, previous.Signature, previous.Highest)
		if err != nil || old.Revision != previous.Highest {
			return Manifest{}, fmt.Errorf("invalid persisted manifest trust state")
		}
	} else if !os.IsNotExist(err) {
		return Manifest{}, err
	}
	m, err := VerifyManifest(data, sig, previous.Highest)
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
