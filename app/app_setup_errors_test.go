package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
	"github.com/ohcnetwork/care_desktop/app/internal/feed"
	"github.com/ohcnetwork/care_desktop/app/internal/stack"
	"github.com/ohcnetwork/care_desktop/app/internal/updates"
)

func TestSetupErrorMessages(t *testing.T) {
	frontend := stack.Image{Role: protocol.ImageRoleFrontend, Repo: "ghcr.io/ohcnetwork/care_fe", Digest: protocol.Digest("sha256:0123")}
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"feed down", fmt.Errorf("setup: %w: fetch update feed: GET https://x/updates.json: 503 Service Unavailable", updates.ErrFeedUnreachable),
			"Couldn't reach the update server. Check the internet connection and try again."},
		{"no compatible release", &feed.NoCompatibleError{Reason: "CARE 3.1.0 needs app ≥ 0.3.0 (this app is 0.2.0); update the app first."},
			"CARE 3.1.0 needs app ≥ 0.3.0 (this app is 0.2.0); update the app first."},
		{"low disk", &updates.DiskSpaceError{Free: 1 << 30, Need: 5 << 30},
			"There isn't enough free space to download CARE (1.0 GB free, 5.0 GB needed). Free up space on this computer, then try again."},
		{"image network failure", &stack.ImageError{Image: frontend, Err: errors.New("dial tcp: lookup ghcr.io: no such host")},
			"Couldn't download the CARE web app image. Check the internet connection and try again; finished downloads are kept."},
		{"image missing", &stack.ImageError{Image: frontend, Err: errors.New("manifest unknown")},
			"The CARE web app image (ghcr.io/ohcnetwork/care_fe@sha256:0123) isn't available from the image registry. Trying again won't help; update the app or ask your support contact."},
		{"digest mismatch", &stack.ImageError{Image: frontend, Err: &protocol.OpError{Code: protocol.CodeDigestMismatch, Message: "pulled image does not carry digest"}},
			"The CARE web app image (ghcr.io/ohcnetwork/care_fe@sha256:0123) isn't available from the image registry. Trying again won't help; update the app or ask your support contact."},
		{"feed rejected", fmt.Errorf("update feed rejected: %w", feed.ErrBadSignature),
			"CARE Clinic couldn't verify the update server's list of releases. Try again later, or share the log file with your support contact."},
		{"anything else", errors.New("migrations failed"), ""},
	}
	for _, tc := range cases {
		if got := setupErrorMessage(tc.err); got != tc.want {
			t.Errorf("%s: %q\nwant %q", tc.name, got, tc.want)
		}
	}
}
