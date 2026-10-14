package compose

import (
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
