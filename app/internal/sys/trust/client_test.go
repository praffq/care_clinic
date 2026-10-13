package trust

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ohcnetwork/care_desktop/app/internal/sys/elevate"
)

func TestClientAddress(t *testing.T) {
	for _, address := range []string{"care", "CARE.local", " https://care.local/ ", "http://care.local"} {
		got, err := ClientURL(address)
		if err != nil || got != "https://care.local" {
			t.Fatalf("%q: %q, %v", address, got, err)
		}
	}
	for _, address := range []string{
		"", "https://", "https://care.local:443", "care.local:", "care.local/setup", "https://person@care.local",
		"care.local?x=1", "care.local?", "care.local#", "127.0.0.1", "[::1]", "clinic.example.com",
		"file:///etc/passwd", "care.local/../", "care.local/%2f", "-care.local", "care..local",
		"care.local\nbad", "care;echo.local",
	} {
		if _, err := ClientURL(address); err == nil {
			t.Errorf("accepted invalid address %q", address)
		}
	}
}

func TestClientCertificateDownloadValidation(t *testing.T) {
	root, _ := clientTestTLS(t, time.Now().Add(time.Hour))
	for _, tc := range []struct {
		name, body string
		status     int
		valid      bool
	}{
		{"root", root, 200, true},
		{"redirect", root, 302, false},
		{"missing", "not found", 404, false},
		{"html", "<html>setup page</html>", 200, false},
		{"oversized", strings.Repeat("x", 64*1024+1), 200, false},
		{"bundle", root + root, 200, false},
		{"trailing content", root + "unexpected data", 200, false},
		{"wrong root", string(certPEM(t, "Other service")), 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body))}
			got, err := readClientCertificate(response)
			if (err == nil) != tc.valid || (tc.valid && got != root) {
				t.Fatalf("certificate validation = %q, %v", got, err)
			}
		})
	}
	expired, _ := clientTestTLS(t, time.Now().Add(-time.Hour))
	if _, err := clientRoot(expired, true); err == nil {
		t.Fatal("expired root was accepted for installation")
	}
	if _, err := clientRoot(expired, false); err != nil {
		t.Fatalf("expired root cannot be removed: %v", err)
	}
}

func TestClientTLSUsesClinicNameAndPinnedRoot(t *testing.T) {
	root, certificate := clientTestTLS(t, time.Now().Add(time.Hour))
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}}
	server.StartTLS()
	defer server.Close()
	for _, tc := range []struct {
		name, host, root string
		ok               bool
	}{
		{"clinic", "care.local", root, true},
		{"wrong host", "other.local", root, false},
		{"replaced root", "care.local", string(certPEM(t, CommonName)), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config, err := clientTLSConfig(tc.host, tc.root)
			if err != nil {
				t.Fatal(err)
			}
			conn, err := tls.Dial("tcp", server.Listener.Addr().String(), config)
			if conn != nil {
				_ = conn.Close()
			}
			if (err == nil) != tc.ok {
				t.Fatalf("TLS verification: %v", err)
			}
		})
	}
}

func TestSelectRemovableNeverRemovesThePinnedRoot(t *testing.T) {
	const pinned = "AABBCCDDEEFF00112233445566778899AABBCCDD"
	const other = "1122334455667788990011223344556677889900"
	for _, tc := range []struct {
		name  string
		found []string
		keep  string
		want  []string
	}{
		{"nothing trusted", nil, pinned, nil},
		{"only the clinic being connected to", []string{pinned}, pinned, nil},
		{"spaced and lower case spelling of the same root",
			[]string{strings.ToLower("AA BB CC DD EE FF 00 11 22 33 44 55 66 77 88 99 AA BB CC DD")}, pinned, nil},
		{"an earlier clinic", []string{other, pinned}, pinned, []string{other}},
		{"duplicates across two stores", []string{other, other}, pinned, []string{other}},
		{"no clinic pinned yet", []string{other, pinned}, "", []string{other, pinned}},
		{"blank entries", []string{"", "  "}, pinned, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := selectRemovable(tc.found, tc.keep)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("selectRemovable(%v, %q) = %v, want %v", tc.found, tc.keep, got, tc.want)
			}
		})
	}
}

