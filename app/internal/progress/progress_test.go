package progress

import (
	"bytes"
	"io"
	"testing"
)

func TestMeterReader(t *testing.T) {
	var events []Event
	f := Func(func(e Event) { events = append(events, e) })
	m := f.Meter("images", "Loading", 10)
	m.every = 0
	if _, err := io.Copy(io.Discard, m.Reader(bytes.NewReader(make([]byte, 10)))); err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 {
		t.Fatal("no events")
	}
	last := events[len(events)-1]
	if last.Step != "images" || last.State != Active || last.Done != 10 || last.Total != 10 || last.Detail != "Loading" {
		t.Fatalf("last = %+v", last)
	}
}

func TestMeterThrottlesButReportsEnd(t *testing.T) {
	var events []Event
	f := Func(func(e Event) { events = append(events, e) })
	m := f.Meter("engine", "", 1000)
	for range 1000 {
		m.Add(1)
	}
	if len(events) > 3 {
		t.Fatalf("%d events, want throttling", len(events))
	}
	if last := events[len(events)-1]; last.Done != 1000 {
		t.Fatalf("last = %+v", last)
	}
}

func TestNilFunc(t *testing.T) {
	var f Func
	f.Start("a", "")
	f.Finish("a", "")
	f.Skip("a", "")
	f.Meter("a", "", 1).Add(1)
}
