package updates

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
	"github.com/ohcnetwork/care_desktop/app/internal/feed"
	"github.com/ohcnetwork/care_desktop/app/internal/stack"
	"github.com/ohcnetwork/care_desktop/app/internal/versions"
)

var ErrFeedUnreachable = errors.New("couldn't reach the update server")

type Release struct {
	Version    string
	ReleasedAt time.Time
	Revision   uint64
	Unsigned   bool
	Images     []Image
	Builders   []Image
	BuildEnv   map[string]string
	StartedAt  time.Time
}

func (r *Release) Pull() []Image {
	return append(slices.Clone(r.Images), r.Builders...)
}

type DiskSpaceError struct {
	Free, Need uint64
}

func (e *DiskSpaceError) Error() string {
	return fmt.Sprintf("not enough free space in the clinic engine to download CARE: %s free, %s needed", bytesStr(e.Free), bytesStr(e.Need))
}

func (c *Coordinator) LatestCompatible(ctx context.Context) (*Release, error) {
	unlock, err := c.acquire()
	if err != nil {
		return nil, err
	}
	defer unlock()
	m, err := c.fetchFeed(ctx)
	if err != nil {
		return nil, err
	}
	desktop, err := versions.ParseSemver(c.o.DesktopVersion)
	if err != nil {
		return nil, fmt.Errorf("desktop version: %w", err)
	}
	st, err := c.o.Agent.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("engine status: %w", err)
	}
	engine, err := versions.ParseSemver(strings.TrimPrefix(st.EngineVersion, "v"))
	if err != nil {
		return nil, fmt.Errorf("engine version %q: %w", st.EngineVersion, err)
	}
	r, err := feed.LatestCompatibleCare(m, desktop, engine)
	if err != nil {
		c.logf("No CARE release in update feed revision %d can be installed: %v", m.Revision, err)
		return nil, err
	}
	rel := &Release{
		Version: r.Version.String(), ReleasedAt: r.ReleasedAt, Revision: m.Revision, Unsigned: m.Unsigned,
		Images: careImages(r, m), BuildEnv: r.BuildEnv, StartedAt: c.now(),
	}
	for _, role := range protocol.BuilderRoles {
		if b := releaseBuilder(r, role); b != nil {
			rel.Builders = append(rel.Builders, *b)
		} else {
			c.logf("warning: CARE %s publishes no %s image; custom plugins or frontend settings that need it will not work", rel.Version, role)
		}
	}
	c.logf("Chose CARE %s from update feed revision %d (app %s, engine %s)", rel.Version, m.Revision, desktop, engine)
	return rel, nil
}

func (c *Coordinator) PrepareInstall(ctx context.Context, r *Release, have map[protocol.Digest]bool) ([]Image, error) {
	pull := r.Pull()
	need := c.o.ImageReserveBytes
	missing := 0
	for _, img := range pull {
		if !have[img.Digest] {
			need += 2 * uint64(max(img.Size, 0))
			missing++
		}
	}
	if missing == 0 {
		return pull, nil
	}
	st, err := c.o.Agent.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("engine status: %w", err)
	}
	if st.DiskFreeBytes < need {
		return nil, &DiskSpaceError{Free: st.DiskFreeBytes, Need: need}
	}
	for i := range pull {
		if have[pull[i].Digest] {
			continue
		}
		if err := c.downloadImages(ctx, pull[i:i+1]); err != nil {
			return nil, &stack.ImageError{Image: StackImages(pull[i : i+1])[0], Err: err}
		}
	}
	return pull, nil
}

func (c *Coordinator) RecordInstall(ctx context.Context, r *Release) error {
	unlock, err := c.acquire()
	if err != nil {
		return err
	}
	defer unlock()
	in, err := c.st.installed()
	if err != nil {
		return err
	}
	e, err := c.activeEngine(ctx)
	if err != nil {
		return err
	}
	if e != nil {
		in.Engine = e
	}
	imgs := slices.Clone(r.Images)
	for i := range imgs {
		imgs[i].Tarball = ""
	}
	in.Care = &InstalledCare{Version: r.Version, ReleasedAt: r.ReleasedAt, Images: imgs}
	in.PreviousImages = nil
	in.Desktop = c.o.DesktopVersion
	in.UpdatedAt = c.now()
	if err := c.st.save(InstalledFile, in); err != nil {
		return err
	}
	now := c.now()
	h := HistoryEntry{
		ID:        fmt.Sprintf("%s-install-%s-%d", feed.ComponentCare, now.Format("20060102T150405Z"), now.Nanosecond()),
		Component: feed.ComponentCare, To: r.Version, Revision: r.Revision, Result: ResultApplied,
		StartedAt: r.StartedAt, FinishedAt: now,
	}
	if err := c.st.appendHistory(h); err != nil {
		return err
	}
	c.logf("Recorded CARE %s (update feed revision %d) as installed", r.Version, r.Revision)
	return nil
}