func TestHexLinesIgnoresEverythingThatIsNotAFingerprint(t *testing.T) {
	got := hexLines("A1B2C3\n\nnot a hash\n a1 b2 c3 \nCN=CARE Clinic Local CA\n")
	if !slices.Equal(got, []string{"A1B2C3"}) {
		t.Fatalf("hexLines = %v", got)
	}
}

func TestLinuxAnchorsListOnlyOtherCARoots(t *testing.T) {
	dir := t.TempDir()
	clinic := certPEM(t, CommonName)
	earlier := certPEM(t, CommonName)
	write := func(name string, data []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("care-root.crt", earlier)
	write("care-client-new.crt", clinic)
	write("someone-else.crt", certPEM(t, "Another organisation"))
	write("notes.txt", clinic)
	write("damaged.crt", []byte("not a certificate"))

	roots, err := linuxCARoots([]string{dir, filepath.Join(dir, "missing")}, SHA1Hex(string(clinic)))
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 1 || roots[0].Fingerprint != SHA1Hex(string(earlier)) ||
		roots[0].Store != filepath.Join(dir, "care-root.crt") {
		t.Fatalf("linuxCARoots = %+v", roots)
	}
	all, err := linuxCARoots([]string{dir}, "")
	if err != nil || len(all) != 2 {
		t.Fatalf("an unpinned client should see every CARE root: %+v, %v", all, err)
	}
}

func TestLinuxClientCertificateRequiresTheRootInTheGeneratedBundle(t *testing.T) {
	root := certPEM(t, CommonName)
	cert, err := clientRoot(string(root), false)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	anchor := filepath.Join(dir, "care-client.crt")
	bundle := filepath.Join(dir, "ca-bundle.crt")
	if err := os.WriteFile(anchor, root, 0o644); err != nil {
		t.Fatal(err)
	}
	present, err := linuxClientCertificatePresent(cert, []string{anchor}, []string{bundle})
	if err != nil || present {
		t.Fatalf("incomplete install = %v, %v; want not present", present, err)
	}
	if err := os.WriteFile(bundle, append(certPEM(t, "unrelated root"), root...), 0o644); err != nil {
		t.Fatal(err)
	}
	present, err = linuxClientCertificatePresent(cert, []string{anchor}, []string{bundle})
	if err != nil || !present {
		t.Fatalf("completed install = %v, %v; want present", present, err)
	}
}

func TestRemoveOtherRootsStepsTargetFingerprintsNotTheCommonName(t *testing.T) {
	const stale = "1122334455667788990011223344556677889900"
	for _, goos := range []string{"darwin", "windows", "linux"} {
		t.Run(goos, func(t *testing.T) {
			if admin, user := removeOtherRootsSteps(goos, nil, nil); admin != nil || user != nil {
				t.Fatal("a computer with no other CARE roots must ask for nothing")
			}
			roots := []CARoot{{Fingerprint: stale, Store: "/etc/anchors/care-root.crt"}}
			if goos == "darwin" {
				roots = []CARoot{
					{Fingerprint: stale, Store: darwinLoginKeychain()},
					{Fingerprint: stale, Store: darwinSystemKeychain},
				}
			}
			paths := map[string]string{}
			for _, root := range roots {
				paths[rootKey(root)] = "/prepared/exact-root.crt"
			}
			admin, user := removeOtherRootsSteps(goos, roots, paths)
			script := ""
			for _, step := range append(append([]elevate.Step{}, admin...), user...) {
				script += step.Sh + step.PS + "\n"
			}
			if strings.Contains(script, CommonName) {
				t.Fatalf("removal by name would delete the clinic's own root: %s", script)
			}
			want := stale
			if goos == "linux" {
				want = "/etc/anchors/care-root.crt"
			}
			if !strings.Contains(script, want) {
				t.Fatalf("%s removal does not target %q: %s", goos, want, script)
			}
			// macOS refuses trust changes to the login keychain from a root
			// script, so that half must stay outside the elevated batch.
			if (goos == "darwin") != (len(user) == 1) {
				t.Fatalf("%s split removal wrongly: admin %d, user %d", goos, len(admin), len(user))
			}
			if goos == "darwin" {
				if !strings.Contains(admin[0].Sh, "remove-trusted-cert -d '/prepared/exact-root.crt' && security delete-certificate -t -Z "+stale) {
					t.Fatalf("System cleanup did not clear admin settings before exact deletion: %s", admin[0].Sh)
				}
				if strings.Contains(user[0].Sh, "remove-trusted-cert") || !strings.Contains(user[0].Sh, "delete-certificate -t -Z "+stale) {
					t.Fatalf("login cleanup must use documented user-settings deletion: %s", user[0].Sh)
				}
			}
		})
	}
}

func TestLinuxClientRemovalHandlesOwnedArtifactsAndBundleResidue(t *testing.T) {
	for _, tc := range []struct {
		name, anchorState                                               string
		bundled, leaveAnchor, leaveBundle, runError, wantRun, wantError bool
	}{
		{name: "incomplete installation", anchorState: "owned", wantRun: true},
		{name: "complete installation", anchorState: "owned", bundled: true, wantRun: true},
		{name: "bundle only residue", bundled: true, wantRun: true},
		{name: "already removed"},
		{name: "changed CARE root", anchorState: "changed", wantError: true},
		{name: "unowned certificate", anchorState: "unowned", wantError: true},
		{name: "symlink", anchorState: "symlink", wantError: true},
		{name: "anchor survives", anchorState: "owned", leaveAnchor: true, wantRun: true, wantError: true},
		{name: "bundle survives", anchorState: "owned", bundled: true, leaveBundle: true, wantRun: true, wantError: true},
		{name: "refresh fails", anchorState: "owned", runError: true, wantRun: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := certPEM(t, CommonName)
			cert, err := clientRoot(string(root), false)
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			anchor, bundle := filepath.Join(dir, "anchor.crt"), filepath.Join(dir, "bundle.crt")
			write := func(path string, data []byte) {
				t.Helper()
				if err := os.WriteFile(path, data, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			switch tc.anchorState {
			case "owned":
				write(anchor, root)
			case "changed":
				write(anchor, certPEM(t, CommonName))
			case "unowned":
				write(anchor, certPEM(t, "unrelated"))
			case "symlink":
				write(anchor+".target", root)
				if err := os.Symlink(anchor+".target", anchor); err != nil {
					t.Fatal(err)
				}
			}
			if tc.bundled {
				write(bundle, root)
			}
			called := false
			err = removeLinuxClientCertificate(cert, []string{anchor}, []string{bundle}, func(sh string) error {
				called = true
				compare, remove := strings.Index(sh, "cmp -s"), strings.Index(sh, "rm -f")
				if compare < 0 || remove <= compare || !strings.Contains(sh, "[ ! -L ") {
					t.Fatalf("deletion lacks an exact-content and symlink guard: %s", sh)
				}
				if tc.runError {
					return errors.New("refresh failed")
				}
				if !tc.leaveAnchor {
					if err := os.Remove(anchor); err != nil && !os.IsNotExist(err) {
						t.Fatal(err)
					}
				}
				if !tc.leaveBundle {
					write(bundle, certPEM(t, "unrelated"))
				}
				return nil
			})
			if called != tc.wantRun || (err != nil) != tc.wantError {
				t.Fatalf("called=%v err=%v, want run=%v error=%v", called, err, tc.wantRun, tc.wantError)
			}
			if !tc.wantRun && tc.anchorState != "" {
				if _, err := os.Lstat(anchor); err != nil {
					t.Fatalf("changed or unowned anchor was removed: %v", err)
				}
			}
		})
	}
}

func TestInstalledClientTrustRetainsCleanupFailures(t *testing.T) {
	stale := CARoot{Fingerprint: "AA", Store: "/login"}
	inspection := errors.New("inspection denied")
	command := errors.New("trust removal denied")
	for _, tc := range []struct {
		name                   string
		remaining              []CARoot
		inspectErr, cleanupErr error
		wantError              bool
	}{
		{name: "confirmed removal"},
		{name: "inspection fails after installation", inspectErr: inspection, wantError: true},
		{name: "arbitrary command error despite no remaining certificates", cleanupErr: command, wantError: true},
		{name: "both errors", inspectErr: inspection, cleanupErr: command, wantError: true},
		{name: "stale certificate remains", remaining: []CARoot{stale}, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			removed, err := clientTrustCleanupResult([]CARoot{stale}, tc.remaining, tc.inspectErr, tc.cleanupErr)
			if (err != nil) != tc.wantError {
				t.Fatalf("removed=%v error=%v", removed, err)
			}
			if err != nil && !strings.Contains(err.Error(), "certificate was installed, but") {
				t.Fatalf("lost partial-install outcome: %v", err)
			}
			if tc.inspectErr != nil && (!errors.Is(err, tc.inspectErr) || len(removed) != 0) {
				t.Fatalf("inspection error hidden or unverified removals reported: %v, %v", removed, err)
			}
			if tc.cleanupErr != nil && !errors.Is(err, tc.cleanupErr) {
				t.Fatalf("arbitrary command error was swallowed: %v", err)
			}
		})
	}
}

func TestClientUserScriptAttemptsInstallWithoutSwallowingCleanupErrors(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fixture")
	}
	for _, tc := range []struct {
		name, cleanup, install string
		wantError              bool
	}{
		{"success", "true", "printf installed", false},
		{"cleanup fails", "false", "printf installed", true},
		{"install fails", "true", "printf installed; exit 1", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sh := clientUserScript([]elevate.Step{{Sh: tc.cleanup}, {Sh: tc.install}})
			out, err := exec.Command("sh", "-c", sh).CombinedOutput()
			if (err != nil) != tc.wantError || string(out) != "installed" {
				t.Fatalf("output=%q error=%v; installation must run and cleanup errors must survive", out, err)
			}
		})
	}
}

