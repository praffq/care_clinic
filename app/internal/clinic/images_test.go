package clinic

import (
	"slices"
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/release"
)

func TestUninstallImagesIncludesPluginRollbackTag(t *testing.T) {
	e := &Clinic{Pins: &release.Pins{BackendImage: "care/backend"}}
	want := "care/backend-plugin-rollback"
	if !slices.Contains(e.uninstallImages(), want) {
		t.Fatalf("uninstall image tags do not include the plugin rollback image %q", want)
	}
}
