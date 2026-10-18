// Package install embeds the deployment kit (the repo's deployments/) as
// app/frontend/scripts/stage-install.mjs stages it at build time, so
// care-clinic-service's ctl renders the kit it was built with rather than a
// copy on disk. The desktop app embeds the same directory itself (main.go).
// On a clean checkout only this file and .gitkeep are here: see Kit.
package install

import (
	"embed"
	"errors"
	"io/fs"
)

//go:embed all:*
var files embed.FS

// ErrNotStaged is Kit's error when the binary was built without staging the
// kit first.
var ErrNotStaged = errors.New("no deployment kit was staged into app/install at build time")

// Kit returns the embedded deployment kit.
func Kit() (fs.FS, error) {
	if _, err := fs.Stat(files, "docker-compose.yml"); err != nil {
		return nil, ErrNotStaged
	}
	return files, nil
}