func TestLinuxClientRemovalRechecksOwnershipBeforePrivilegedDeletion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fixture")
	}
	for _, change := range []string{"replace", "symlink"} {
		t.Run(change, func(t *testing.T) {
			root := certPEM(t, CommonName)
			cert, err := clientRoot(string(root), false)
			if err != nil {
				t.Fatal(err)
			}
			anchor := filepath.Join(t.TempDir(), "anchor.crt")
			if err := os.WriteFile(anchor, root, 0o644); err != nil {
				t.Fatal(err)
			}
			err = removeLinuxClientCertificate(cert, []string{anchor}, nil, func(sh string) error {
				changed := certPEM(t, "unowned")
				if change == "replace" {
					if err := os.WriteFile(anchor, changed, 0o644); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.WriteFile(anchor+".target", root, 0o644); err != nil {
						t.Fatal(err)
					}
					if err := os.Remove(anchor); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(anchor+".target", anchor); err != nil {
						t.Fatal(err)
					}
				}
				guards, _, found := strings.Cut(sh, "set -e\nrm -f")
				if !found {
					t.Fatal("missing removal boundary")
				}
				// Run only read-only guard checks, never trust-store commands.
				// An && wrapper must not defeat explicit ownership failures.
				out, err := exec.Command("sh", "-c", "( "+guards+"printf guard-passed ) && true").CombinedOutput()
				if err == nil || strings.Contains(string(out), "guard-passed") {
					t.Fatalf("ownership guard accepted %s: %q, %v", change, out, err)
				}
				return err
			})
			if err == nil {
				t.Fatal("changed privileged target reported successful removal")
			}
			if _, err := os.Lstat(anchor); err != nil {
				t.Fatalf("changed target was deleted: %v", err)
			}
		})
	}
}

