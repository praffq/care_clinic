package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/sys/diskspace"
)

func TestDiskStatusFailsClosedWhenMeasurementFails(t *testing.T) {
	failure := errors.New("device is unavailable")
	tests := []struct {
		name       string
		setupDone  bool
		failOnPath string
	}{
		{name: "docker data drive", failOnPath: "docker"},
		{name: "settings drive", failOnPath: "install"},
		{name: "running clinic data drive", setupDone: true, failOnPath: "docker"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			measure := func(path string) (diskspace.Usage, error) {
				if path == tc.failOnPath {
					return diskspace.Usage{}, failure
				}
				return diskspace.Usage{Path: path, Free: 1 << 40, Total: 1 << 41, Volume: path}, nil
			}
			got := diskStatus("install", "docker", tc.setupDone, measure)
			if got.OK {
				t.Fatal("measurement failure was reported as enough free space")
			}
			if !strings.Contains(got.Message, failure.Error()) {
				t.Fatalf("measurement failure was not explained: %+v", got)
			}
		})
	}
}
