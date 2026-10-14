package main

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/compose"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const supportLogWarning = "This file contains clinic passwords. Share it only with the CARE support team."

func (a *App) ExportDiagnostics(passwordWarningAccepted bool) (err error) {
	defer a.logError(&err)
	if !passwordWarningAccepted {
		return fmt.Errorf("export cancelled: the clinic-password warning must be acknowledged first")
	}
	path, err := wruntime.SaveFileDialog(a.ctx, wruntime.SaveDialogOptions{
		Title: "Save CARE support logs", DefaultFilename: "care-support-logs-" + time.Now().Format("20060102-150405") + ".zip",
		Filters: []wruntime.FileFilter{{DisplayName: "ZIP archive", Pattern: "*.zip"}},
	})
	if err != nil || path == "" {
		return err
	}
	dir := a.log.Folder()
	if dir == "" {
		return fmt.Errorf("the logs folder is unavailable")
	}
	rel, err := filepath.Rel(dir, path)
	if diagnosticsPathInsideLogDir(rel, err) {
		return fmt.Errorf("save diagnostics outside the logs folder")
	}
	metadata := map[string]string{
		"app_version": a.pins.AppVersion, "engine_version": a.pins.CareVMVersion,
		"platform": runtime.GOOS + "/" + runtime.GOARCH, "captured_at": time.Now().UTC().Format(time.RFC3339),
		"warning": supportLogWarning,
	}
	lock := compose.ReadLock(a.installDir())
	metadata["care_version"] = a.pins.CAREVersion
	metadata["backend_commit"] = lock.Get("backend").Current.Commit
	metadata["frontend_commit"] = lock.Get("frontend").Current.Commit
	health := map[string]any{"engine": a.EngineStatus()}
	status, statusErr := a.engine().Status()
	health["clinic_status"] = status
	if statusErr != nil {
		health["clinic_status_error"] = statusErr.Error()
	}
	if err := writeDiagnostics(dir, filepath.Join(a.installDir(), "updates"), a.installDir(), path, metadata, health); err != nil {
		return err
	}
	a.logln("CARE support log bundle saved locally to " + path)
	return nil
}

func diagnosticsPathInsideLogDir(rel string, relErr error) bool {
	if relErr != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel)
}

type diagnosticFile struct {
	path string
	name string
	size int64
}

func writeDiagnostics(logRoot, updatesRoot, installRoot, dest string, metadata map[string]string, health any) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".care-diagnostics-*.zip")
	if err != nil {
		return err
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()
	archive := zip.NewWriter(tmp)
	meta, err := archive.Create("versions.json")
	if err != nil {
		return err
	}
	if err := json.NewEncoder(meta).Encode(metadata); err != nil {
		return err
	}
	healthFile, err := archive.Create("health.json")
	if err != nil {
		return err
	}
	if err := json.NewEncoder(healthFile).Encode(health); err != nil {
		return err
	}
	files, err := collectDiagnosticFiles(logRoot, updatesRoot, installRoot)
	if err != nil {
		_ = archive.Close()
		return err
	}
	var total int64
	for _, item := range files {
		if item.size > 0 && total+item.size > 500<<20 {
			_ = archive.Close()
			return fmt.Errorf("support log bundle exceeds the 500 MB limit; clear old logs or free disk space and retry")
		}
		f, err := os.Open(item.path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			_ = archive.Close()
			return fmt.Errorf("open support log file %s: %w", item.name, err)
		}
		info, err := f.Stat()
		if err != nil {
			_ = f.Close()
			_ = archive.Close()
			return err
		}
		if !info.Mode().IsRegular() {
			_ = f.Close()
			continue
		}
		out, err := archive.Create(item.name)
		if err == nil {
			var n int64
			n, err = io.Copy(out, f)
			total += n
		}
		_ = f.Close()
		if err != nil {
			_ = archive.Close()
			return fmt.Errorf("add support log file %s: %w", item.name, err)
		}
	}
	if err := archive.Close(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dest)
}

func collectDiagnosticFiles(logRoot, updatesRoot, installRoot string) ([]diagnosticFile, error) {
	var files []diagnosticFile
	for _, root := range []struct {
		path   string
		prefix string
	}{{logRoot, "logs"}, {updatesRoot, "updates"}} {
		err := filepath.WalkDir(root.path, func(path string, entry os.DirEntry, walkErr error) error {
			if os.IsNotExist(walkErr) {
				return nil
			}
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return nil
			}
			rel, err := filepath.Rel(root.path, path)
			if err != nil {
				return err
			}
			files = append(files, diagnosticFile{path: path, name: filepath.ToSlash(filepath.Join(root.prefix, rel)), size: info.Size()})
			return nil
		})
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
	}
	for _, name := range []string{".env", "backend.env", "frontend.env", "backend.overrides.env", "frontend.overrides.env", "secrets.env", "env-defaults.yml"} {
		path := filepath.Join(installRoot, name)
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			continue
		}
		files = append(files, diagnosticFile{path: path, name: filepath.ToSlash(filepath.Join("env", name)), size: info.Size()})
	}
	return files, nil
}
