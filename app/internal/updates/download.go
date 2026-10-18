package updates

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/ohcnetwork/care_desktop/app/internal/feed"
	"github.com/ohcnetwork/care_desktop/app/internal/stack"
)

// Feed size limits.
const (
	maxFeedBytes = 8 << 20
	maxSigBytes  = 4 << 10
)

// HTTPFetcher fetches URL and URL+".sig".
type HTTPFetcher struct {
	URL    string
	Client *http.Client // nil means http.DefaultClient
}

func (f HTTPFetcher) Fetch(ctx context.Context) ([]byte, []byte, error) {
	data, err := httpGet(ctx, f.Client, f.URL, maxFeedBytes)
	if err != nil {
		return nil, nil, err
	}
	sig, err := httpGet(ctx, f.Client, f.URL+".sig", maxSigBytes)
	if err != nil {
		return nil, nil, err
	}
	return data, sig, nil
}

func httpGet(ctx context.Context, hc *http.Client, url string, limit int64) ([]byte, error) {
	resp, err := open(ctx, hc, url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("GET %s: larger than %d bytes", url, limit)
	}
	return b, nil
}

func open(ctx context.Context, hc *http.Client, url string) (*http.Response, error) {
	if hc == nil {
		hc = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return resp, nil
}

// download fetches url into the cache as downloads/<sha256>, verifying size
// and SHA-256; a verified cached copy is reused.
func (c *Coordinator) download(ctx context.Context, url, sum string, size int64) (string, error) {
	dir := filepath.Join(c.o.Dir, downloadsDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, sum)
	if got, n, err := hashFile(dst); err == nil && got == sum && n == size {
		return dst, nil
	}
	c.logf("Downloading %s", url)
	err := c.fetchTo(ctx, url, dst, func(f *os.File) error {
		got, n, err := hashFile(f.Name())
		switch {
		case err != nil:
			return err
		case n != size:
			return fmt.Errorf("download %s: %d bytes, want %d", url, n, size)
		case got != sum:
			return fmt.Errorf("download %s: sha256 %s, want %s", url, got, sum)
		}
		return nil
	}, size)
	if err != nil {
		return "", err
	}
	return dst, nil
}

// fetchTo downloads url to a temp file, checks it, and renames it to dst.
func (c *Coordinator) fetchTo(ctx context.Context, url, dst string, check func(*os.File) error, limit int64) error {
	resp, err := open(ctx, c.o.HTTP, url)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	f, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+"-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	var r io.Reader = resp.Body
	if limit > 0 {
		r = io.LimitReader(resp.Body, limit+1)
	}
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		return fmt.Errorf("download %s: %w", url, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if check != nil {
		if err := check(f); err != nil {
			return err
		}
	}
	return os.Rename(f.Name(), dst)
}

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// ErrImageRepo means an image tarball was saved under a different repository
// than the feed pins. Docker resolves the stack's repo@digest references by
// repository, so the loaded image would be missing when the stack starts.
var ErrImageRepo = errors.New("image tarball repository does not match the feed")

// downloadImages fetches the tarball of every image that has a URL (dev
// feeds) and sets its Tarball. The agent verifies the digest on load, so
// there is no checksum to check here, but the repository the tarball was
// saved under must be the one the feed pins.
func (c *Coordinator) downloadImages(ctx context.Context, imgs []Image) error {
	dir := filepath.Join(c.o.Dir, downloadsDir, "images")
	for i := range imgs {
		img := &imgs[i]
		if img.URL == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		dst := filepath.Join(dir, strings.TrimPrefix(string(img.Digest), "sha256:")+".tar")
		if st, err := os.Stat(dst); err != nil || st.Size() == 0 {
			c.logf("Downloading %s", img.URL)
			if err := c.fetchTo(ctx, img.URL, dst, nil, 0); err != nil {
				return err
			}
		}
		repo, err := stack.TarballRepo(dst)
		if err != nil {
			return fmt.Errorf("%s image %s: %w", img.Role, img.URL, err)
		}
		if normalizeRepo(repo) != normalizeRepo(img.Repo) {
			return fmt.Errorf("%w: %s image %s is %s, the feed pins %s", ErrImageRepo, img.Role, img.URL, repo, img.Repo)
		}
		img.Tarball = dst
	}
	return nil
}

// normalizeRepo expands Docker Hub short names (postgres,
// library/postgres) to docker.io/library/postgres.
func normalizeRepo(r string) string {
	first, _, ok := strings.Cut(r, "/")
	if !ok {
		return "docker.io/library/" + r
	}
	if !strings.ContainsAny(first, ".:") && first != "localhost" {
		return "docker.io/" + r
	}
	return r
}

func (c *Coordinator) downloadDesktop(ctx context.Context, r *feed.DesktopRelease) (string, error) {
	a, ok := r.Assets[c.o.DesktopPlatform]
	if !ok {
		return "", fmt.Errorf("desktop %s has no %s installer", r.Version, c.o.DesktopPlatform)
	}
	return c.download(ctx, a.URL, a.SHA256, a.Size)
}

// prefetch downloads every artifact of p, stages engine slots and pulls
// CARE images into the running engine. It changes nothing that is in use.
func (c *Coordinator) prefetch(ctx context.Context, m *feed.Manifest, in Installed, p *feed.Plan) error {
	var errs []error
	for _, s := range p.Steps {
		switch s.Component {
		case feed.ComponentDesktop:
			if _, err := c.downloadDesktop(ctx, s.Desktop); err != nil {
				errs = append(errs, err)
			}
		case feed.ComponentEngine:
			a, ok := s.Engine.Assets[c.o.EngineArch]
			if !ok {
				errs = append(errs, fmt.Errorf("engine %s has no %s asset", s.To, c.o.EngineArch))
				continue
			}
			t := &EngineTarget{Version: s.To, URL: a.URL, SHA256: a.SHA256, Size: a.Size}
			if err := c.stageEngine(ctx, t); err != nil {
				errs = append(errs, err)
			}
		case feed.ComponentCare:
			imgs := careImages(s.Care, m)
			if err := c.downloadImages(ctx, imgs); err != nil {
				errs = append(errs, err)
				continue
			}
			// Images can only be pulled into a running engine; a fresh
			// install pulls them while applying.
			if in.Engine == nil {
				continue
			}
			if err := c.o.Stack.EnsureImages(ctx, StackImages(imgs)); err != nil {
				errs = append(errs, fmt.Errorf("pull CARE %s images: %w", s.To, err))
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return errors.Join(errs...)
}
