// Command bundle-windows stages what setup.exe ships next to
// "CARE Clinic.exe". Wails runs it after `wails build` on Windows
// (postBuildHooks in app/wails.json); project.nsi installs the staged
// directory, build\bin\payload, into the install folder:
//
//	care-clinic-service.exe             the service; onboarding installs it
//	qemu\qemu-system-x86_64.exe, ...    QEMU, pinned in versions.yml
//	engine\care-engine-<v>-amd64.tar.gz  optional
//	engine\SHA256SUMS                   optional
//
// Usage: bundle-windows <app executable> [windows/amd64]
//
// Inputs from the environment:
//
//	CARE_CLINIC_SERVICE_BINARY  a prebuilt service to ship instead of building one
//	CARE_CLINIC_QEMU_DIR        an unpacked QEMU to ship instead of downloading
//	                            tools.qemu_windows_x86_64 (resumable, sha256
//	                            checked, cached in dist/cache) and unpacking
//	                            it with 7-Zip
//	CARE_CLINIC_ENGINE_BUNDLE   an engine tarball to ship; its SHA256SUMS line
//	                            is taken from a SHA256SUMS next to it, or
//	                            computed. Without it, the newest
//	                            dist/care-engine-*-amd64.tar.gz is used, if any.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/ohcnetwork/care_desktop/app/internal/fetch"
	"github.com/ohcnetwork/care_desktop/app/internal/versions"
)

const (
	toolName   = "qemu_windows_x86_64"
	serviceExe = "care-clinic-service.exe"
)

func main() {
	if len(os.Args) < 2 || len(os.Args) > 3 {
		fmt.Fprintln(os.Stderr, "usage: bundle-windows <app executable> [windows/amd64]")
		os.Exit(2)
	}
	if len(os.Args) == 3 && !strings.HasPrefix(os.Args[2], "windows/") {
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	app, err := filepath.Abs(os.Args[1])
	if err == nil {
		err = run(ctx, app)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "bundle-windows:", err)
		os.Exit(1)
	}
}

// repoRoot is the checkout above the working directory (Wails runs hooks
// in app\build\bin).
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "versions.yml")); err == nil {
			return d, nil
		}
		if filepath.Dir(d) == d {
			return "", fmt.Errorf("no versions.yml above %s", dir)
		}
	}
}

func run(ctx context.Context, app string) error {
	repo, err := repoRoot()
	if err != nil {
		return err
	}
	payload := filepath.Join(filepath.Dir(app), "payload")
	if err := os.RemoveAll(payload); err != nil {
		return err
	}
	if err := os.MkdirAll(payload, 0o755); err != nil {
		return err
	}
	if err := stageService(ctx, filepath.Join(repo, "app"), filepath.Join(payload, serviceExe)); err != nil {
		return err
	}
	qemu := os.Getenv("CARE_CLINIC_QEMU_DIR")
	if qemu == "" {
		cache := filepath.Join(repo, "dist", "cache")
		if qemu, err = fetchQEMU(ctx, filepath.Join(repo, "versions.yml"), cache, ""); err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(qemu) }()
	}
	n, err := copySubset(qemu, filepath.Join(payload, "qemu"))
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(payload, "qemu", "qemu-system-x86_64.exe")); err != nil {
		return fmt.Errorf("%s has no qemu-system-x86_64.exe", qemu)
	}
	fmt.Printf("bundle-windows: shipped QEMU (%d files)\n", n)
	engine := os.Getenv("CARE_CLINIC_ENGINE_BUNDLE")
	if engine == "" {
		engine = newestEngine(filepath.Join(repo, "dist"))
	}
	if engine == "" {
		fmt.Println("bundle-windows: no engine bundle shipped (set CARE_CLINIC_ENGINE_BUNDLE or build one into dist/)")
		return nil
	}
	if err := stageEngine(engine, filepath.Join(payload, "engine")); err != nil {
		return err
	}
	fmt.Printf("bundle-windows: shipped %s\n", filepath.Base(engine))
	return nil
}

