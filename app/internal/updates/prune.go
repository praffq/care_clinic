package updates

import (
	"context"
	"errors"
	"fmt"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
)

// PruneImages removes the engine's unused docker images to free space in
// the VM disk. It keeps what the stack runs (also pinned by the stack env,
// which the agent keeps on its own), the images before the last CARE,
// plugins or frontend update (for a rollback), the CARE builder images (for
// plugin and frontend builds), and every image a container uses.
// Downloaded images of an update not yet applied are removed and loaded
// again when it is applied.
func (c *Coordinator) PruneImages(ctx context.Context) (protocol.ImagePruneResult, error) {
	var res protocol.ImagePruneResult
	unlock, err := c.acquire()
	if err != nil {
		return res, err
	}
	defer unlock()
	if j, err := c.st.journal(); err != nil {
		return res, err
	} else if j != nil {
		return res, fmt.Errorf("%w (%s update to %s is %s)", ErrInFlight, j.Component, j.To, j.State)
	}
	in, err := c.installed()
	if err != nil {
		return res, err
	}
	cur, err := c.current(ctx, in)
	if err != nil {
		return res, err
	}
	if cur.Base == nil {
		return res, ErrNotInstalled
	}
	imgs := append(append([]Image{}, cur.Images...), cur.Base...)
	imgs = append(imgs, in.PreviousImages...)
	for _, role := range protocol.BuilderRoles {
		if b, _, err := c.installedBuilder(in, role); err == nil {
			imgs = append(imgs, *b)
		} else if !errors.Is(err, ErrNoBuilder) && !errors.Is(err, ErrNoFrontendBuilder) {
			return res, err
		}
	}
	op := protocol.ImagePrune{Keep: keepDigests(imgs)}
	if err := op.Validate(); err != nil {
		return res, err
	}
	c.logf("Removing unused engine images (keeping %d)", len(op.Keep))
	if err := c.o.Agent.Op(ctx, op, &res); err != nil {
		return res, err
	}
	c.logf("Removed %d unused engine images (%d in use were kept)", res.Removed, res.Skipped)
	return res, nil
}

func keepDigests(imgs []Image) []protocol.Digest {
	seen := map[protocol.Digest]bool{}
	var out []protocol.Digest
	for _, i := range imgs {
		if i.Digest != "" && !seen[i.Digest] {
			seen[i.Digest] = true
			out = append(out, i.Digest)
		}
	}
	return out
}
