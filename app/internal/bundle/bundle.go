// Package bundle finds what the desktop app ships for the host service: the
// care-clinic-service binary and the engine bundle. In the macOS app
// (see scripts/bundle-macos.sh):
//
//	CARE Clinic.app/Contents/MacOS/care-clinic-service
//	CARE Clinic.app/Contents/Resources/engine/care-engine-<version>-arm64.tar.gz
//	CARE Clinic.app/Contents/Resources/engine/SHA256SUMS
//
// In the Windows install directory (see app/build/windows/installer):
//
//	care-clinic-service.exe
//	qemu\qemu-system-x86_64.exe (and the rest of the QEMU build)
//	engine\care-engine-<version>-amd64.tar.gz
//	engine\SHA256SUMS
//
// CARE_CLINIC_SERVICE_BINARY and CARE_CLINIC_ENGINE_BUNDLE (a tarball, with
// SHA256SUMS next to it or not) override them, e.g. for wails dev.
package bundle

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

const (
	ServiceBinaryEnv = "CARE_CLINIC_SERVICE_BINARY"
	EngineBundleEnv  = "CARE_CLINIC_ENGINE_BUNDLE"

	ServiceBinaryName = "care-clinic-service"
	sumsName          = "SHA256SUMS"
)

// ErrNoEngine means no engine bundle ships with this app.
var ErrNoEngine = errors.New("this copy of CARE Clinic has no engine bundle")

// Layout is where to look; Default reads the environment and the running
// executable.
type Layout struct {
	// ExeDir holds the app executable (Contents/MacOS, or the Windows
	// install directory).
	ExeDir string
	// OS is the layout's system, runtime.GOOS when empty.
	OS string
	// ServiceBinary and EngineBundle override the bundled files.
	ServiceBinary string
	EngineBundle  string
}

// Default is the running app's layout.
func Default() Layout {
	l := Layout{ServiceBinary: os.Getenv(ServiceBinaryEnv), EngineBundle: os.Getenv(EngineBundleEnv)}
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		l.ExeDir = filepath.Dir(exe)
	}
	return l
}

func (l Layout) windows() bool {
	if l.OS == "" {
		return runtime.GOOS == "windows"
	}
	return l.OS == "windows"
}

// EngineArch is the engine architecture this app ships: Windows runs the
// amd64 engine under QEMU, macOS the arm64 one under Virtualization.framework.
func (l Layout) EngineArch() string {
	if l.windows() {
		return "amd64"
	}
	return "arm64"
}

func (l Layout) serviceName() string {
	if l.windows() {
		return ServiceBinaryName + ".exe"
	}
	return ServiceBinaryName
}

func (l Layout) engineDir() string {
	if l.windows() {
		return filepath.Join(l.ExeDir, "engine")
	}
	return filepath.Join(l.ExeDir, "..", "Resources", "engine")
}

// ServicePath is the service binary to install.
func (l Layout) ServicePath() (string, error) {
	p := l.ServiceBinary
	if p == "" {
		if l.ExeDir == "" {
			return "", errors.New("couldn't locate the CARE Clinic app")
		}
		p = filepath.Join(l.ExeDir, l.serviceName())
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("this copy of CARE Clinic has no %s: %w", ServiceBinaryName, err)
	}
	if !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0) {
		return "", fmt.Errorf("%s is not an executable file", abs)
	}
	return abs, nil
}

// Engine is a shipped engine bundle.
type Engine struct {
	Path    string
	Version string
	SHA256  string
	Size    int64
}

var engineName = regexp.MustCompile(`^care-engine-(.+)-([a-z0-9]+)\.tar\.gz$`)

// Engine finds the engine bundle and its checksum: from SHA256SUMS next to
// it when listed there, otherwise hashed.
func (l Layout) Engine() (Engine, error) {
	path := l.EngineBundle
	if path == "" {
		if l.ExeDir == "" {
			return Engine{}, ErrNoEngine
		}
		matches, _ := filepath.Glob(filepath.Join(l.engineDir(), "care-engine-*-"+l.EngineArch()+".tar.gz"))
		if len(matches) == 0 {
			return Engine{}, ErrNoEngine
		}
		sort.Strings(matches)
		path = matches[len(matches)-1]
	}
	name := filepath.Base(path)
	m := engineName.FindStringSubmatch(name)
	if m == nil || m[2] != l.EngineArch() {
		return Engine{}, fmt.Errorf("%s is not named care-engine-<version>-%s.tar.gz", name, l.EngineArch())
	}
	info, err := os.Stat(path)
	if err != nil {
		return Engine{}, fmt.Errorf("engine bundle: %w", err)
	}
	e := Engine{Path: filepath.Clean(path), Version: m[1], Size: info.Size()}
	if e.SHA256, err = listedSum(filepath.Join(filepath.Dir(path), sumsName), name); err != nil {
		return Engine{}, err
	}
	if e.SHA256 == "" {
		if e.SHA256, err = hashFile(path); err != nil {
			return Engine{}, err
		}
	}
	return e, nil
}

// listedSum is name's sum in a sha256sum-style file, "" when not listed.
func listedSum(sums, name string) (string, error) {
	f, err := os.Open(sums)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name {
			sum := strings.ToLower(fields[0])
			if _, err := hex.DecodeString(sum); err != nil || len(sum) != 64 {
				return "", fmt.Errorf("%s: bad checksum for %s", sums, name)
			}
			return sum, nil
		}
	}
	return "", sc.Err()
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