func TestApplyClientTrustReportsOnlyRootsConfirmedRemoved(t *testing.T) {
	removed := CARoot{Fingerprint: "AABB", Store: "/keychain/login"}
	stillThere := CARoot{Fingerprint: "CCDD", Store: "/keychain/login"}
	otherStore := CARoot{Fingerprint: "AABB", Store: "/keychain/system"}
	got := confirmedRemovals(
		[]CARoot{removed, stillThere, otherStore},
		[]CARoot{stillThere, otherStore},
	)
	if !slices.Equal(got, []CARoot{removed}) {
		t.Fatalf("confirmedRemovals = %+v, want only %+v", got, removed)
	}
}

func TestProbeClientSeparatesSilenceFromAnUntrustedAnswer(t *testing.T) {
	root, certificate := clientTestTLS(t, time.Now().Add(time.Hour))
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}}
	server.StartTLS()
	address := server.Listener.Addr().String()

	closed, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	silent := closed.Addr().String()
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, host, root, address string
		reachable                 bool
		detail                    string
	}{
		{"the clinic answers", "care.local", root, address, true, ""},
		{"nothing is listening", "care.local", root, silent, false, "the server did not answer"},
		{"another clinic's certificate", "care.local", string(certPEM(t, CommonName)), address,
			false, "the connection could not be verified"},
		{"the wrong clinic name", "other.local", root, address,
			false, "the connection could not be verified"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config, err := clientTLSConfig(tc.host, tc.root)
			if err != nil {
				t.Fatal(err)
			}
			got := probeClient(context.Background(), config, tc.address)
			if got.Reachable != tc.reachable || got.Detail != tc.detail {
				t.Fatalf("probeClient = %+v, want reachable %v with %q", got, tc.reachable, tc.detail)
			}
		})
	}
	server.Close()

	// A clinic whose pinned root has expired is a state to report, not a
	// question the screen asked wrongly.
	expired, _ := clientTestTLS(t, time.Now().Add(-time.Hour))
	probe, err := ProbeClient(context.Background(), "care.local", expired)
	if err != nil || probe.Reachable || probe.Detail != "the clinic's security certificate is no longer valid" {
		t.Fatalf("expired pin: %+v, %v", probe, err)
	}
	if _, err := ProbeClient(context.Background(), "not a clinic address", root); err == nil {
		t.Fatal("an address that is not a clinic address should be a misuse error")
	}
}

