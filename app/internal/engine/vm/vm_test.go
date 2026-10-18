package vm

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validConfig() Config {
	return Config{BundleDir: "/b", DataDisk: "/d.img", ConsoleLog: "/c.log"}.WithDefaults()
}

func TestWithDefaults(t *testing.T) {
	c := Config{}.WithDefaults()
	if c.DataDiskSize != DefaultDataDiskSize || c.MemoryMiB != DefaultMemoryMiB || c.CPUs != DefaultCPUs || c.ConsoleLogMaxBytes != DefaultConsoleLogMaxBytes {
		t.Fatalf("defaults not applied: %+v", c)
	}
	c = Config{DataDiskSize: 1 << 30, MemoryMiB: 1024, CPUs: 4, ConsoleLogMaxBytes: 1}.WithDefaults()
	if c.DataDiskSize != 1<<30 || c.MemoryMiB != 1024 || c.CPUs != 4 || c.ConsoleLogMaxBytes != 1 {
		t.Fatalf("explicit values overridden: %+v", c)
	}
}

func TestValidate(t *testing.T) {
	if err := validConfig().Validate(); err != nil {
		t.Fatalf("valid config: %v", err)
	}
	if err := (Config{}).Validate(); err == nil {
		t.Fatal("zero config accepted")
	} else {
		for _, want := range []string{"bundle directory", "data disk path", "console log path", "data disk size", "memory", "CPU", "size cap"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("zero config error %q does not mention %q", err, want)
			}
		}
	}
	for name, tc := range map[string]struct {
		mod  func(*Config)
		want string
	}{
		"odd disk size": {func(c *Config) { c.DataDiskSize = 1000 }, "multiple of 512"},
		"low memory":    {func(c *Config) { c.MemoryMiB = 256 }, "below"},
		"newline":       {func(c *Config) { c.ExtraCmdline = "a\nb" }, "single line"},
		"console":       {func(c *Config) { c.ExtraCmdline = "quiet console=ttyS0" }, "console"},
		"care.data":     {func(c *Config) { c.ExtraCmdline = "care.data=/dev/vda" }, "care.data"},
		"care.system":   {func(c *Config) { c.ExtraCmdline = "care.system" }, "care.system"},
	} {
		t.Run(name, func(t *testing.T) {
			c := validConfig()
			tc.mod(&c)
			err := c.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want error containing %q", err, tc.want)
			}
		})
	}
}

func TestCmdline(t *testing.T) {
	c := validConfig()
	if got, want := c.Cmdline("hvc0"), "console=hvc0 care.system=/dev/vda care.data=/dev/vdb"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	c.ExtraCmdline = "  care.debug   loglevel=7 "
	if got, want := c.Cmdline("ttyS0"), "console=ttyS0 care.system=/dev/vda care.data=/dev/vdb care.debug loglevel=7"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func writeBundle(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{KernelFile, InitrdFile, SystemImgFile} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestValidateBundle(t *testing.T) {
	dir := writeBundle(t)
	if err := ValidateBundle(dir); err != nil {
		t.Fatalf("valid bundle: %v", err)
	}

	if err := ValidateBundle(filepath.Join(dir, "nope")); err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing dir: %v", err)
	}
	if err := ValidateBundle(filepath.Join(dir, KernelFile)); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("file as bundle: %v", err)
	}

	if err := os.Remove(filepath.Join(dir, KernelFile)); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(filepath.Join(dir, InitrdFile), 0); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, SystemImgFile)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, SystemImgFile), 0o755); err != nil {
		t.Fatal(err)
	}
	err := ValidateBundle(dir)
	for _, want := range []string{"missing vmlinuz", "initramfs is empty", "system.img is not a regular file"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("got %v, want error containing %q", err, want)
		}
	}
}

func TestEnsureDataDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.img")
	const size = 8 << 30

	created, err := EnsureDataDisk(path, size)
	if err != nil || !created {
		t.Fatalf("create: created=%v err=%v", created, err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() != size {
		t.Errorf("size = %d, want %d", st.Size(), size)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %v, want 0600", perm)
	}
	assertSparse(t, path, st)

	// An existing disk is left alone, whatever its size.
	if err := os.Truncate(path, 1<<20); err != nil {
		t.Fatal(err)
	}
	created, err = EnsureDataDisk(path, size)
	if err != nil || created {
		t.Fatalf("existing: created=%v err=%v", created, err)
	}
	if st, _ := os.Stat(path); st.Size() != 1<<20 {
		t.Errorf("existing disk resized to %d", st.Size())
	}
}

func TestEnsureDataDiskErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := EnsureDataDisk(dir, 1<<20); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("directory: %v", err)
	}
	if _, err := EnsureDataDisk(filepath.Join(dir, "missing", "data.img"), 1<<20); err == nil || !strings.Contains(err.Error(), "create data disk") {
		t.Errorf("missing parent: %v", err)
	}
	if _, err := EnsureDataDisk(filepath.Join(dir, "odd.img"), 1000); err == nil {
		t.Error("odd size accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "odd.img")); !os.IsNotExist(err) {
		t.Errorf("odd-size disk was created: %v", err)
	}
}

func TestStateString(t *testing.T) {
	for s, want := range map[State]string{StateNew: "new", StateRunning: "running", StateError: "error", State(42): "State(42)"} {
		if got := s.String(); got != want {
			t.Errorf("%d: got %q, want %q", int(s), got, want)
		}
	}
}
