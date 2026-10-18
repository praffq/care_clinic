package slots_test

import (
	"archive/tar"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ohcnetwork/care_desktop/app/internal/service/slots"
	"github.com/ohcnetwork/care_desktop/app/internal/service/slots/slotstest"
)

const arch = "arm64"

func newStore(t *testing.T) (*slots.Store, string) {
	dir := filepath.Join(t.TempDir(), "slots")
	return slots.New(dir, arch), dir
}

func install(t *testing.T, s *slots.Store, version string) slots.Slot {
	t.Helper()
	path, sum := slotstest.WriteTestBundle(t, slotstest.BundleEntries(version, arch))
	sl, err := s.Install(t.Context(), path, sum)
	if err != nil {
		t.Fatalf("install %s: %v", version, err)
	}
	return sl
}

func TestInstallGoodBundle(t *testing.T) {
	s, dir := newStore(t)
	path, sum := slotstest.WriteTestBundle(t, slotstest.BundleEntries("1.2.3", arch))
	sl, err := s.Install(t.Context(), path, strings.ToUpper(sum))
	if err != nil {
		t.Fatal(err)
	}
	if sl.Version != "1.2.3" || sl.Arch != arch || sl.TarballSHA256 != sum || sl.Dir != filepath.Join(dir, "1.2.3") || sl.DataSchema != 1 {
		t.Fatalf("slot = %+v", sl)
	}
	for _, f := range []string{"manifest.json", "vmlinuz", "initramfs", "system.img", ".verified"} {
		if _, err := os.Stat(filepath.Join(sl.Dir, f)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Verify("1.2.3"); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Fatalf("slots dir has leftovers: %v", ents)
	}
	// Idempotent.
	again, err := s.Install(t.Context(), path, sum)
	if err != nil || again != sl {
		t.Fatalf("reinstall = %+v, %v", again, err)
	}
}

func TestInstallRejects(t *testing.T) {
	good := func() []slotstest.Entry { return slotstest.BundleEntries("1.0.0", arch) }
	top := "care-engine-1.0.0-" + arch
	cases := map[string][]slotstest.Entry{
		"tampered file": func() []slotstest.Entry {
			e := good()
			e[2].Body = []byte("evil kernel")
			return e
		}(),
		"traversal": append(good(), slotstest.Entry{Name: top + "/../evil", Body: []byte("x")}),
		"absolute":  append(good(), slotstest.Entry{Name: "/etc/evil", Body: []byte("x")}),
		"symlink": func() []slotstest.Entry {
			e := good()
			e[3] = slotstest.Entry{Name: top + "/initramfs", Type: tar.TypeSymlink, Linkname: "/etc/passwd"}
			return e
		}(),
		"hardlink":   append(good()[:4], slotstest.Entry{Name: top + "/system.img", Type: tar.TypeLink, Linkname: top + "/vmlinuz"}),
		"extra file": append(good(), slotstest.Entry{Name: top + "/extra", Body: []byte("x")}),
		"duplicate":  append(good(), slotstest.Entry{Name: top + "/vmlinuz", Body: []byte("kernel 1.0.0")}),
		"nested dir": append(good(), slotstest.Entry{Name: top + "/sub/", Type: tar.TypeDir}),
		"two roots":  append(good(), slotstest.Entry{Name: "care-engine-2.0.0-arm64/vmlinuz", Body: []byte("x")}),
		"missing":    good()[:4],
		"wrong arch": slotstest.BundleEntries("1.0.0", "amd64"),
		"bad version": func() []slotstest.Entry {
			e := slotstest.BundleEntries("1.0", arch)
			return e
		}(),
		"dir name mismatch": func() []slotstest.Entry {
			e := good()
			for i := range e {
				e[i].Name = strings.Replace(e[i].Name, "1.0.0", "9.9.9", 1)
			}
			return e
		}(),
	}
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			s, dir := newStore(t)
			path, sum := slotstest.WriteTestBundle(t, entries)
			if _, err := s.Install(t.Context(), path, sum); err == nil {
				t.Fatal("install succeeded")
			} else {
				t.Log(err)
			}
			ents, _ := os.ReadDir(dir)
			if len(ents) != 0 {
				t.Fatalf("slots dir not clean: %v", ents)
			}
			if _, err := os.Stat("/etc/evil"); err == nil {
				t.Fatal("wrote outside the slot")
			}
		})
	}
}

