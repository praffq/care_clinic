package updates

import (
	"context"
	"errors"
	"fmt"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
	"github.com/ohcnetwork/care_desktop/app/internal/feed"
)

// ErrUnknownCare means a CARE stack is installed that the coordinator did
// not install and that matches no release in the feed. Updating it as a
// fresh install would replace it without a backup, so nothing is planned.
var ErrUnknownCare = errors.New("the installed CARE stack is not a release in the update feed")

// adopt records an engine and CARE stack installed without the coordinator
// (before it existed, or with ctl clinic install): the service's active
// slot, and the CARE release in m whose backend and frontend digests the
// stack's containers run. It is a no-op once installed.json knows both.
func (c *Coordinator) adopt(ctx context.Context, m *feed.Manifest, in Installed) (Installed, error) {
	if in.Engine != nil && in.Care != nil {
		return in, nil
	}
	save := func() error {
		in.UpdatedAt = c.now()
		return c.st.save(InstalledFile, in)
	}
	if in.Engine == nil {
		e, err := c.activeEngine(ctx)
		if err != nil {
			return in, err
		}
		if e != nil {
			in.Engine = e
			c.logf("Adopted the installed engine %s", e.Version)
			if err := save(); err != nil {
				return in, err
			}
		}
	}
	if in.Engine != nil && in.Care == nil {
		var st protocol.StackStatusResult
		if err := c.o.Agent.Op(ctx, protocol.StackStatus{}, &st); err != nil {
			return in, fmt.Errorf("read the installed CARE stack: %w", err)
		}
		running := map[protocol.ImageRole]protocol.Digest{}
		for _, s := range st.Services {
			switch s.Name {
			case protocol.ServiceBackend:
				running[protocol.ImageRoleBackend] = s.Image
			case protocol.ServiceFrontend:
				running[protocol.ImageRoleFrontend] = s.Image
			}
		}
		be, fe := running[protocol.ImageRoleBackend], running[protocol.ImageRoleFrontend]
		// A derived plugin image runs on top of the release's backend.
		if p := in.Plugins; p != nil && p.Image != nil && be == p.Image.Digest {
			be = p.Base.Digest
		}
		if be != "" || fe != "" {
			var match *feed.CareRelease
			for i := range m.Care {
				r := &m.Care[i]
				if r.Images[protocol.ImageRoleBackend].Digest == be && r.Images[protocol.ImageRoleFrontend].Digest == fe {
					match = r
					break
				}
			}
			if match == nil {
				return in, fmt.Errorf("%w (backend %s, frontend %s)", ErrUnknownCare, short(be), short(fe))
			}
			in.Care = &InstalledCare{Version: match.Version.String(), ReleasedAt: match.ReleasedAt, Images: careImages(match, m)}
			c.logf("Adopted the installed CARE %s", in.Care.Version)
			if err := save(); err != nil {
				return in, err
			}
		}
	}
	return in, nil
}

func (c *Coordinator) activeEngine(ctx context.Context) (*InstalledEngine, error) {
	sl, err := c.o.Service.Slots(ctx)
	if err != nil {
		return nil, fmt.Errorf("engine slots: %w", err)
	}
	for _, s := range sl.Slots {
		if sl.Current != "" && s.Version == sl.Current {
			return &InstalledEngine{Version: s.Version, DataSchema: s.DataSchema, SHA256: s.TarballSHA256}, nil
		}
	}
	return nil, nil
}

func short(d protocol.Digest) string {
	s := string(d)
	if len(s) > 19 {
		return s[:19]
	}
	if s == "" {
		return "none"
	}
	return s
}
