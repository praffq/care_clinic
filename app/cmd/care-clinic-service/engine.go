//go:build unix

package main

import (
	"context"
	"errors"

	"github.com/ohcnetwork/care_desktop/app/internal/service/control"
)

const engineUsage = `usage: care-clinic-service ctl [--dev] engine <command> [flags]

commands:
  prune              remove unused docker images in the engine VM to free
                     space in its disk. Keeps the images the clinic runs, the
                     ones before the last CARE or plugins update (rollback),
                     the CARE plugin builder and any image a container uses.
                     Downloaded images of an update not applied yet are
                     downloaded again when it is applied.

flags: as for ctl update (--desktop-version is required)
`

var errEngineUsage = errors.New("engine usage")

func engineCtl(ctx context.Context, c *control.Client, args []string) (any, error) {
	if len(args) == 0 || args[0] != "prune" {
		return nil, errEngineUsage
	}
	f := newCoordFlags("engine prune")
	if err := f.fs.Parse(args[1:]); err != nil || f.fs.NArg() > 0 || *f.desktop == "" {
		return nil, errEngineUsage
	}
	u, err := f.open(ctx, c)
	if err != nil {
		return nil, err
	}
	return u.PruneImages(ctx)
}
