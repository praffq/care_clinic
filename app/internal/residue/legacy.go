package residue

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/ohcnetwork/care_desktop/app/internal/sys/proc"
)

const LegacyProject = "care-clinic"

var legacyImagePrefixes = []string{"ghcr.io/ohcnetwork/care"}

type dockerResource struct {
	id, label, one, many string
	query, remove        []string
}

func dockerResources() []dockerResource {
	label := "label=com.docker.compose.project=" + LegacyProject
	return []dockerResource{
		{"containers", "Clinic containers", "container", "containers", []string{"ps", "-aq", "--filter", label}, []string{"rm", "-f"}},
		{"volumes", "Old clinic data", "data volume", "data volumes", []string{"volume", "ls", "-q", "--filter", label}, []string{"volume", "rm", "-f"}},
		{"networks", "Clinic network", "network", "networks", []string{"network", "ls", "-q", "--filter", label}, []string{"network", "rm"}},
	}
}

func dockerBin() string {
	if p, err := exec.LookPath("docker"); err == nil {
		return p
	}
	if runtime.GOOS != "windows" {
		if home, err := os.UserHomeDir(); err == nil {
			p := filepath.Join(home, ".rd", "bin", "docker")
			if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() {
				return p
			}
		}
	}
	return ""
}

func dockerAnswering(run proc.Runner, bin string) bool {
	_, err := run.Capture(bin, "version", "--format", "{{.Server.Version}}")
	return err == nil
}

func dockerTraces(run proc.Runner) ([]Trace, error) {
	bin := dockerBin()
	if bin == "" || !dockerAnswering(run, bin) {
		return nil, nil
	}
	var traces []Trace
	var failed []error
	for _, r := range dockerResources() {
		ids, err := run.Lines(bin, r.query...)
		if err != nil {
			failed = append(failed, fmt.Errorf("could not inspect the earlier clinic's %s in Docker: %w", r.many, err))
		} else if len(ids) > 0 {
			traces = append(traces, Trace{ID: r.id, Label: r.label, Detail: plural(len(ids), r.one, r.many) + " from an earlier install in Docker"})
		}
	}
	images, err := legacyImages(run, bin)
	if err != nil {
		failed = append(failed, err)
	} else if len(images) > 0 {
		traces = append(traces, Trace{ID: "images", Label: "Clinic images", Detail: plural(len(images), "Docker image", "Docker images") + " from an earlier install"})
	}
	return traces, errors.Join(failed...)
}

func legacyImages(run proc.Runner, bin string) ([]string, error) {
	lines, err := run.Lines(bin, "images", "--format", "{{.Repository}}:{{.Tag}}")
	if err != nil {
		return nil, fmt.Errorf("could not inspect Docker images: %w", err)
	}
	var found []string
	for _, line := range lines {
		if strings.HasSuffix(line, ":<none>") {
			continue
		}
		if slices.ContainsFunc(legacyImagePrefixes, func(p string) bool { return strings.HasPrefix(line, p) }) {
			found = append(found, line)
		}
	}
	return found, nil
}

func RemoveDocker(run proc.Runner, log func(string)) error {
	bin := dockerBin()
	if bin == "" || !dockerAnswering(run, bin) {
		return nil
	}
	var failed []error
	for _, r := range dockerResources() {
		ids, err := run.Lines(bin, r.query...)
		if err != nil {
			failed = append(failed, fmt.Errorf("could not inspect the earlier clinic's %s in Docker: %w", r.many, err))
			continue
		}
		if len(ids) == 0 {
			continue
		}
		logln(log, "Removing the earlier clinic's Docker "+r.many+"...")
		if _, err := run.Capture(bin, append(r.remove, ids...)...); err != nil {
			failed = append(failed, fmt.Errorf("could not remove the earlier clinic's Docker %s: %w", r.many, err))
		}
	}
	images, err := legacyImages(run, bin)
	if err != nil {
		failed = append(failed, err)
	}
	for _, image := range images {
		if _, err := run.Capture(bin, "image", "rm", image); err != nil {
			logln(log, "Kept the Docker image "+image+": "+err.Error())
			continue
		}
		logln(log, "Removed the Docker image "+image)
	}
	return errors.Join(failed...)
}

const (
	autostartLabel = "ohc.care-clinic"
	autostartName  = "CARE Clinic"
	autostartKey   = `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`
)

func autostartFile() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "LaunchAgents", autostartLabel+".plist")
	case "linux":
		return filepath.Join(home, ".config", "autostart", "care-clinic.desktop")
	}
	return ""
}

func AutostartEnabled() bool {
	if runtime.GOOS == "windows" {
		return proc.Command("reg", "query", autostartKey, "/v", autostartName).Run() == nil
	}
	if p := autostartFile(); p != "" {
		_, err := os.Stat(p)
		return err == nil
	}
	return false
}

func RemoveAutostart() error {
	if runtime.GOOS == "windows" {
		if !AutostartEnabled() {
			return nil
		}
		return proc.Command("reg", "delete", autostartKey, "/v", autostartName, "/f").Run()
	}
	p := autostartFile()
	if p == "" {
		return nil
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func RemoveSecrets(stateDir string) error {
	if stateDir == "" {
		return nil
	}
	if err := os.Remove(filepath.Join(stateDir, SecretsFile)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func logln(log func(string), s string) {
	if log != nil {
		log(s)
	}
}
