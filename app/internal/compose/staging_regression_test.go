package compose

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/lines"
)

func TestLineUpgradeCannotOverwriteAnExistingStagedUpdate(t *testing.T) {
	b := installed(t)
	stable := lineFixture("stable", 'a')
	stable.Frontend = lineFixture("stable", 'b').Frontend
	publish(t, b, lines.Manifest{Lines: []lines.Line{stable, lineFixture("next", 'c')}})
	if _, err := b.PrepareUpdate(); err != nil {
		t.Fatal(err)
	}
	before := ReadLock(b.dir)
	trace := os.Getenv("CARE_BUILD_TRACE")
	if err := os.WriteFile(trace, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := b.StageLine("next"); err == nil || !strings.Contains(err.Error(), "already staged") {
		t.Fatalf("existing update was not rejected: %v", err)
	}
	if got := ReadLock(b.dir); !reflect.DeepEqual(got, before) {
		t.Fatalf("rejected line staging changed lock: %+v", got)
	}
	if calls := readTrace(t, trace); calls != "" {
		t.Fatalf("rejected line staging modified Docker tags: %s", calls)
	}
	if err := b.DiscardPending(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.StageLine("next"); err != nil {
		t.Fatalf("discarding old update did not permit upgrade: %v", err)
	}
}

func TestInterruptedLineStagingReturnsAnErrorWithoutPublishing(t *testing.T) {
	for _, during := range []bool{false, true} {
		t.Run(fmt.Sprint(during), func(t *testing.T) {
			b := installed(t)
			publish(t, b, lines.Manifest{Lines: []lines.Line{lineFixture("stable", 'a'), lineFixture("next", 'c')}})
			stop := !during
			b.Stop = func() bool { return stop }
			b.Log = func(string) { stop = true }
			before := ReadLock(b.dir)
			if _, err := b.StageLine("next"); err == nil || !strings.Contains(err.Error(), "interrupted") {
				t.Fatalf("interrupted stage = %v", err)
			}
			if got := ReadLock(b.dir); !reflect.DeepEqual(got, before) {
				t.Fatalf("interrupted line stage changed lock: %+v", got)
			}
		})
	}
}
