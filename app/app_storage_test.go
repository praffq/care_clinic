package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/engine/protocol"
	"github.com/ohcnetwork/care_desktop/app/internal/service/api"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/diskspace"
)

func TestStorageState(t *testing.T) {
	stat := func(free uint64) func(string) (diskspace.Usage, error) {
		return func(string) (diskspace.Usage, error) { return diskspace.Usage{Free: free, Total: 500 * gb}, nil }
	}
	withAgent := func(free uint64, backups bool) *api.ServiceStatus {
		st := &api.ServiceStatus{Engine: api.EngineStatus{Agent: &protocol.Status{DiskFreeBytes: free, DiskTotalBytes: 100 * gb}}}
		if backups {
			st.Backups = &api.BackupStatus{Dir: "/backups"}
		}
		return st
	}
	cases := []struct {
		name  string
		st    *api.ServiceStatus
		err   error
		stat  func(string) (diskspace.Usage, error)
		level string
		disks int
		title string
	}{
		{"plenty", withAgent(50*gb, true), nil, stat(200 * gb), storageOK, 2, "Enough free space."},
		{"engine low", withAgent(8*gb, true), nil, stat(200 * gb), storageLow, 2, "Clinic data (engine disk) is running low (8.0 GB free)."},
		{"backups critical", withAgent(50*gb, true), nil, stat(1 * gb), storageCritical, 2, "Backup folder is almost full"},
		{"no backups dir", withAgent(50*gb, false), nil, stat(1 * gb), storageOK, 1, "Enough"},
		{"engine off", &api.ServiceStatus{}, nil, stat(200 * gb), storageUnknown, 1, "isn't available"},
		{"unreachable", nil, errors.New("dial: refused"), nil, storageUnknown, 1, "isn't available"},
		{"stat fails", withAgent(50*gb, true), nil, func(string) (diskspace.Usage, error) { return diskspace.Usage{}, errors.New("denied") }, storageUnknown, 2, "isn't available"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := storageState(c.st, c.err, c.stat)
			if s.Level != c.level || len(s.Disks) != c.disks || !strings.Contains(s.Title, c.title) {
				t.Fatalf("got %+v", s)
			}
		})
	}
}

func TestSummaryWarnsOnLowEngineDisk(t *testing.T) {
	st := &api.ServiceStatus{Engine: api.EngineStatus{
		State: api.StateReady, Stack: &api.StackStatus{State: api.StackRunning},
		Agent: &protocol.Status{DiskFreeBytes: 3 * gb, DiskTotalBytes: 100 * gb},
	}}
	sum := serviceState(true, st, nil).Summary
	if sum.Level != levelWarn || len(sum.Notes) != 1 || !strings.Contains(sum.Notes[0], "almost full") {
		t.Fatalf("summary = %+v", sum)
	}
	st.Engine.Agent.DiskFreeBytes = 40 * gb
	if sum := serviceState(true, st, nil).Summary; sum.Level != levelOK || len(sum.Notes) != 0 {
		t.Fatalf("summary = %+v", sum)
	}
}

func TestCheckCareHealth(t *testing.T) {
	code := http.StatusOK
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ping/" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(code)
	}))
	defer srv.Close()
	a := settingsApp(t)
	a.carePingURL = srv.URL + "/ping/"

	h := a.CheckCareHealth()
	if !h.Healthy || h.Code != 200 || h.ServicesError != "" || len(h.Services) != 1 ||
		h.Services[0] != (ServiceHealth{Name: "backend", State: "running"}) {
		t.Fatalf("health = %+v", h)
	}

	code = http.StatusBadGateway
	if h := a.CheckCareHealth(); h.Healthy || h.Code != 502 || !strings.Contains(h.Detail, "502") {
		t.Fatalf("health = %+v", h)
	}

	srv.Close()
	if h := a.CheckCareHealth(); h.Healthy || h.Code != 0 || h.Detail != "CARE isn't answering on this computer" {
		t.Fatalf("health = %+v", h)
	}
}
