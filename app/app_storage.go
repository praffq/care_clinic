package main

import (
	"context"
	"fmt"

	"github.com/ohcnetwork/care_desktop/app/internal/service/api"
	"github.com/ohcnetwork/care_desktop/app/internal/sys/diskspace"
)

const gb = uint64(1) << 30

// Free space below which the clinic is warned (low) and at risk (critical):
// Postgres and MinIO stop writing on a full disk.
const (
	lowFree      = 10 * gb
	criticalFree = 5 * gb
)

// Storage levels, worst last.
const (
	storageOK       = "ok"
	storageUnknown  = "unknown"
	storageLow      = "low"
	storageCritical = "critical"
)

func storageRank(level string) int {
	switch level {
	case storageCritical:
		return 3
	case storageLow:
		return 2
	case storageUnknown:
		return 1
	}
	return 0
}

func freeLevel(free uint64) string {
	switch {
	case free < criticalFree:
		return storageCritical
	case free < lowFree:
		return storageLow
	}
	return storageOK
}

// DiskUsage is one disk the clinic depends on.
type DiskUsage struct {
	Kind   string `json:"kind"` // "engine" or "backups"
	Name   string `json:"name"`
	Path   string `json:"path,omitempty"`
	Free   uint64 `json:"free"`
	Total  uint64 `json:"total"`
	Level  string `json:"level"`
	Detail string `json:"detail,omitempty"`
}

// StorageState is the Storage card: the engine's data disk (clinic
// database, files and images) and the host folder backups are copied to.
type StorageState struct {
	Level string      `json:"level"`
	Title string      `json:"title"`
	Disks []DiskUsage `json:"disks"`
}

// StorageStatus reads free space on the engine's data disk and the backup
// folder's volume.
func (a *App) StorageStatus() StorageState {
	ctx, cancel := context.WithTimeout(a.clientContext(), serviceCallTimeout)
	defer cancel()
	st, err := a.svc().Status(ctx)
	if err != nil {
		return storageState(nil, err, diskspace.Of)
	}
	return storageState(&st, nil, diskspace.Of)
}

func storageState(st *api.ServiceStatus, err error, stat func(string) (diskspace.Usage, error)) StorageState {
	engine := DiskUsage{Kind: "engine", Name: "Clinic data (engine disk)", Level: storageUnknown}
	switch {
	case err != nil:
		engine.Detail = "Couldn't reach the CARE Clinic service: " + err.Error()
	case st.Engine.Agent == nil || st.Engine.Agent.DiskTotalBytes == 0:
		engine.Detail = "Shown while the engine is running."
	default:
		engine.Free, engine.Total = st.Engine.Agent.DiskFreeBytes, st.Engine.Agent.DiskTotalBytes
		engine.Level = freeLevel(engine.Free)
	}
	disks := []DiskUsage{engine}
	if st != nil && st.Backups != nil && st.Backups.Dir != "" {
		b := DiskUsage{Kind: "backups", Name: "Backup folder", Path: st.Backups.Dir, Level: storageUnknown}
		if u, err := stat(st.Backups.Dir); err != nil {
			b.Detail = "Couldn't read free space: " + err.Error()
		} else {
			b.Free, b.Total, b.Level = u.Free, u.Total, freeLevel(u.Free)
		}
		disks = append(disks, b)
	}
	s := StorageState{Level: storageOK, Disks: disks}
	for _, d := range disks {
		if storageRank(d.Level) > storageRank(s.Level) {
			s.Level = d.Level
		}
	}
	s.Title = storageTitle(s.Level, disks)
	return s
}

func storageTitle(level string, disks []DiskUsage) string {
	var worst DiskUsage
	for _, d := range disks {
		if d.Level == level {
			worst = d
			break
		}
	}
	switch level {
	case storageCritical:
		return fmt.Sprintf("%s is almost full (%s free). Free up space now: CARE stops saving records when it runs out.", worst.Name, humanBytes(worst.Free))
	case storageLow:
		return fmt.Sprintf("%s is running low (%s free).", worst.Name, humanBytes(worst.Free))
	case storageUnknown:
		return "Some disk usage isn't available right now."
	}
	return "Enough free space."
}

// lowDiskNote is the panel summary's free-space warning for the engine disk.
func lowDiskNote(e api.EngineStatus) (string, bool) {
	if e.Agent == nil || e.Agent.DiskTotalBytes == 0 {
		return "", false
	}
	switch freeLevel(e.Agent.DiskFreeBytes) {
	case storageCritical:
		return fmt.Sprintf("The clinic's disk is almost full (%s free). See Storage on the Overview tab.", humanBytes(e.Agent.DiskFreeBytes)), true
	case storageLow:
		return fmt.Sprintf("The clinic's disk is running low (%s free).", humanBytes(e.Agent.DiskFreeBytes)), true
	}
	return "", false
}

func humanBytes(b uint64) string {
	switch {
	case b >= 10*gb:
		return fmt.Sprintf("%.0f GB", float64(b)/float64(gb))
	case b >= gb:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(gb))
	case b >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(b)/float64(1<<20))
	}
	return fmt.Sprintf("%d bytes", b)
}
