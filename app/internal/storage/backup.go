package storage

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	BackupFloor  = 1 * GB
	backupMargin = 256 * MB
	StaleAfter   = 26 * time.Hour
)

type BackupSet struct {
	Stamp      string    `json:"stamp"`
	At         time.Time `json:"-"`
	BaseBytes  uint64    `json:"base_bytes"`
	DumpBytes  uint64    `json:"dump_bytes"`
	FilesBytes uint64    `json:"files_bytes"`
}

func (s BackupSet) Bytes() uint64 { return s.BaseBytes + s.DumpBytes + s.FilesBytes }

var setDump = regexp.MustCompile(`^care-(\d{8}-\d{6})\.dump\.enc$`)

// LatestSet finds the newest verified-set dump (not pre-update safety copies)
// and sizes it together with its full copy and files archive.
func LatestSet(dir string) (BackupSet, bool, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return BackupSet{}, false, nil
	}
	if err != nil {
		return BackupSet{}, false, err
	}
	var set BackupSet
	found := false
	for _, en := range entries {
		m := setDump.FindStringSubmatch(en.Name())
		if m == nil || (found && m[1] <= set.Stamp) {
			continue
		}
		info, err := en.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		set, found = BackupSet{Stamp: m[1], At: info.ModTime(), DumpBytes: uint64(info.Size())}, true
	}
	if !found {
		return BackupSet{}, false, nil
	}
	if info, err := os.Lstat(filepath.Join(dir, "files-"+set.Stamp+".tar.gz.enc")); err == nil && info.Mode().IsRegular() {
		set.FilesBytes = uint64(info.Size())
	}
	if info, err := os.Lstat(filepath.Join(dir, "base-"+set.Stamp, "base.tar.gz.enc")); err == nil && info.Mode().IsRegular() {
		set.BaseBytes = uint64(info.Size())
	}
	return set, true, nil
}

// BackupNeed is the room a new set needs beside the current one: only one set
// is kept, but the previous set is deleted after the new one is verified.
func BackupNeed(set BackupSet, found bool) uint64 {
	if !found {
		return BackupFloor
	}
	return max(set.Bytes()*5/4+backupMargin, BackupFloor)
}

type BackupRun struct {
	State     string `json:"state"`
	Reason    string `json:"reason"`
	At        int64  `json:"at"`
	NeedBytes uint64 `json:"need_bytes"`
	FreeBytes uint64 `json:"free_bytes"`
	Message   string `json:"message"`
}

const StatusFile = "backup-status"

func ReadBackupRun(dir string) (BackupRun, error) {
	f, err := os.Open(filepath.Join(dir, StatusFile))
	if os.IsNotExist(err) {
		return BackupRun{}, nil
	}
	if err != nil {
		return BackupRun{}, err
	}
	defer func() { _ = f.Close() }()
	var run BackupRun
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		switch k {
		case "state":
			run.State = v
		case "reason":
			run.Reason = v
		case "at":
			run.At, _ = strconv.ParseInt(v, 10, 64)
		case "need_kb":
			n, _ := strconv.ParseUint(v, 10, 64)
			run.NeedBytes = n << 10
		case "free_kb":
			n, _ := strconv.ParseUint(v, 10, 64)
			run.FreeBytes = n << 10
		case "message":
			run.Message = v
		}
	}
	return run, sc.Err()
}
