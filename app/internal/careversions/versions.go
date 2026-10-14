// Package careversions provides the verified image metadata for CARE 3.1.
package careversions

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/release"
)

const Version = "3.1.0"

type Image = release.ImageBuild

type Manifest struct {
	Version  string
	Backend  Image
	Frontend Image
}

func Fetch(dir string, pins *release.Pins) (*Manifest, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	m, err := release.NewClient(dir).Fetch(ctx, pins.UpdatesURL)
	if err != nil {
		return nil, err
	}
	return FromRelease(m, pins)
}

func FromRelease(m release.Manifest, pins *release.Pins) (*Manifest, error) {
	if !strings.HasPrefix(pins.CAREVersion, "3.1.") || !strings.HasPrefix(m.Care.Version, "3.1.") {
		return nil, fmt.Errorf("this app only supports CARE %s", Version)
	}
	return &Manifest{Version: m.Care.Version, Backend: m.Care.Backend, Frontend: m.Care.Frontend}, nil
}

func Load(dir string, pins *release.Pins) (*Manifest, error) {
	return Fetch(dir, pins)
}
