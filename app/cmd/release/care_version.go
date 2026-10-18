package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
	"github.com/ohcnetwork/care_desktop/app/internal/feed"
)

// careReleaseVersion is <core>+be.<rev>.fe.<rev>, plus .pl.<hash> when the release
// carries plugins, so a plugin-only change is a distinct version.
func careReleaseVersion(core string, c *CareArtifacts) string {
	v := fmt.Sprintf("%s+be.%s.fe.%s", core, c.BackendRev, c.FrontendRev)
	if h := pluginsHash(c.Sources); h != "" {
		v += ".pl." + h
	}
	return v
}

// pluginsHash is the first 8 hex digits of SHA-256 over the sorted
// "id=rev\n" lines of the plugins, or "" without plugins.
func pluginsHash(s *feed.CareSources) string {
	if s == nil || len(s.Plugins) == 0 {
		return ""
	}
	h := sha256.New()
	for _, id := range slices.Sorted(maps.Keys(s.Plugins)) {
		_, _ = fmt.Fprintf(h, "%s=%s\n", id, s.Plugins[id].Rev)
	}
	return hex.EncodeToString(h.Sum(nil))[:8]
}

// pinImages embeds each image's digest in its ref (see pinRef).
func pinImages(path string, in map[protocol.ImageRole]feed.ImageRef) (map[protocol.ImageRole]feed.ImageRef, error) {
	if in == nil {
		return nil, nil
	}
	out := make(map[protocol.ImageRole]feed.ImageRef, len(in))
	for role, img := range in {
		ref, err := pinRef(img.Ref, img.Digest)
		if err != nil {
			return nil, fmt.Errorf("artifacts %s.%s: %w", path, role, err)
		}
		img.Ref = ref
		out[role] = img
	}
	return out, nil
}