func TestClientCertificateStepsInstallAndRemoveOnlyOwnedCertificate(t *testing.T) {
	root := string(certPEM(t, CommonName))
	cert, err := clientRoot(root, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, goos := range []string{"darwin", "windows", "linux"} {
		step, err := clientCertificateStep(goos, root, "/tmp/clinic certificate.crt", false)
		if err != nil || !strings.Contains(step.Sh+step.PS, "/tmp/clinic certificate.crt") {
			t.Fatalf("%s install: %+v, %v", goos, step, err)
		}
		if goos == "darwin" && !strings.Contains(step.Sh, "-p ssl") {
			t.Fatalf("macOS client trust is missing the SSL-only policy: %s", step.Sh)
		}
		step, err = clientCertificateStep(goos, root, "", true)
		if err != nil {
			t.Fatal(err)
		}
		script := step.Sh + step.PS
		want := SHA1Hex(root)
		if goos == "linux" {
			want = clientAnchors(cert)[0]
			if strings.Contains(script, "/care-root.crt") {
				t.Fatal("client removal targets server certificate")
			}
		}
		if !strings.Contains(script, want) || strings.Contains(script, CommonName) {
			t.Fatalf("%s removal must target only the exact certificate: %s", goos, script)
		}
	}
	if _, err := clientCertificateStep("windows", "bad root", "", true); err == nil {
		t.Fatal("invalid certificate accepted for removal")
	}
}

func clientTestTLS(t *testing.T, notAfter time.Time) (string, tls.Certificate) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: CommonName},
		NotBefore: time.Now().Add(-2 * time.Hour), NotAfter: notAfter,
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	root := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	leaf := &x509.Certificate{
		SerialNumber: big.NewInt(2), DNSNames: []string{"care.local"},
		NotBefore: ca.NotBefore, NotAfter: ca.NotAfter,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, ca, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	return root, tls.Certificate{Certificate: [][]byte{leafDER}, PrivateKey: key}
}