func stageService(ctx context.Context, appDir, dst string) error {
	if bin := os.Getenv("CARE_CLINIC_SERVICE_BINARY"); bin != "" {
		return copyFile(bin, dst)
	}
	cmd := exec.CommandContext(ctx, "go", "build", "-trimpath", "-ldflags", "-s -w", "-o", dst, "./cmd/care-clinic-service")
	cmd.Dir = appDir
	cmd.Env = append(os.Environ(), "GOOS=windows", "GOARCH=amd64", "CGO_ENABLED=0")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("build the service: %w", err)
	}
	return nil
}

var engineName = regexp.MustCompile(`^care-engine-.+-amd64\.tar\.gz$`)

func newestEngine(dist string) string {
	matches, _ := filepath.Glob(filepath.Join(dist, "care-engine-*-amd64.tar.gz"))
	sort.Strings(matches)
	if len(matches) == 0 {
		return ""
	}
	return matches[len(matches)-1]
}

// stageEngine copies an amd64 engine tarball and writes its SHA256SUMS
// line, taken from a SHA256SUMS beside it when it lists the file.
func stageEngine(engine, dir string) error {
	name := filepath.Base(engine)
	if !engineName.MatchString(name) {
		return fmt.Errorf("%s is not an amd64 engine bundle (care-engine-<version>-amd64.tar.gz)", engine)
	}
	if err := copyFile(engine, filepath.Join(dir, name)); err != nil {
		return err
	}
	sum := sumFromFile(filepath.Join(filepath.Dir(engine), "SHA256SUMS"), name)
	if sum == "" {
		var err error
		if sum, err = hashFile(engine); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(sum+"  "+name+"\n"), 0o644)
}

func sumFromFile(sums, name string) string {
	data, err := os.ReadFile(sums)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && (f[1] == name || f[1] == "*"+name) {
			return strings.ToLower(f[0])
		}
	}
	return ""
}

func hashFile(p string) (string, error) {
	f, err := os.Open(p)
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

// fetchQEMU downloads the pinned QEMU installer into cache and unpacks it
// into a new directory under cache, which the caller removes.
func fetchQEMU(ctx context.Context, versionsPath, cache, sevenZip string) (string, error) {
	vf, err := versions.Load(versionsPath)
	if err != nil {
		return "", err
	}
	tool, ok := vf.Tools[toolName]
	if !ok {
		return "", fmt.Errorf("%s: no tools.%s", versionsPath, toolName)
	}
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return "", err
	}
	installer := filepath.Join(cache, path.Base(tool.URL))
	fmt.Printf("bundle-windows: QEMU %s from %s\n", tool.Version, tool.URL)
	if err := fetch.File(ctx, nil, tool.URL, tool.SHA256, installer); err != nil {
		return "", err
	}
	if sevenZip == "" {
		if sevenZip, err = find7z(); err != nil {
			return "", err
		}
	}
	tmp, err := os.MkdirTemp(cache, "qemu-x-")
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, sevenZip, "x", "-y", "-bso0", "-bsp0", "-o"+tmp, installer)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		_ = os.RemoveAll(tmp)
		return "", fmt.Errorf("unpack %s: %w", installer, err)
	}
	return tmp, nil
}

func find7z() (string, error) {
	for _, name := range []string{"7z", "7zz"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	if pf := os.Getenv("ProgramFiles"); pf != "" {
		p := filepath.Join(pf, "7-Zip", "7z.exe")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("7-Zip not found: install it or pass -7z")
}

// keep reports whether the unpacked file rel (slash-separated) is needed to
// run the engine: the x86_64 system emulator and its DLLs, the x86 BIOS and
// option ROMs it loads, and the licences. The full build is about 1.1 GB;
// this is about an eighth of it.
func keep(rel string) bool {
	dir, name := path.Split(rel)
	lower := strings.ToLower(name)
	switch dir {
	case "":
		return lower == "qemu-system-x86_64.exe" ||
			strings.HasSuffix(lower, ".dll") ||
			strings.HasPrefix(lower, "copying") ||
			lower == "version" || lower == "readme.rst"
	case "share/":
		return strings.HasSuffix(lower, ".bin") || strings.HasSuffix(lower, ".rom")
	}
	return false
}

func copySubset(src, dst string) (int, error) {
	n := 0
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if !keep(filepath.ToSlash(rel)) {
			return nil
		}
		n++
		return copyFile(p, filepath.Join(dst, rel))
	})
	return n, err
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