func TestInstallBadSHA(t *testing.T) {
	s, _ := newStore(t)
	path, _ := slotstest.WriteTestBundle(t, slotstest.BundleEntries("1.0.0", arch))
	_, err := s.Install(t.Context(), path, strings.Repeat("0", 64))
	if !errors.Is(err, slots.ErrChecksum) {
		t.Fatalf("err = %v", err)
	}
	if _, err := s.Install(t.Context(), path, "abc"); err == nil {
		t.Fatal("short sha accepted")
	}
	if _, err := s.Install(t.Context(), filepath.Dir(path), strings.Repeat("0", 64)); err == nil {
		t.Fatal("directory accepted")
	}
}

func TestVerifyDetectsTampering(t *testing.T) {
	s, _ := newStore(t)
	sl := install(t, s, "1.0.0")
	if err := os.WriteFile(filepath.Join(sl.Dir, "system.img"), []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Verify("1.0.0"); !errors.Is(err, slots.ErrChecksum) {
		t.Fatalf("verify = %v", err)
	}
	// Reinstalling the same tarball repairs a non-current slot.
	install(t, s, "1.0.0")
	if _, err := s.Verify("1.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(sl.Dir, ".verified")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Verify("1.0.0"); !errors.Is(err, slots.ErrNotInstalled) {
		t.Fatalf("verify without marker = %v", err)
	}
	if _, err := s.Verify("../x"); err == nil {
		t.Fatal("bad version accepted")
	}
}

func TestActivatePreviousAndPrune(t *testing.T) {
	s, dir := newStore(t)
	if _, err := s.Current(); !errors.Is(err, slots.ErrNoCurrent) {
		t.Fatalf("current = %v", err)
	}
	for _, v := range []string{"1.0.0", "1.1.0", "1.2.0", "1.10.0"} {
		install(t, s, v)
	}
	if err := s.Activate("9.9.9"); !errors.Is(err, slots.ErrNotInstalled) {
		t.Fatalf("activate missing = %v", err)
	}
	must(t, s.Activate("1.0.0"))
	must(t, s.Activate("1.1.0"))
	must(t, s.Activate("1.2.0"))
	must(t, s.Activate("1.2.0"))
	p, err := s.Pointer()
	if err != nil || p != (slots.Pointer{Current: "1.2.0", Previous: "1.1.0"}) {
		t.Fatalf("pointer = %+v, %v", p, err)
	}
	cur, err := s.Current()
	if err != nil || cur.Version != "1.2.0" {
		t.Fatalf("current = %+v, %v", cur, err)
	}
	list, _ := s.List()
	var vs []string
	for _, sl := range list {
		vs = append(vs, sl.Version)
	}
	if !slices.Equal(vs, []string{"1.0.0", "1.1.0", "1.2.0", "1.10.0"}) {
		t.Fatalf("list = %v", vs)
	}

	must(t, os.Mkdir(filepath.Join(dir, ".install-stale"), 0o755))
	removed, err := s.Prune()
	must(t, err)
	slices.Sort(removed)
	if !slices.Equal(removed, []string{"1.0.0", "1.10.0"}) {
		t.Fatalf("removed = %v", removed)
	}
	ents, _ := os.ReadDir(dir)
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	if !slices.Equal(names, []string{"1.1.0", "1.2.0", "current"}) {
		t.Fatalf("left = %v", names)
	}

	// Rolling back swaps current and previous.
	must(t, s.Activate("1.1.0"))
	p, _ = s.Pointer()
	if p != (slots.Pointer{Current: "1.1.0", Previous: "1.2.0"}) {
		t.Fatalf("pointer after rollback = %+v", p)
	}
}

func TestNeverOverwriteCurrent(t *testing.T) {
	s, _ := newStore(t)
	sl := install(t, s, "1.0.0")
	must(t, s.Activate("1.0.0"))
	// A different tarball for the same version.
	e := slotstest.BundleEntries("1.0.0", arch)
	e = append(e[:1], e[1:]...)
	e[1], e[2] = e[2], e[1]
	path, sum := slotstest.WriteTestBundle(t, e)
	if sum == sl.TarballSHA256 {
		t.Fatal("test bundle unchanged")
	}
	if _, err := s.Install(t.Context(), path, sum); !errors.Is(err, slots.ErrIsCurrent) {
		t.Fatalf("install over current = %v", err)
	}
	got, err := s.Verify("1.0.0")
	if err != nil || got.TarballSHA256 != sl.TarballSHA256 {
		t.Fatalf("current changed: %+v, %v", got, err)
	}
	removed, err := s.Prune()
	if err != nil || len(removed) != 0 {
		t.Fatalf("prune = %v, %v", removed, err)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
