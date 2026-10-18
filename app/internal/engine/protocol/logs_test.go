package protocol

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestLogsSystemValidate(t *testing.T) {
	ok := LogsSystem{Cursors: map[string]FileCursor{LogSourceMessages: {Inode: 4, Offset: 10}}, TailBytes: 4096}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, op := range map[string]LogsSystem{
		"cursors.journal":       {Cursors: map[string]FileCursor{"journal": {}}},
		"cursors.docker.offset": {Cursors: map[string]FileCursor{LogSourceDocker: {Offset: -1}}},
		"tail_bytes":            {TailBytes: MaxLogTailBytes + 1},
	} {
		var ve *ValidationError
		if err := op.Validate(); !errors.As(err, &ve) || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestLogsContainersValidate(t *testing.T) {
	ok := LogsContainers{Since: map[string]time.Time{"care-backend-1": time.Now()}, TailLines: 200}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []LogsContainers{
		{Since: map[string]time.Time{"../etc": {}}},
		{Since: map[string]time.Time{"-x": {}}},
		{Since: map[string]time.Time{"a/b": {}}},
		{TailLines: MaxTailLines + 1},
		{TailLines: -1},
	} {
		if err := bad.Validate(); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
	many := LogsContainers{Since: map[string]time.Time{}}
	for i := range MaxLogContainers + 1 {
		many.Since[fmt.Sprintf("c%d", i)] = time.Time{}
	}
	if err := many.Validate(); err == nil {
		t.Error("too many containers accepted")
	}
}

func TestFileCursorRoundTrip(t *testing.T) {
	c := FileCursor{Inode: 1234, Offset: 99}
	got, ok := ParseFileCursor(c.String())
	if !ok || got != c {
		t.Fatalf("ParseFileCursor(%q) = %+v %v", c.String(), got, ok)
	}
	for _, bad := range []string{"", "1", "a:1", "1:-2"} {
		if _, ok := ParseFileCursor(bad); ok {
			t.Errorf("ParseFileCursor(%q) accepted", bad)
		}
	}
}

func TestDecodeLogOps(t *testing.T) {
	op, err := DecodeOp(OpLogsContainers, strings.NewReader(`{"since":{"care-db-1":"2026-01-02T03:04:05.123456789Z"},"tail_lines":10}`))
	if err != nil {
		t.Fatal(err)
	}
	lc := op.(*LogsContainers)
	if lc.Since["care-db-1"].Nanosecond() != 123456789 || lc.TailLines != 10 {
		t.Fatalf("decoded %+v", lc)
	}
	if _, err := DecodeOp(OpLogsSystem, strings.NewReader(`{"cursors":{"messages":{"inode":1,"offset":2}},"follow":true}`)); err == nil {
		t.Fatal("unknown field accepted")
	}
}
