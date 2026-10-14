package compose

import (
	"reflect"
	"strings"
	"testing"
)

func TestInterruptedUpdateDoesNotPublishAnImage(t *testing.T) {
	b := installed(t)
	publish(t, b, versionFixture('b'))
	b.Log = func(string) { b.Stop = func() bool { return true } }
	before := ReadLock(b.dir)
	if _, err := b.PrepareUpdate(); err == nil || !strings.Contains(err.Error(), "interrupted") {
		t.Fatalf("interrupted update = %v", err)
	}
	if got := ReadLock(b.dir); !reflect.DeepEqual(got, before) {
		t.Fatalf("interrupted update changed lock: %+v", got)
	}
}

func TestRenderEnvIsStableAndQuoted(t *testing.T) {
	got := renderEnv(map[string]string{"B": "it's", "A": "x y", "C": `a"b'c`})
	want := "A='x y'\nB=\"it's\"\nC=`a\"b'c`\n"
	if got != want {
		t.Fatalf("renderEnv = %q, want %q", got, want)
	}
}
