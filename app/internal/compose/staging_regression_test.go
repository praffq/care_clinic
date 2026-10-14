package compose

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestInterruptedUpdateDoesNotPublishAnImage(t *testing.T) {
	b := installed(t)
	publish(t, b, versionFixture('b'))
	stop := false
	b.Stop = func() bool { return stop }
	b.Log = func(string) { stop = true }
	before := ReadLock(b.dir)
	if _, err := b.PrepareUpdate(); err == nil || !strings.Contains(err.Error(), "interrupted") {
		t.Fatalf("interrupted update = %v", err)
	}
	if got := ReadLock(b.dir); !reflect.DeepEqual(got, before) {
		t.Fatalf("interrupted update changed lock: %+v", got)
	}
}

func TestPendingImagesReapplyLatestPluginsAndEmptyFrontendOverrides(t *testing.T) {
	b := installed(t)
	publish(t, b, versionFixture('b'))
	if update, err := b.PrepareUpdate(); err != nil || !update.Any() {
		t.Fatalf("prepare update: %+v %v", update, err)
	}
	before := ReadLock(b.dir)
	pending := b.Pending()
	oldBackend, oldFrontend := key(t, pending.backendPlan), key(t, pending.frontendPlan)
	writeBuildFile(t, b.dir, "backend.env", `ADDITIONAL_PLUGS='[{"name":"latest","package_name":"pkg_latest"}]'`+"\n")
	writeBuildFile(t, b.dir, "frontend.env", "REACT_MFE_REGISTERED_COMPONENTS=\nREACT_APP_TITLE='Latest Clinic'\nREACT_CARE_API_URL=https://latest.local\n")
	if key(t, pending.backendPlan) == oldBackend || key(t, pending.frontendPlan) == oldFrontend {
		t.Fatal("staged release ignored settings changed after download")
	}
	if err := pending.Parallel(EnsureBackend, EnsureFrontend); err != nil {
		t.Fatal(err)
	}
	if after := ReadLock(b.dir); !reflect.DeepEqual(after, before) {
		t.Fatal("reapplying overrides activated the pending release")
	}
	trace := readTrace(t, os.Getenv("CARE_BUILD_TRACE"))
	for _, want := range []string{"pkg_latest", digestRef("backend-builder", 'b'), digestRef("frontend-builder", 'b'), "care:clinic-next", "care_fe:clinic-next"} {
		if !strings.Contains(trace, want) {
			t.Fatalf("staged customization did not consume %s", want)
		}
	}
	var compiled map[string]string
	if err := json.Unmarshal([]byte(readTrace(t, os.Getenv("CARE_BUILD_TRACE")+".json")), &compiled); err != nil {
		t.Fatal(err)
	}
	if value, ok := compiled["REACT_MFE_REGISTERED_COMPONENTS"]; !ok || value != "" || compiled["REACT_APP_TITLE"] != "Latest Clinic" {
		t.Fatal("staged frontend did not retain exact latest empty/compiled values")
	}
}
